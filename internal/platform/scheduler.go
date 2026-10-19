package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"
)

type Limits struct {
	Concurrent        int `json:"concurrent"`
	ConversationQueue int `json:"conversation_queue"`
	GlobalQueue       int `json:"global_queue"`
	RunTimeout        int `json:"run_timeout_seconds"`
	ToolTimeout       int `json:"tool_timeout_seconds"`
	ToolRounds        int `json:"tool_rounds"`
	ContextBytes      int `json:"context_bytes,omitempty"` // Deprecated: accepted for compatibility and ignored.
}

func defaultLimits() Limits { return Limits{2, 10, 100, 300, 60, 10, 0} }
func (l Limits) valid() bool {
	return l.Concurrent >= 1 && l.Concurrent <= 8 && l.ConversationQueue >= 1 && l.ConversationQueue <= 1000 && l.GlobalQueue >= l.ConversationQueue && l.GlobalQueue <= 10000 && l.RunTimeout >= 1 && l.RunTimeout <= 3600 && l.ToolTimeout >= 1 && l.ToolTimeout <= 600 && l.ToolRounds >= 1 && l.ToolRounds <= 100
}
func (s *Server) loadLimits() (Limits, error) {
	l := defaultLimits()
	var data string
	err := s.store.db.QueryRow("SELECT config FROM settings WHERE id=1").Scan(&data)
	if err == sql.ErrNoRows {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if err = json.Unmarshal([]byte(data), &l); err != nil {
		return l, err
	}
	l.ContextBytes = 0 // Deprecated byte limits from older settings no longer restrict model context.
	if !l.valid() {
		return l, fmt.Errorf("invalid saved execution limits")
	}
	return l, nil
}
func (s *Server) getLimits(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	respond(w, 200, s.limits)
}
func (s *Server) saveLimits(w http.ResponseWriter, r *http.Request) {
	var l Limits
	if !decode(w, r, &l) {
		return
	}
	l.ContextBytes = 0 // Accept legacy clients, but model Token budgets are authoritative.
	if !l.valid() {
		fail(w, 400, "运行限制无效：并发 1–8，会话等待 1–1000，总等待不少于会话等待且不超过 10000；运行 1–3600 秒，工具 1–600 秒，工具轮数 1–100。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(l)
	if _, err := s.store.db.Exec("INSERT INTO settings VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config", string(data)); err != nil {
		internal(w)
		return
	}
	s.limits = l
	if err := s.dispatch(); err != nil {
		internal(w)
		return
	}
	respond(w, 200, l)
}

type TurnTime struct {
	ReceivedAt string `json:"received_at"`
	Timezone   string `json:"timezone"`
}

type runFallback struct {
	ModelOptions ModelOptions `json:"model_options"`
	ModelID      int64        `json:"model_id"`
	ModelVersion int          `json:"model_version"`
	Provider     string       `json:"provider"`
	BaseURL      string       `json:"base_url"`
	Model        string       `json:"model"`
}

type runConfig struct {
	Fallbacks    []runFallback `json:"fallbacks,omitempty"`
	TimeContext  *TurnTime     `json:"time_context,omitempty"`
	ModelOptions ModelOptions  `json:"model_options"`
	ModelID      int64         `json:"model_id,omitempty"`
	KnowledgeIDs []string      `json:"knowledge_ids,omitempty"`
	ModelVersion int           `json:"model_version"`
	Provider     string        `json:"provider"`
	BaseURL      string        `json:"base_url"`
	Model        string        `json:"model"`
	Agent        Agent         `json:"agent"`
	Limits       Limits        `json:"limits"`
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}

// dispatch runs under s.mu. A slot remains occupied until Runtime confirms all
// work and storage are closed, even when its terminal message is already saved.
func (s *Server) dispatch() error {
	if s.stopping || s.schedulerCtx.Err() != nil || s.restarting || !s.ready() {
		return nil
	}
	for {
		var count int
		if err := s.store.db.QueryRow("SELECT count(*) FROM runs WHERE released=0").Scan(&count); err != nil {
			return err
		}
		if count >= s.limits.Concurrent {
			return nil
		}
		var runID, conversationID, config, credential, input, messageAttachments string
		err := s.store.db.QueryRow(`SELECT r.id,r.conversation_id,r.config,r.credential,m.content,m.attachments FROM runs r JOIN messages m ON m.run_id=r.id AND m.role='user'
   WHERE r.status='queued' AND NOT EXISTS(SELECT 1 FROM runs active WHERE active.conversation_id=r.conversation_id AND active.released=0)
   ORDER BY r.created,r.rowid LIMIT 1`).Scan(&runID, &conversationID, &config, &credential, &input, &messageAttachments)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		var snapshot runConfig
		if err := json.Unmarshal([]byte(config), &snapshot); err != nil {
			return err
		}
		key, err := s.store.decrypt(credential)
		if err != nil {
			return err
		}
		credentials := RunCredentials{ModelKey: key}
		if snapshot.Capabilities != nil {
			if err := json.Unmarshal([]byte(key), &credentials); err != nil {
				return err
			}
			key = credentials.ModelKey
		}
		tx, err := s.store.db.Begin()
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE runs SET status='running',released=0,started=? WHERE id=?", time.Now().UnixMilli(), runID)
		if err == nil {
			err = recordEvent(tx, runID, 0, "dispatched", map[string]any{"model_version": snapshot.ModelVersion})
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			return err
		}
		modelRequest := map[string]any{"provider": snapshot.Provider, "base_url": snapshot.BaseURL, "name": snapshot.Model, "api_key": key, "options": snapshot.ModelOptions}
		if len(snapshot.Fallbacks) > 0 {
			fallbacks := make([]map[string]any, 0, len(snapshot.Fallbacks))
			for index, fallback := range snapshot.Fallbacks {
				fallbackKey := ""
				if index < len(credentials.FallbackKeys) {
					fallbackKey = credentials.FallbackKeys[index]
				}
				fallbacks = append(fallbacks, map[string]any{"provider": fallback.Provider, "base_url": fallback.BaseURL, "name": fallback.Model, "api_key": fallbackKey, "options": fallback.ModelOptions})
			}
			modelRequest["fallbacks"] = fallbacks
		}
		request := map[string]any{"v": 1, "type": "start", "request_id": runID, "run_id": runID, "conversation_id": conversationID, "input": input, "instructions": snapshot.Agent.Instructions, "model": modelRequest, "limits": map[string]int{"run_timeout_ms": snapshot.Limits.RunTimeout * 1000, "tool_timeout_ms": snapshot.Limits.ToolTimeout * 1000, "tool_rounds": snapshot.Limits.ToolRounds}}
		if snapshot.TimeContext != nil {
			request["time_context"] = snapshot.TimeContext
		}
		policy := snapshot.Agent.effectivePolicy()
		request["context_policy"] = policy.Context
		request["output_policy"] = policy.Output
		request["capabilities"] = snapshot.Capabilities
		request["mcp_secrets"] = credentials.MCPs
		request["agent_id"] = snapshot.Agent.ID
		advanced := snapshot.Agent.Policy.AdvancedTools && !s.advancedToolsDisabled
		request["advanced_tools"] = advanced
		if advanced {
			request["workspace"] = filepath.Join(s.store.directory, "workspaces", snapshot.Agent.ID)
		}
		if snapshot.KnowledgeIDs == nil {
			snapshot.KnowledgeIDs = []string{}
		}
		request["knowledge_ids"] = snapshot.KnowledgeIDs
		attachments, err := s.attachmentsForRun(runID)
		if err != nil {
			return err
		}
		runtimeAttachments, err := s.runtimeAttachments(attachments)
		if err != nil {
			return err
		}
		request["attachments"] = runtimeAttachments
		if err := s.runtime.Send(request); err != nil {
			// A partial pipe write can have admitted the request. Keep its slot until
			// status/exit reconciliation, and do not replay it.
			if _, err := s.store.db.Exec("UPDATE runs SET status='interrupted',error='无法确认任务是否已接收，请核对后重新发起。',credential='',cancel_started=? WHERE id=?", time.Now().UnixMilli(), runID); err != nil {
				return err
			}
			return s.queryStatus(runID)
		}
	}
}
func recordEvent(tx *sql.Tx, runID string, seq int64, kind string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO run_events(run_id,seq,kind,data,created) VALUES(?,?,?,?,?)", runID, seq, kind, string(encoded), time.Now().UnixMilli())
	return err
}
func (s *Server) queryStatus(runID string) error {
	// Failure to write a query changes no task outcome; the watchdog will require
	// a process exit if the peer cannot confirm release.
	_ = s.runtime.Send(map[string]any{"v": 1, "type": "status", "request_id": "status-" + runID, "run_id": runID})
	return nil
}
func (s *Server) interruptAll(message string, release bool) error {
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT id FROM runs WHERE status IN ('queued','running','cancelling') OR released=0")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := recordEvent(tx, id, 0, "interrupted", map[string]string{"reason": message}); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE tool_calls SET status='interrupted' WHERE run_id=? AND status='running'", id); err != nil {
			return err
		}
	}
	_, err = tx.Exec("UPDATE runs SET status=CASE WHEN status IN ('queued','running','cancelling') THEN 'interrupted' ELSE status END,error=CASE WHEN status IN ('queued','running','cancelling') THEN ? ELSE error END,credential='',released=CASE WHEN ? OR status='queued' THEN 1 ELSE released END WHERE status IN ('queued','running','cancelling') OR released=0", message, release)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Tick enforces timeouts and reconciles any lost acknowledgement. Only the
// platform scheduler calls it; tests can supply time without wall-clock sleeps.
func (s *Server) Tick(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil
	}
	if err := s.dispatch(); err != nil {
		return err
	}
	rows, err := s.store.db.Query("SELECT id,status,config,started,cancel_started FROM runs WHERE released=0")
	if err != nil {
		return err
	}
	type pending struct {
		id, status, config string
		started, cancel    int64
	}
	work := []pending{}
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.status, &p.config, &p.started, &p.cancel); err != nil {
			rows.Close()
			return err
		}
		work = append(work, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range work {
		var config runConfig
		if err := json.Unmarshal([]byte(p.config), &config); err != nil {
			return err
		}
		if p.status == "running" && now.UnixMilli()-p.started >= int64(config.Limits.RunTimeout)*1000 {
			tx, err := s.store.db.Begin()
			if err != nil {
				return err
			}
			_, err = tx.Exec("UPDATE runs SET status='cancelling',cancel_started=?,error='运行已超时，正在确认停止。' WHERE id=?", now.UnixMilli(), p.id)
			if err == nil {
				err = recordEvent(tx, p.id, 0, "timeout_requested", map[string]any{})
			}
			if err == nil {
				err = tx.Commit()
			} else {
				tx.Rollback()
			}
			if err != nil {
				return err
			}
			_ = s.runtime.Send(map[string]any{"v": 1, "type": "cancel", "request_id": "cancel-" + p.id, "run_id": p.id, "reason": "timeout"})
		}
		if p.cancel > 0 && now.UnixMilli()-p.cancel >= 10000 && !s.restarting {
			s.restarting = true
			if err := s.interruptAll("未能确认当前操作停止，正在重启对话进程；操作结果不明，请核对外部系统。", false); err != nil {
				return err
			}
			if peer, ok := s.runtime.(interface{ Restart() error }); ok {
				if err := peer.Restart(); err != nil {
					return err
				}
			}
			return nil
		}
		if now.Sub(s.lastStatus) >= 5*time.Second {
			if err := s.queryStatus(p.id); err != nil {
				return err
			}
		}
	}
	if now.Sub(s.lastStatus) >= 5*time.Second {
		s.lastStatus = now
	}
	return nil
}
func (s *Server) RunScheduler(ctx context.Context) error {
	s.mu.Lock()
	s.schedulerCtx = ctx
	s.mu.Unlock()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.stopping = true
			s.mu.Unlock()
			return nil
		case now := <-ticker.C:
			if err := s.Tick(now); err != nil {
				return err
			}
		}
	}
}
