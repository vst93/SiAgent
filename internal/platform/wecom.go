package platform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"siagent/internal/wecom"
)

const wecomSchema = `
 ALTER TABLE conversations ADD COLUMN channel_id TEXT NOT NULL DEFAULT '';
 CREATE TABLE wecom_channels(id TEXT PRIMARY KEY,corp_id TEXT NOT NULL,app_id INTEGER NOT NULL,agent_id TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 0,archived INTEGER NOT NULL DEFAULT 0,secrets TEXT NOT NULL,version INTEGER NOT NULL,verified INTEGER NOT NULL DEFAULT 0,UNIQUE(corp_id,app_id));
 CREATE TABLE wecom_sessions(channel_id TEXT NOT NULL REFERENCES wecom_channels(id),member TEXT NOT NULL,agent_id TEXT NOT NULL,conversation_id TEXT NOT NULL REFERENCES conversations(id),PRIMARY KEY(channel_id,member,agent_id));
 CREATE TABLE wecom_inbox(id TEXT PRIMARY KEY,channel_id TEXT NOT NULL REFERENCES wecom_channels(id),msg_id TEXT NOT NULL,member TEXT NOT NULL,agent_id TEXT NOT NULL,content TEXT NOT NULL,fingerprint TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',error TEXT NOT NULL DEFAULT '',run_id TEXT NOT NULL DEFAULT '',conversation_id TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,finalized INTEGER NOT NULL DEFAULT 0,hinted INTEGER NOT NULL DEFAULT 0,UNIQUE(channel_id,msg_id));
 CREATE TABLE wecom_outbox(id TEXT PRIMARY KEY,inbox_id TEXT NOT NULL REFERENCES wecom_inbox(id),channel_id TEXT NOT NULL REFERENCES wecom_channels(id),member TEXT NOT NULL,kind TEXT NOT NULL,part INTEGER NOT NULL,total INTEGER NOT NULL,content TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,code INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,UNIQUE(inbox_id,kind,part));
 CREATE INDEX wecom_pending_inbox ON wecom_inbox(state,created);
 CREATE INDEX wecom_pending_outbox ON wecom_outbox(state,next_at,created);`

type wecomChannel struct {
	ID           string `json:"id"`
	CorpID       string `json:"corp_id"`
	AppID        int64  `json:"app_id"`
	AgentID      string `json:"agent_id"`
	Enabled      bool   `json:"enabled"`
	Archived     bool   `json:"archived"`
	Version      int    `json:"version"`
	Verified     bool   `json:"verified"`
	HasSecrets   bool   `json:"has_secrets"`
	CallbackPath string `json:"callback_path"`
}

func (s *Server) channel(id string) (wecomChannel, wecom.Credentials, error) {
	return s.channelContext(context.Background(), id)
}
func (s *Server) channelContext(ctx context.Context, id string) (wecomChannel, wecom.Credentials, error) {
	var c wecomChannel
	var encrypted string
	err := s.store.db.QueryRowContext(ctx, "SELECT id,corp_id,app_id,agent_id,enabled,archived,version,verified,secrets FROM wecom_channels WHERE id=?", id).Scan(&c.ID, &c.CorpID, &c.AppID, &c.AgentID, &c.Enabled, &c.Archived, &c.Version, &c.Verified, &encrypted)
	var secret wecom.Credentials
	if err != nil {
		return c, secret, err
	}
	raw, err := s.store.decrypt(encrypted)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &secret)
	}
	c.HasSecrets = true
	c.CallbackPath = "/callbacks/wecom/" + c.ID
	return c, secret, err
}
func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id,corp_id,app_id,agent_id,enabled,archived,version,verified FROM wecom_channels ORDER BY rowid")
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	values := []wecomChannel{}
	for rows.Next() {
		var c wecomChannel
		if err := rows.Scan(&c.ID, &c.CorpID, &c.AppID, &c.AgentID, &c.Enabled, &c.Archived, &c.Version, &c.Verified); err != nil {
			internal(w)
			return
		}
		c.HasSecrets = true
		c.CallbackPath = "/callbacks/wecom/" + c.ID
		values = append(values, c)
	}
	if rows.Err() != nil {
		internal(w)
		return
	}
	respond(w, 200, values)
}
func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CorpID  string `json:"corp_id"`
		AppID   int64  `json:"app_id"`
		AgentID string `json:"agent_id"`
		Enabled bool   `json:"enabled"`
		Secret  string `json:"secret"`
		Token   string `json:"token"`
		AESKey  string `json:"aes_key"`
		Confirm bool   `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	s.channelConfigMu.Lock()
	defer s.channelConfigMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	channelID := r.PathValue("channel")
	version := 1
	secret := wecom.Credentials{}
	verified := false
	if channelID != "" {
		old, saved, err := s.channel(channelID)
		if err == sql.ErrNoRows {
			fail(w, 404, "渠道不存在。")
			return
		}
		if err != nil {
			internal(w)
			return
		}
		if old.Archived {
			fail(w, 409, "渠道已归档。")
			return
		}
		if old.CorpID != input.CorpID || old.AppID != input.AppID {
			fail(w, 409, "企业和应用标识保存后固定，其他应用请新增渠道。")
			return
		}
		if old.Enabled && old.AgentID != input.AgentID {
			fail(w, 409, "请先停用渠道，再修改助手绑定。")
			return
		}
		secret = saved
		version = old.Version + 1
		verified = old.Verified
	} else {
		channelID = id()
	}
	if input.Secret != "" {
		secret.Secret = input.Secret
	}
	if input.Token != "" {
		secret.Token = input.Token
		verified = false
	}
	if input.AESKey != "" {
		secret.AESKey = input.AESKey
		verified = false
	}
	if len(input.CorpID) < 1 || len(input.CorpID) > 128 || strings.ContainsAny(input.CorpID, "\x00\r\n ") || input.AppID < 1 || input.AppID > 2147483647 || len(secret.Secret) < 1 || len(secret.Secret) > 8192 || strings.ContainsAny(secret.Secret, "\x00\r\n") {
		fail(w, 400, "请填写有效的 CorpID、应用 AgentID、Secret、回调 Token 和 EncodingAESKey。")
		return
	}
	if _, err := wecom.NewCryptor(secret.Token, secret.AESKey, input.CorpID); err != nil {
		fail(w, 400, "回调 Token 或 EncodingAESKey 无效，AES Key 须为 43 位 Base64 字符。")
		return
	}
	if input.AgentID != "" {
		a, err := s.agentByID(input.AgentID)
		if err == sql.ErrNoRows || a.Archived {
			fail(w, 400, "请选择未归档的助手。")
			return
		}
		if err != nil {
			internal(w)
			return
		}
		if input.Enabled && !a.Enabled {
			fail(w, 409, "绑定助手已停用，请先启用助手。")
			return
		}
	}
	if input.Enabled && (input.AgentID == "" || !input.Confirm) {
		fail(w, 400, "启用前请选择助手，并确认成员消息及检索内容会发送至该助手配置的模型服务。")
		return
	}
	var duplicate int
	if err := s.store.db.QueryRow("SELECT count(*) FROM wecom_channels WHERE corp_id=? AND app_id=? AND id<>?", input.CorpID, input.AppID, channelID).Scan(&duplicate); err != nil {
		internal(w)
		return
	}
	if duplicate > 0 {
		fail(w, 409, "同一企业应用已有渠道，请编辑既有配置。")
		return
	}
	raw, _ := json.Marshal(secret)
	_, err := s.store.db.Exec("INSERT INTO wecom_channels(id,corp_id,app_id,agent_id,enabled,secrets,version,verified) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET agent_id=excluded.agent_id,enabled=excluded.enabled,secrets=excluded.secrets,version=excluded.version,verified=excluded.verified", channelID, input.CorpID, input.AppID, input.AgentID, input.Enabled, s.store.encrypt(string(raw)), version, verified)
	if err != nil {
		internal(w)
		return
	}
	c, _, err := s.channel(channelID)
	if err != nil {
		internal(w)
		return
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	respond(w, status, c)
}
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	c, secret, err := s.channel(r.PathValue("channel"))
	if err != nil || c.Archived {
		fail(w, 404, "渠道不存在或已归档。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	result := s.wecomClient.Test(ctx, wecom.Application{ID: c.ID, CorpID: c.CorpID, AppID: c.AppID, Secret: secret.Secret})
	if result.State != "sent" {
		fail(w, 502, result.Message)
		return
	}
	respond(w, 200, map[string]string{"message": result.Message})
}
func (s *Server) archiveChannel(w http.ResponseWriter, r *http.Request) {
	s.channelConfigMu.Lock()
	defer s.channelConfigMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _, err := s.channel(r.PathValue("channel"))
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
	if err := tx.QueryRow("SELECT count(*) FROM wecom_outbox WHERE channel_id=? AND state='sending'", c.ID).Scan(&sending); err != nil {
		internal(w)
		return
	}
	if sending > 0 {
		fail(w, 409, "仍有发送请求在途，请稍后再归档。")
		return
	}
	if _, err := tx.Exec("UPDATE wecom_channels SET archived=1,agent_id='',enabled=0 WHERE id=?", c.ID); err != nil {
		internal(w)
		return
	}
	if _, err := tx.Exec("UPDATE wecom_outbox SET state='cancelled',error='渠道已归档，停止发送。' WHERE channel_id=? AND state IN ('pending','retry')", c.ID); err != nil {
		internal(w)
		return
	}
	if _, err := tx.Exec("UPDATE wecom_inbox SET finalized=1,state=CASE WHEN state='pending' THEN 'rejected' ELSE state END,error=CASE WHEN state='pending' THEN '渠道已归档。' ELSE error END WHERE channel_id=?", c.ID); err != nil {
		internal(w)
		return
	}
	if err := tx.Commit(); err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]bool{"archived": true})
}

// CallbackHandler is the sole public channel endpoint; it uses signature/AES authentication, not administrator cookies.
func (s *Server) CallbackHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callbacks/wecom/{channel}", s.wecomCallback)
	mux.HandleFunc("POST /callbacks/wecom/{channel}", s.wecomCallback)
	return mux
}
func (s *Server) wecomCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	c, secret, err := s.channelContext(ctx, r.PathValue("channel"))
	if err != nil || c.Archived {
		http.Error(w, "not found", 404)
		return
	}
	q := r.URL.Query()
	timestamp, nonce, signature := q.Get("timestamp"), q.Get("nonce"), q.Get("msg_signature")
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || ts < time.Now().Add(-5*time.Minute).Unix() || ts > time.Now().Add(5*time.Minute).Unix() || len(nonce) < 1 || len(nonce) > 128 {
		http.Error(w, "invalid callback", 403)
		return
	}
	cryptor, err := wecom.NewCryptor(secret.Token, secret.AESKey, c.CorpID)
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	encrypted := q.Get("echostr")
	if r.Method == "POST" {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
		var envelope wecom.Envelope
		if err != nil || wecom.XML(data, &envelope) != nil {
			http.Error(w, "invalid callback", 400)
			return
		}
		encrypted = envelope.Encrypted
	}
	plain, err := cryptor.Decrypt(signature, timestamp, nonce, encrypted)
	if err != nil {
		http.Error(w, "invalid callback", 403)
		return
	}
	if r.Method == "GET" {
		result, err := s.store.db.ExecContext(ctx, "UPDATE wecom_channels SET verified=1 WHERE id=? AND version=? AND archived=0", c.ID, c.Version)
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		if count, _ := result.RowsAffected(); count != 1 {
			http.Error(w, "configuration changed", 503)
			return
		}
		w.Write(plain)
		return
	}
	var message wecom.Message
	if wecom.XML(plain, &message) != nil || message.CorpID != c.CorpID || message.AppID != c.AppID {
		http.Error(w, "invalid callback", 403)
		return
	}
	if message.Type == "event" || message.ChatID != "" {
		io.WriteString(w, "success")
		return
	}
	if !wecom.ValidMember(message.Member) || len(message.ID) < 1 || len(message.ID) > 128 || strings.Trim(message.ID, "0123456789") != "" || !utf8.ValidString(message.Content) || len(message.Content) > 100000 {
		http.Error(w, "invalid callback", 400)
		return
	}
	original, _ := json.Marshal(message)
	digest := sha256.Sum256(original)
	fingerprint := hex.EncodeToString(digest[:])
	s.channelConfigMu.Lock()
	defer s.channelConfigMu.Unlock()
	var saved string
	err = s.store.db.QueryRowContext(ctx, "SELECT fingerprint FROM wecom_inbox WHERE channel_id=? AND msg_id=?", c.ID, message.ID).Scan(&saved)
	if err == nil {
		if saved != fingerprint {
			http.Error(w, "conflicting callback", 403)
			return
		}
		io.WriteString(w, "success")
		return
	}
	if err != sql.ErrNoRows {
		http.Error(w, "unavailable", 503)
		return
	}
	// Re-read under the same admission lock used by configuration edits.
	current, _, err := s.channelContext(ctx, c.ID)
	if err != nil || current.Version != c.Version || !current.Enabled || current.Archived {
		http.Error(w, "channel unavailable", 503)
		return
	}
	var waiting int
	if err := s.store.db.QueryRowContext(ctx, "SELECT count(*) FROM wecom_inbox WHERE channel_id=? AND state='pending'", c.ID).Scan(&waiting); err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	if waiting >= 1000 {
		http.Error(w, "channel queue full", 503)
		return
	}
	content := strings.TrimSpace(message.Content)
	state, reason := "pending", ""
	if message.Type != "text" {
		state = "rejected"
		reason = "目前仅支持文本私聊，请发送文字问题。"
	} else if content == "" {
		state = "rejected"
		reason = "未收到可处理的文本，请重新发送问题。"
	}
	_, err = s.store.db.ExecContext(ctx, "INSERT INTO wecom_inbox(id,channel_id,msg_id,member,agent_id,content,fingerprint,state,error,created) VALUES(?,?,?,?,?,?,?,?,?,?)", id(), c.ID, message.ID, message.Member, current.AgentID, content, fingerprint, state, reason, time.Now().UnixMilli())
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	io.WriteString(w, "success")
}
func (s *Server) channelDiagnostics(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id,msg_id,member,agent_id,state,error,run_id,conversation_id,created,substr(content,1,200) FROM wecom_inbox WHERE channel_id=? ORDER BY created DESC,rowid DESC LIMIT 50", r.PathValue("channel"))
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
	rows, err = s.store.db.Query("SELECT id,inbox_id,member,kind,part,total,state,attempts,code,error,created FROM wecom_outbox WHERE channel_id=? ORDER BY created DESC,rowid DESC LIMIT 100", r.PathValue("channel"))
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
	respond(w, 200, map[string]any{"inbox": inbox, "outbox": outbox})
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
	if err := tx.QueryRow("SELECT o.inbox_id,o.kind,o.part FROM wecom_outbox o JOIN wecom_channels c ON c.id=o.channel_id WHERE o.id=? AND o.state='failed' AND c.archived=0 AND NOT EXISTS(SELECT 1 FROM wecom_inbox i JOIN conversation_cleanup d ON d.conversation_id=i.conversation_id WHERE i.id=o.inbox_id)", r.PathValue("delivery")).Scan(&inbox, &kind, &part); err != nil {
		fail(w, 409, "仅明确失败的发送可重试；结果不明须人工核对，不自动重发。")
		return
	}
	if _, err := tx.Exec("UPDATE wecom_outbox SET state='pending',attempts=0,next_at=0,error='' WHERE id=?", r.PathValue("delivery")); err != nil {
		internal(w)
		return
	}
	if _, err := tx.Exec("UPDATE wecom_outbox SET state='pending',error='' WHERE inbox_id=? AND kind=? AND part>? AND state='blocked'", inbox, kind, part); err != nil {
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
	_, err := s.store.db.Exec("UPDATE wecom_outbox SET state='unknown',error='服务在发送确认前退出，请人工核对企微，不自动重发。' WHERE state='sending'; UPDATE wecom_inbox SET state='rejected',error='服务已重启，该消息尚未开始处理，请重新发送。' WHERE state='pending';")
	return err
}
func channelApplication(c wecomChannel, secret wecom.Credentials) wecom.Application {
	return wecom.Application{ID: c.ID, CorpID: c.CorpID, AppID: c.AppID, Secret: secret.Secret}
}
func (s *Server) channelConversation(ctx context.Context, channel, member, agent string, reset bool) (string, error) {
	var conversation string
	err := s.store.db.QueryRowContext(ctx, "SELECT conversation_id FROM wecom_sessions WHERE channel_id=? AND member=? AND agent_id=?", channel, member, agent).Scan(&conversation)
	if err == nil && !reset {
		return conversation, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	a, err := s.agentByID(agent)
	if err != nil {
		return "", err
	}
	if !a.Enabled || a.Archived {
		return "", &admissionError{409, "助手已停用或归档，请联系管理员。"}
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	conversation = id()
	if _, err := tx.ExecContext(ctx, "INSERT INTO conversations(id,title,created,agent_id,channel_id) VALUES(?,'新对话',?,?,?)", conversation, time.Now().UnixMilli(), agent, channel); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO wecom_sessions VALUES(?,?,?,?) ON CONFLICT(channel_id,member,agent_id) DO UPDATE SET conversation_id=excluded.conversation_id", channel, member, agent, conversation); err != nil {
		return "", err
	}
	return conversation, tx.Commit()
}
func (s *Server) admitChannelInbox(ctx context.Context) error {
	for range 4 {
		var inbox, channel, member, agent, text string
		err := s.store.db.QueryRowContext(ctx, "SELECT i.id,i.channel_id,i.member,i.agent_id,i.content FROM wecom_inbox i JOIN wecom_channels c ON c.id=i.channel_id WHERE i.state='pending' AND c.enabled=1 AND c.archived=0 ORDER BY i.created,i.rowid LIMIT 1").Scan(&inbox, &channel, &member, &agent, &text)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		conversation, err := s.channelConversation(ctx, channel, member, agent, text == "/new")
		if err == nil && text == "/new" {
			_, err = s.store.db.ExecContext(ctx, "UPDATE wecom_inbox SET state='rejected',error='已开启新的对话，请发送问题。',conversation_id=? WHERE id=?", conversation, inbox)
			if err != nil {
				return err
			}
			continue
		}
		if err == nil {
			_, err = s.enqueueRun(ctx, conversation, text, func(tx *sql.Tx, run string) error {
				_, err := tx.ExecContext(ctx, "UPDATE wecom_inbox SET state='admitted',run_id=?,conversation_id=? WHERE id=? AND state='pending'", run, conversation, inbox)
				return err
			})
		}
		if err != nil {
			var rejected *admissionError
			if !errors.As(err, &rejected) {
				return err
			}
			if rejected.Status == 503 {
				return nil
			}
			if _, err := s.store.db.ExecContext(ctx, "UPDATE wecom_inbox SET state='rejected',error=? WHERE id=? AND state='pending'", rejected.Message, inbox); err != nil {
				return err
			}
		}
	}
	return nil
}
func queueChannelText(tx *sql.Tx, inbox, channel, member, kind, text string, now int64) error {
	parts := wecom.Split(text)
	for index, part := range parts {
		if len(parts) > 1 {
			part = fmt.Sprintf("（%d/%d）\n%s", index+1, len(parts), part)
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO wecom_outbox(id,inbox_id,channel_id,member,kind,part,total,content,created) VALUES(?,?,?,?,?,?,?,?,?)", id(), inbox, channel, member, kind, index, len(parts), part, now); err != nil {
			return err
		}
	}
	return nil
}
func (s *Server) prepareChannelReplies(ctx context.Context, now time.Time) error {
	rows, err := s.store.db.QueryContext(ctx, "SELECT i.id,i.channel_id,i.member,i.state,i.error,i.run_id,i.created,i.hinted FROM wecom_inbox i JOIN wecom_channels c ON c.id=i.channel_id WHERE i.finalized=0 AND c.archived=0 AND (i.hinted=0 OR i.state='rejected' OR EXISTS(SELECT 1 FROM runs r WHERE r.id=i.run_id AND r.status NOT IN ('queued','running','cancelling'))) ORDER BY i.created,i.rowid LIMIT 200")
	if err != nil {
		return err
	}
	type item struct {
		id, channel, member, state, reason, run string
		created                                 int64
		hinted                                  bool
	}
	items := []item{}
	for rows.Next() {
		var i item
		if err := rows.Scan(&i.id, &i.channel, &i.member, &i.state, &i.reason, &i.run, &i.created, &i.hinted); err != nil {
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
		if i.run != "" {
			var status, reason, answer string
			if err := s.store.db.QueryRowContext(ctx, "SELECT r.status,r.error,m.content FROM runs r JOIN messages m ON m.run_id=r.id AND m.role='assistant' WHERE r.id=?", i.run).Scan(&status, &reason, &answer); err != nil {
				return err
			}
			if terminal(status) {
				done = true
				if status == "completed" {
					text = answer
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

					if strings.TrimSpace(text) == "" {
						text = "任务已完成，但未产生文本回复。"
					}
				} else {
					text = "本次任务未完成：" + reason + "。请核对已有内容或联系管理员；不会自动重新执行。"
					if answer != "" {
						text += "\n已产生的内容：\n" + answer
					}
				}
			}
		}
		if !done && (i.hinted || now.UnixMilli()-i.created < 10000) {
			continue
		}
		kind := "hint"
		if done {
			kind = "final"
		} else {
			text = "消息已收到，仍在排队或处理中，请稍候。"
		}
		tx, err := s.store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := queueChannelText(tx, i.id, i.channel, i.member, kind, text, now.UnixMilli()); err != nil {
			tx.Rollback()
			return err
		}
		if done {
			_, err = tx.Exec("UPDATE wecom_inbox SET finalized=1 WHERE id=?", i.id)
			if err == nil {
				_, err = tx.Exec("UPDATE wecom_outbox SET state='cancelled',error='最终回复已生成，取消尚未发送的处理中提示。' WHERE inbox_id=? AND kind='hint' AND state IN ('pending','retry')", i.id)
			}
		} else {
			_, err = tx.Exec("UPDATE wecom_inbox SET hinted=1 WHERE id=?", i.id)
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
	s.channelWork.Lock()
	defer s.channelWork.Unlock()
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
	_, err = s.store.db.ExecContext(ctx, "UPDATE wecom_outbox AS o SET state='blocked',error='前一分段未确认成功，停止后续分段。' WHERE o.state IN ('pending','retry') AND EXISTS(SELECT 1 FROM wecom_outbox p WHERE p.inbox_id=o.inbox_id AND p.kind=o.kind AND p.part<o.part AND p.state IN ('failed','unknown','blocked','cancelled'))")
	if err != nil {
		s.mu.Unlock()
		return err
	}
	var delivery, channel, member, text string
	var attempts int
	err = s.store.db.QueryRowContext(ctx, "SELECT o.id,o.channel_id,o.member,o.content,o.attempts FROM wecom_outbox o JOIN wecom_channels c ON c.id=o.channel_id WHERE c.enabled=1 AND c.archived=0 AND NOT EXISTS(SELECT 1 FROM wecom_inbox i JOIN conversation_cleanup d ON d.conversation_id=i.conversation_id WHERE i.id=o.inbox_id) AND o.state IN ('pending','retry') AND o.next_at<=? AND NOT EXISTS(SELECT 1 FROM wecom_outbox p WHERE p.inbox_id=o.inbox_id AND p.kind=o.kind AND p.part<o.part AND p.state<>'sent') ORDER BY o.created,o.rowid LIMIT 1", now.UnixMilli()).Scan(&delivery, &channel, &member, &text, &attempts)
	if err == sql.ErrNoRows {
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		s.mu.Unlock()
		return err
	}
	c, secret, err := s.channel(channel)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	_, err = s.store.db.ExecContext(ctx, "UPDATE wecom_outbox SET state='sending',attempts=attempts+1 WHERE id=?", delivery)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	result := s.wecomClient.Send(sendCtx, channelApplication(c, secret), member, text)
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
	_, err = s.store.db.ExecContext(persist, "UPDATE wecom_outbox SET state=?,code=?,error=?,next_at=? WHERE id=?", result.State, result.Code, result.Message, next, delivery)
	return err
}
func (s *Server) RunChannels(ctx context.Context) error {
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
