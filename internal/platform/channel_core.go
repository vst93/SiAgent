package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"siagent/internal/channels"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Server) archiveChannel(w http.ResponseWriter, r *http.Request) {
	s.channelConfigMu.Lock()
	defer s.channelConfigMu.Unlock()
	if s.channelTests[r.PathValue("channel")] {
		fail(w, 409, "渠道正在连接测试，请等待结束后再归档。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _, err := s.loadChannel(r.Context(), r.PathValue("channel"))
	if err != nil {
		fail(w, 404, "渠道不存在。")
		return
	}
	if c.Enabled {
		fail(w, 409, "请先停用渠道。归档会停止尚未发送的回复，历史保留。")
		return
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback()
	var sending int
	if err := tx.QueryRow("SELECT count(*) FROM channel_outbox WHERE channel_id=? AND state='sending'", c.ID).Scan(&sending); err != nil {
		internal(w)
		return
	}
	if sending > 0 {
		fail(w, 409, "仍有发送请求在途，请稍后再归档。")
		return
	}
	if _, err := tx.Exec("UPDATE channels SET archived=1,agent_id='',enabled=0 WHERE id=?", c.ID); err != nil {
		internal(w)
		return
	}
	if _, err := tx.Exec("UPDATE channel_outbox SET state='cancelled',error='渠道已归档，停止发送。' WHERE channel_id=? AND state IN ('pending','retry')", c.ID); err != nil {
		internal(w)
		return
	}
	if _, err := tx.Exec("UPDATE channel_inbox SET finalized=1,state=CASE WHEN state='pending' THEN 'rejected' ELSE state END,error=CASE WHEN state='pending' THEN '渠道已归档。' ELSE error END WHERE channel_id=?", c.ID); err != nil {
		internal(w)
		return
	}
	if err := tx.Commit(); err != nil {
		internal(w)
		return
	}
	// A rejected message never reaches a run, so its staged pictures are unusable.
	s.resetChannelMedia(c.ID)
	respond(w, 200, map[string]bool{"archived": true})
}

func (s *Server) channelDiagnostics(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id,msg_id,member,agent_id,state,error,run_id,conversation_id,created,substr(content,1,200) FROM channel_inbox WHERE channel_id=? ORDER BY created DESC,rowid DESC LIMIT 50", r.PathValue("channel"))
	if err != nil {
		internal(w)
		return
	}
	inbox := []map[string]any{}
	for rows.Next() {
		var id, msg, member, agent, state, reason, run, conversation, preview string
		var created int64
		if err := rows.Scan(&id, &msg, &member, &agent, &state, &reason, &run, &conversation, &created, &preview); err != nil {
			rows.Close()
			internal(w)
			return
		}
		inbox = append(inbox, map[string]any{"id": id, "msg_id": msg, "member": member, "agent_id": agent, "state": state, "error": reason, "run_id": run, "conversation_id": conversation, "created": created, "preview": preview})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w)
		return
	}
	rows, err = s.store.db.Query("SELECT id,inbox_id,member,kind,part,total,state,attempts,code,error,created FROM channel_outbox WHERE channel_id=? ORDER BY created DESC,rowid DESC LIMIT 100", r.PathValue("channel"))
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	outbox := []map[string]any{}
	for rows.Next() {
		var id, inboxID, member, kind, state, reason string
		var part, total, attempts, code int
		var created int64
		if err := rows.Scan(&id, &inboxID, &member, &kind, &part, &total, &state, &attempts, &code, &reason, &created); err != nil {
			internal(w)
			return
		}
		outbox = append(outbox, map[string]any{"id": id, "inbox_id": inboxID, "member": member, "kind": kind, "part": part + 1, "total": total, "state": state, "attempts": attempts, "code": code, "error": reason, "created": created})
	}
	if rows.Err() != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]any{"inbox": inbox, "outbox": outbox, "status": s.channelConnectionStatus(r.PathValue("channel"))})
}
func (s *Server) retryChannelSend(w http.ResponseWriter, r *http.Request) {
	s.channelWork.Lock()
	defer s.channelWork.Unlock()
	tx, err := s.store.db.Begin()
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback()
	var inbox, kind string
	var part int
	if err := tx.QueryRow("SELECT o.inbox_id,o.kind,o.part FROM channel_outbox o JOIN channels c ON c.id=o.channel_id WHERE o.id=? AND o.state='failed' AND c.archived=0 AND NOT EXISTS(SELECT 1 FROM channel_inbox i JOIN conversation_cleanup d ON d.conversation_id=i.conversation_id WHERE i.id=o.inbox_id)", r.PathValue("delivery")).Scan(&inbox, &kind, &part); err != nil {
		fail(w, 409, "仅明确失败的发送可重试；结果不明须人工核对，不自动重发。")
		return
	}
	if _, err := tx.Exec("UPDATE channel_outbox SET state='pending',attempts=0,next_at=0,error='' WHERE id=?", r.PathValue("delivery")); err != nil {
		internal(w)
		return
	}
	if _, err := tx.Exec("UPDATE channel_outbox SET state='pending',error='' WHERE inbox_id=? AND kind=? AND part>? AND state='blocked'", inbox, kind, part); err != nil {
		internal(w)
		return
	}
	if err := tx.Commit(); err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]string{"state": "pending"})
}
func (s *Server) recoverChannels() error {
	_, err := s.store.db.Exec("UPDATE channel_outbox SET state='unknown',error='服务在发送确认前退出，请人工核对渠道，不自动重发。' WHERE state='sending'; UPDATE channel_inbox SET state='rejected',error='服务已重启，该消息尚未开始处理，请重新发送。' WHERE state='pending';")
	return err
}
func (s *Server) admitChannelInbox(ctx context.Context) error {
	for range 4 {
		var inbox, channel, member, agent, text string
		var receivedAt int64
		err := s.store.db.QueryRowContext(ctx, "SELECT i.id,i.channel_id,i.member,i.agent_id,i.content,i.created FROM channel_inbox i JOIN channels c ON c.id=i.channel_id WHERE i.state='pending' AND c.enabled=1 AND c.archived=0 ORDER BY i.created,i.rowid LIMIT 1").Scan(&inbox, &channel, &member, &agent, &text, &receivedAt)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		conversation, err := s.channelConversation(ctx, channel, member, agent, text == "/new")
		if err == nil && text == "/new" {
			_, err = s.store.db.ExecContext(ctx, "UPDATE channel_inbox SET state='rejected',error='已开启新的对话，请发送问题。',conversation_id=? WHERE id=?", conversation, inbox)
			if err != nil {
				return err
			}
			continue
		}
		if err == nil {
			images, readErr := s.inboxStagedImages(inbox)
			if readErr != nil {
				if _, err := s.store.db.ExecContext(ctx, "UPDATE channel_inbox SET state='rejected',error='图片消息已损坏，请重新发送。' WHERE id=? AND state='pending'", inbox); err != nil {
					return err
				}
				continue
			}
			// A picture-only message carries no text of its own; the prompt tells the
			// model what to look at without inventing content the member did not send.
			prompt := text
			if prompt == "" && len(images) > 0 {
				prompt = "请查看图片。"
			}
			var attachmentIDs []string
			if len(images) > 0 {
				attachmentIDs, err = s.promoteStagedImages(inbox, conversation, images)
			}
			if err == nil {
				_, err = s.enqueueRunWithAttachments(ctx, conversation, prompt, receivedAt, attachmentIDs, func(tx *sql.Tx, run string) error {
					_, err := tx.ExecContext(ctx, "UPDATE channel_inbox SET state='admitted',run_id=?,conversation_id=? WHERE id=? AND state='pending'", run, conversation, inbox)
					return err
				})
			}
			if err != nil {
				// A rejected run must not leave orphan pictures behind. Promoted IDs stay
				// on the inbox row so a deferred admission can retry them; a rejection
				// removes both the rows and the staging list.
				var rejected *admissionError
				if errors.As(err, &rejected) && rejected.Status != 503 {
					s.discardInboundAttachments(attachmentIDs)
					if _, resetErr := s.store.db.ExecContext(ctx, "UPDATE channel_inbox SET attachments='[]' WHERE id=?", inbox); resetErr != nil {
						return resetErr
					}
				}
			}
		}
		if err != nil {
			var rejected *admissionError
			if !errors.As(err, &rejected) {
				return err
			}
			if rejected.Status == 503 {
				return nil
			}
			if _, err := s.store.db.ExecContext(ctx, "UPDATE channel_inbox SET state='rejected',error=? WHERE id=? AND state='pending'", rejected.Message, inbox); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Server) queueChannelText(tx *sql.Tx, inbox, channel, member, kind, text string, now int64) error {
	var kindID string
	if err := tx.QueryRow("SELECT provider FROM channels WHERE id=?", channel).Scan(&kindID); err != nil {
		return err
	}
	limit := 1800
	if provider, ok := s.channelProviders.Get(kindID); ok {
		limit = provider.Descriptor().MaxTextBytes
	}
	parts := channels.SplitText(text, limit-32)
	for index, part := range parts {
		if len(parts) > 1 {
			part = fmt.Sprintf("（%d/%d）\n%s", index+1, len(parts), part)
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO channel_outbox(id,inbox_id,channel_id,member,kind,part,total,content,created) VALUES(?,?,?,?,?,?,?,?,?)", id(), inbox, channel, member, kind, index, len(parts), part, now); err != nil {
			return err
		}
	}
	return nil
}

// A queued message can look lost once it waits behind other work, while a task
// the model is already running will answer on its own. Notices therefore use
// separate thresholds and text, so ordinary questions see no notice at all.
const (
	queuedNoticeDelay     = 30 * time.Second
	processingNoticeDelay = 90 * time.Second
)

// Channel delivery kinds. Every kind except final is an unfinished stream
// message that the final answer replaces when the adapter supports streaming.
const (
	channelKindAck   = "ack"
	channelKindHint  = "hint"
	channelKindDraft = "draft"
	channelKindFinal = "final"
)

const channelAckText = "已收到你的消息，正在作答，请稍候…"

// draftInterval and draftGrowthBytes throttle progressive channel updates so a
// token-by-token answer does not flood the vendor rate limit.
const (
	draftInterval    = 1500 * time.Millisecond
	draftGrowthBytes = 8
)

// draftMark records the last time a run published a channel draft and how much
// answer text that draft already carried.
type draftMark struct {
	at     time.Time
	length int
}

// queueChannelStreamingText stores a single unfinished message for a streaming
// adapter. It is never split: the stream message is updated in place, and the
// final answer carries the complete text.
func (s *Server) queueChannelStreamingText(tx *sql.Tx, inbox, channel, member, kind, text string, now int64) error {
	_, err := tx.Exec("INSERT OR IGNORE INTO channel_outbox(id,inbox_id,channel_id,member,kind,part,total,content,created) VALUES(?,?,?,?,?,0,1,?,?)", id(), inbox, channel, member, kind, text, now)
	return err
}

// streamingChannel reports whether the bound adapter can update one outbound
// message in place, which is what makes an immediate acknowledgement cheap.
// It also returns the adapter's single-message byte limit.
func (s *Server) streamingChannel(channel string) (bool, int) {
	var provider string
	if err := s.store.db.QueryRow("SELECT provider FROM channels WHERE id=?", channel).Scan(&provider); err != nil {
		return false, 0
	}
	entry, ok := s.channelProviders.Get(provider)
	if !ok {
		return false, 0
	}
	descriptor := entry.Descriptor()
	return descriptor.Streaming, descriptor.MaxTextBytes
}

// supportsChannelImages reports whether the bound adapter declares inbound image
// support. A text-only adapter keeps the text-only notice instead of accepting
// pictures it can never deliver.
func (s *Server) supportsChannelImages(provider string) bool {
	entry, ok := s.channelProviders.Get(provider)
	if !ok {
		return false
	}
	return entry.Descriptor().Images
}

// agentAcceptsImages reports the bound Agent's own switch. Images are on by
// default; an administrator can turn them off for a text-only assistant even
// when the model and adapter could accept them.
func (s *Server) agentAcceptsImages(agentID string) bool {
	a, err := s.agentByID(agentID)
	if err != nil {
		return false
	}
	return a.effectivePolicy().Session.Images
}

// queueChannelAck acknowledges a received message immediately. Only adapters
// that update a message in place get it before the answer, so a legacy adapter
// never sends a second message just to say it is working.
func (s *Server) queueChannelAck(ctx context.Context, inbox, channel, member, agentID string) error {
	if streaming, _ := s.streamingChannel(channel); !streaming {
		return nil
	}
	a, err := s.agentByID(agentID)
	if err != nil {
		return nil
	}
	if !a.effectivePolicy().Output.Acknowledge {
		return nil
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.queueChannelStreamingText(tx, inbox, channel, member, channelKindAck, channelAckText, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// stageChannelDraft publishes the evolving answer so a member sees progress
// instead of waiting for the whole reply. It runs under s.mu from the event
// handler, so the outbox row cannot race a concurrent send.
func (s *Server) stageChannelDraft(runID, answer string) error {
	if s.drafts == nil {
		return nil
	}
	now := time.Now()
	if mark, ok := s.drafts[runID]; ok {
		if now.Sub(mark.at) < draftInterval || len(answer) < mark.length+draftGrowthBytes {
			return nil
		}
	}
	var inbox, channel, member, config string
	err := s.store.db.QueryRow("SELECT i.id,i.channel_id,i.member,r.config FROM channel_inbox i JOIN runs r ON r.id=i.run_id WHERE i.run_id=? AND i.finalized=0 ORDER BY i.rowid LIMIT 1", runID).Scan(&inbox, &channel, &member, &config)
	if err == sql.ErrNoRows {
		// A Web-only run, or one already finalized, never stages a draft.
		delete(s.drafts, runID)
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot runConfig
	if err := json.Unmarshal([]byte(config), &snapshot); err != nil {
		return err
	}
	output := snapshot.Agent.effectivePolicy().Output
	streaming, limit := s.streamingChannel(channel)
	if !output.Stream || !streaming {
		return nil
	}
	text := answer
	if output.ThinkTags != "keep" {
		text = sanitizeAnswer(answer)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	text = truncateBytes(text, limit-32)
	var existing, state string
	var created int64
	err = s.store.db.QueryRow("SELECT content,state,created FROM channel_outbox WHERE inbox_id=? AND kind=? AND part=0", inbox, channelKindDraft).Scan(&existing, &state, &created)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if state == "sending" {
			// The in-flight update will be replaced by the next admitted draft or
			// by the final answer, so nothing is lost by waiting.
			s.drafts[runID] = draftMark{at: now, length: len(answer)}
			return nil
		}
		if existing == text {
			s.drafts[runID] = draftMark{at: now, length: len(answer)}
			return nil
		}
		if _, err := s.store.db.Exec("UPDATE channel_outbox SET content=?,state='pending',attempts=0,next_at=0,error='',created=? WHERE inbox_id=? AND kind=? AND part=0", text, now.UnixMilli(), inbox, channelKindDraft); err != nil {
			return err
		}
	} else {
		if _, err := s.store.db.Exec("INSERT INTO channel_outbox(id,inbox_id,channel_id,member,kind,part,total,content,created) VALUES(?,?,?,?,?,0,1,?,?)", id(), inbox, channel, member, channelKindDraft, text, now.UnixMilli()); err != nil {
			return err
		}
	}
	s.drafts[runID] = draftMark{at: now, length: len(answer)}
	return nil
}

func truncateBytes(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func (s *Server) prepareChannelReplies(ctx context.Context, now time.Time) error {
	rows, err := s.store.db.QueryContext(ctx, "SELECT i.id,i.channel_id,i.member,i.state,i.error,i.run_id,i.created,i.hinted,i.agent_id FROM channel_inbox i JOIN channels c ON c.id=i.channel_id WHERE i.finalized=0 AND c.archived=0 AND (i.hinted=0 OR i.state='rejected' OR EXISTS(SELECT 1 FROM runs r WHERE r.id=i.run_id AND r.status NOT IN ('queued','running','cancelling'))) ORDER BY i.created,i.rowid LIMIT 200")
	if err != nil {
		return err
	}
	type item struct {
		id, channel, member, state, reason, run, agent string
		created                                        int64
		hinted                                         bool
	}
	items := []item{}
	for rows.Next() {
		var i item
		if err := rows.Scan(&i.id, &i.channel, &i.member, &i.state, &i.reason, &i.run, &i.created, &i.hinted, &i.agent); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		done := i.state == "rejected"
		text := i.reason
		progress := true
		// started tells a queued message from one the model is already working on.
		started := false
		if i.run == "" && !done {
			a, err := s.agentByID(i.agent)
			if err != nil {
				return err
			}
			progress = a.Policy.Output.Progress
		}
		if i.run != "" {
			var status, reason, answer, config, thinking string
			if err := s.store.db.QueryRowContext(ctx, "SELECT r.status,r.error,m.content,r.config,r.thinking FROM runs r JOIN messages m ON m.run_id=r.id AND m.role='assistant' WHERE r.id=?", i.run).Scan(&status, &reason, &answer, &config, &thinking); err != nil {
				return err
			}
			started = status != "queued"
			var snapshot runConfig
			if err := json.Unmarshal([]byte(config), &snapshot); err != nil {
				return err
			}
			output := snapshot.Agent.effectivePolicy().Output
			progress = output.Progress
			if terminal(status) {
				done = true
				if status == "completed" {
					text = answer
					if output.Sources {
						refs, readErr := s.store.db.QueryContext(ctx, "SELECT reference FROM run_sources WHERE run_id=? ORDER BY rowid LIMIT 8", i.run)
						if readErr != nil {
							return readErr
						}
						titles := []string{}
						for refs.Next() {
							var raw string
							if err := refs.Scan(&raw); err != nil {
								refs.Close()
								return err
							}
							var ref struct {
								Title    string `json:"title"`
								Version  int    `json:"version"`
								Location struct {
									Page      int `json:"page"`
									Paragraph int `json:"paragraph"`
								} `json:"location"`
							}
							if err := json.Unmarshal([]byte(raw), &ref); err != nil {
								refs.Close()
								return err
							}
							title := fmt.Sprintf("%s · v%d", ref.Title, ref.Version)
							if ref.Location.Page > 0 {
								title += fmt.Sprintf(" · 第%d页", ref.Location.Page)
							} else if ref.Location.Paragraph > 0 {
								title += fmt.Sprintf(" · 第%d段", ref.Location.Paragraph)
							}
							titles = append(titles, title)
						}
						readErr = refs.Err()
						refs.Close()
						if readErr != nil {
							return readErr
						}
						if len(titles) > 0 {
							text += "\n\n本轮检索来源：\n" + strings.Join(titles, "\n")
						}
					}

					if strings.TrimSpace(text) == "" {
						text = "任务已完成，但未产生文本回复。"
					}
				} else {
					text = "本次任务未完成：" + reason + "。请核对已有内容或联系管理员；不会自动重新执行。"
					if answer != "" {
						text += "\n已产生的内容：\n" + answer
					}
				}
				var err error
				text, err = s.channelRunOutput(i.run, text, thinking, snapshot)
				if err != nil {
					return err
				}
			}
		}
		if !done && !progress {
			if _, err := s.store.db.ExecContext(ctx, "UPDATE channel_inbox SET hinted=1 WHERE id=?", i.id); err != nil {
				return err
			}
			continue
		}
		// A waiting message gets one short notice, but a running task stays silent
		// until it either answers or clearly exceeds the long processing threshold.
		// This keeps the common case free of a generic "still processing" message
		// that arrives before or after visible streamed progress.
		if !done {
			threshold := queuedNoticeDelay
			if started {
				threshold = processingNoticeDelay
			}
			if i.hinted || now.Sub(time.UnixMilli(i.created)) < threshold {
				continue
			}
			// Streamed drafts already show progress; a generic notice would replace
			// newer text with older text. A draft that definitively failed shows
			// nothing, so the notice still applies.
			var drafted int
			if err := s.store.db.QueryRowContext(ctx, "SELECT count(*) FROM channel_outbox WHERE inbox_id=? AND kind=? AND state IN ('pending','sending','sent','retry')", i.id, channelKindDraft).Scan(&drafted); err != nil {
				return err
			}
			if drafted > 0 {
				if _, err := s.store.db.ExecContext(ctx, "UPDATE channel_inbox SET hinted=1 WHERE id=?", i.id); err != nil {
					return err
				}
				continue
			}
		}
		kind := channelKindHint
		if done {
			kind = channelKindFinal
		} else if started {
			text = "仍在处理中，请稍候；完成后会发送回答。"
		} else {
			text = "消息已收到，正在排队等待处理，请稍候。"
		}
		tx, err := s.store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := s.queueChannelText(tx, i.id, i.channel, i.member, kind, text, now.UnixMilli()); err != nil {
			tx.Rollback()
			return err
		}
		if done {
			_, err = tx.Exec("UPDATE channel_inbox SET finalized=1 WHERE id=?", i.id)
			if err == nil {
				// Unfinished stream messages are replaced by the final answer. Cancel
				// the ones still waiting so an older text cannot overwrite it.
				_, err = tx.Exec("UPDATE channel_outbox SET state='cancelled',error='最终回复已生成，取消尚未发送的中间消息。' WHERE inbox_id=? AND kind IN (?,?,?) AND state IN ('pending','retry')", i.id, channelKindAck, channelKindHint, channelKindDraft)
			}
		} else {
			_, err = tx.Exec("UPDATE channel_inbox SET hinted=1 WHERE id=?", i.id)
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
func (s *Server) ChannelTick(ctx context.Context, now time.Time) error {
	if s.updating.Load() {
		return nil
	}
	s.channelWork.Lock()
	defer s.channelWork.Unlock()
	if err := s.syncChannelConnections(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil
	}
	err := s.admitChannelInbox(ctx)
	if err == nil {
		err = s.prepareChannelReplies(ctx, now)
	}
	if err != nil {
		s.mu.Unlock()
		return err
	}
	_, err = s.store.db.ExecContext(ctx, "UPDATE channel_outbox AS o SET state='blocked',error='前一分段未确认成功，停止后续分段。' WHERE o.state IN ('pending','retry') AND EXISTS(SELECT 1 FROM channel_outbox p WHERE p.inbox_id=o.inbox_id AND p.kind=o.kind AND p.part<o.part AND p.state IN ('failed','unknown','blocked','cancelled'))")
	if err != nil {
		s.mu.Unlock()
		return err
	}
	var delivery, channel, member, text string
	var attempts int
	err = s.store.db.QueryRowContext(ctx, "SELECT o.id,o.channel_id,o.member,o.content,o.attempts FROM channel_outbox o JOIN channels c ON c.id=o.channel_id WHERE c.enabled=1 AND c.archived=0 AND NOT EXISTS(SELECT 1 FROM channel_inbox i JOIN conversation_cleanup d ON d.conversation_id=i.conversation_id WHERE i.id=o.inbox_id) AND o.state IN ('pending','retry') AND o.next_at<=? AND NOT EXISTS(SELECT 1 FROM channel_outbox p WHERE p.inbox_id=o.inbox_id AND p.kind=o.kind AND p.part<o.part AND p.state<>'sent') ORDER BY o.created,o.rowid LIMIT 1", now.UnixMilli()).Scan(&delivery, &channel, &member, &text, &attempts)
	if err == sql.ErrNoRows {
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		s.mu.Unlock()
		return err
	}
	c, _, err := s.loadChannel(ctx, channel)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	_, err = s.store.db.ExecContext(ctx, "UPDATE channel_outbox SET state='sending',attempts=attempts+1 WHERE id=?", delivery)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	var inbox, kind, replyRaw string
	var part int
	err = s.store.db.QueryRowContext(ctx, "SELECT o.inbox_id,o.kind,o.part,i.reply_context FROM channel_outbox o JOIN channel_inbox i ON i.id=o.inbox_id WHERE o.id=?", delivery).Scan(&inbox, &kind, &part, &replyRaw)
	reply := map[string]string{}
	if err == nil {
		err = json.Unmarshal([]byte(replyRaw), &reply)
	}
	s.channelConnections.Lock()
	entry, ok := s.channelConnections.values[channel]
	s.channelConnections.Unlock()
	result := channels.Result{State: "failed", Message: "渠道适配器不可用，未发送消息。"}
	if err != nil {
		result = channels.Result{State: "failed", Message: "回复路由信息无效，未发送消息。"}
	} else if ok && entry.version == c.Version {
		result = entry.connection.Send(sendCtx, channels.Delivery{ID: delivery, InboxID: inbox, Target: member, Text: text, Kind: kind, Part: part, ReplyContext: reply})
	}
	cancel()
	attempts++
	next := int64(0)
	if result.State == "retry" {
		if attempts >= 3 {
			result.State = "failed"
			result.Message += " 已达到自动重试上限。"
		} else {
			delay := 2 * time.Second
			if attempts == 2 {
				delay = 5 * time.Second
			}
			base := time.Now()
			if now.After(base) {
				base = now
			}
			next = base.Add(delay).UnixMilli()
		}
	}
	persist, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = s.store.db.ExecContext(persist, "UPDATE channel_outbox SET state=?,code=?,error=?,next_at=? WHERE id=?", result.State, result.Code, result.Message, next, delivery)
	return err
}
func (s *Server) RunChannels(ctx context.Context) error {
	defer s.closeChannelConnections()
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if err := s.ChannelTick(ctx, now); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}
