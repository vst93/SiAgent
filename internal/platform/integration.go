package platform

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"siagent/internal/knowledge"
)

// IntegrationScope values are granted per key. A key with an empty resource
// field can act on any Agent or knowledge base; a scoped key cannot.
const (
	scopeAgentRun       = "agent:run"
	scopeAgentRead      = "agent:read"
	scopeKnowledgeWrite = "knowledge:write"
	scopeKnowledgeRead  = "knowledge:search"
	scopeWebhook        = "webhook:manage"
)

var allScopes = map[string]bool{scopeAgentRun: true, scopeAgentRead: true, scopeKnowledgeWrite: true, scopeKnowledgeRead: true, scopeWebhook: true}

type principal struct {
	ID          string
	AgentID     string
	KnowledgeID string
	Scopes      map[string]bool
	RateLimit   int
}

type principalKey struct{}

func principalOf(r *http.Request) (principal, bool) {
	value, ok := r.Context().Value(principalKey{}).(principal)
	return value, ok
}

// apiKeyHash stores only a SHA-256 of the secret; the plaintext is shown once.
func apiKeyHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newAPIToken() (string, string) {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	token := "siagent_" + base64.RawURLEncoding.EncodeToString(raw[:])
	prefix := token[:len("siagent_")+6]
	return token, prefix
}

type apiKey struct {
	ID          string   `json:"id"`
	Prefix      string   `json:"prefix"`
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	AgentID     string   `json:"agent_id"`
	KnowledgeID string   `json:"knowledge_id"`
	RateLimit   int      `json:"rate_limit"`
	Expires     int64    `json:"expires"`
	Revoked     bool     `json:"revoked"`
	Created     int64    `json:"created"`
	LastUsed    int64    `json:"last_used"`
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string   `json:"name"`
		Scopes      []string `json:"scopes"`
		AgentID     string   `json:"agent_id"`
		KnowledgeID string   `json:"knowledge_id"`
		RateLimit   int      `json:"rate_limit"`
		ExpiresDays int      `json:"expires_days"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 100 {
		fail(w, 400, "请填写密钥名称（最多 100 字节）。")
		return
	}
	if len(input.Scopes) == 0 || len(input.Scopes) > 8 {
		fail(w, 400, "请选择 1–8 个作用域。")
		return
	}
	scopes := map[string]bool{}
	for _, scope := range input.Scopes {
		if !allScopes[scope] {
			fail(w, 400, "作用域无效。")
			return
		}
		scopes[scope] = true
	}
	if input.RateLimit == 0 {
		input.RateLimit = 60
	}
	if input.RateLimit < 1 || input.RateLimit > 6000 {
		fail(w, 400, "每分钟请求上限须为 1–6000。")
		return
	}
	if input.ExpiresDays < 0 || input.ExpiresDays > 3650 {
		fail(w, 400, "有效期须为 0–3650 天，0 表示不过期。")
		return
	}
	encoded, _ := json.Marshal(input.Scopes)
	token, prefix := newAPIToken()
	id := id()
	expires := int64(0)
	if input.ExpiresDays > 0 {
		expires = time.Now().Add(time.Duration(input.ExpiresDays) * 24 * time.Hour).Unix()
	}
	if _, err := s.store.db.Exec("INSERT INTO api_keys(id,prefix,hash,name,scopes,agent_id,knowledge_id,rate_limit,expires,created) VALUES(?,?,?,?,?,?,?,?,?,?)",
		id, prefix, apiKeyHash(token), input.Name, string(encoded), input.AgentID, input.KnowledgeID, input.RateLimit, expires, time.Now().UnixMilli()); err != nil {
		internal(w)
		return
	}
	s.audit("api_key.create", id, strings.Join(input.Scopes, ","))
	respond(w, 201, map[string]any{"id": id, "name": input.Name, "prefix": prefix, "key": token, "scopes": input.Scopes, "agent_id": input.AgentID, "knowledge_id": input.KnowledgeID, "rate_limit": input.RateLimit, "expires": expires})
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id,prefix,name,scopes,agent_id,knowledge_id,rate_limit,expires,revoked,created,last_used FROM api_keys ORDER BY created DESC")
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	values := []apiKey{}
	for rows.Next() {
		var value apiKey
		var scopes string
		var revoked int
		if err := rows.Scan(&value.ID, &value.Prefix, &value.Name, &scopes, &value.AgentID, &value.KnowledgeID, &value.RateLimit, &value.Expires, &revoked, &value.Created, &value.LastUsed); err != nil {
			internal(w)
			return
		}
		_ = json.Unmarshal([]byte(scopes), &value.Scopes)
		value.Revoked = revoked == 1
		values = append(values, value)
	}
	respond(w, 200, values)
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.db.Exec("UPDATE api_keys SET revoked=1 WHERE id=?", r.PathValue("id"))
	if err != nil {
		internal(w)
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		fail(w, 404, "密钥不存在。")
		return
	}
	s.audit("api_key.revoke", r.PathValue("id"), "")
	respond(w, 200, map[string]bool{"revoked": true})
}

type rateWindow struct {
	mu     sync.Mutex
	window int64
	count  int
}

// allow applies a per-key fixed window limit. It is intentionally in-memory:
// limits reset on restart, which is acceptable for abuse protection.
func (s *Server) allow(keyID string, limit int) bool {
	now := time.Now().Unix() / 60
	s.apiRateMu.Lock()
	if s.apiRate == nil {
		s.apiRate = map[string]*rateWindow{}
	}
	window, ok := s.apiRate[keyID]
	if !ok {
		window = &rateWindow{}
		s.apiRate[keyID] = window
	}
	s.apiRateMu.Unlock()
	window.mu.Lock()
	defer window.mu.Unlock()
	if window.window != now {
		window.window = now
		window.count = 0
	}
	window.count++
	return window.count <= limit
}

// integration wraps a handler with Bearer authentication and scope checks.
func (s *Server) integration(scope string, next func(http.ResponseWriter, *http.Request, principal)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			fail(w, 401, "缺少集成 API 密钥。")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		var value principal
		var scopes string
		var expires int64
		var revoked int
		var limit int
		err := s.store.db.QueryRow("SELECT id,scopes,agent_id,knowledge_id,rate_limit,expires,revoked FROM api_keys WHERE hash=?", apiKeyHash(token)).Scan(&value.ID, &scopes, &value.AgentID, &value.KnowledgeID, &limit, &expires, &revoked)
		if err == sql.ErrNoRows {
			fail(w, 401, "集成 API 密钥无效。")
			return
		}
		if err != nil {
			internal(w)
			return
		}
		if revoked == 1 || (expires > 0 && time.Now().Unix() > expires) {
			fail(w, 401, "集成 API 密钥已吊销或过期。")
			return
		}
		value.Scopes = map[string]bool{}
		for _, item := range strings.Split(scopes, ",") {
			item = strings.Trim(strings.TrimSpace(item), `"[]`)
			if item != "" {
				value.Scopes[item] = true
			}
		}
		if !value.Scopes[scope] {
			fail(w, 403, "密钥缺少所需作用域。")
			return
		}
		value.RateLimit = limit
		if !s.allow(value.ID, limit) {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, "请求过于频繁，请稍后重试。")
			return
		}
		_, _ = s.store.db.Exec("UPDATE api_keys SET last_used=? WHERE id=?", time.Now().UnixMilli(), value.ID)
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, value)), value)
	})
}

// authorizeAgent enforces the key's Agent scope.
func (p principal) authorizeAgent(agentID string) bool {
	return p.AgentID == "" || p.AgentID == agentID
}

func (p principal) authorizeKnowledge(id string) bool {
	return p.KnowledgeID == "" || p.KnowledgeID == id
}

func (s *Server) v1CreateRun(w http.ResponseWriter, r *http.Request, p principal) {
	agentID := r.PathValue("agent")
	if !p.authorizeAgent(agentID) {
		fail(w, 403, "密钥无权访问该 Agent。")
		return
	}
	var input struct {
		Input                  string         `json:"input"`
		ExternalConversationID string         `json:"external_conversation_id"`
		AgentRevision          int            `json:"agent_revision"`
		Metadata               map[string]any `json:"metadata"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Input = strings.TrimSpace(input.Input)
	if input.Input == "" || len(input.Input) > 100000 {
		fail(w, 400, "请提供 1–100000 字节的 input。")
		return
	}
	if len(input.ExternalConversationID) > 200 {
		fail(w, 400, "external_conversation_id 最多 200 字节。")
		return
	}
	idempotency := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotency) > 200 {
		fail(w, 400, "Idempotency-Key 最多 200 字节。")
		return
	}
	hash := requestHash(input.Input, input.ExternalConversationID, input.AgentRevision)
	if idempotency != "" {
		var runID, stored string
		err := s.store.db.QueryRow("SELECT run_id,request_hash FROM api_idempotency WHERE api_key_id=? AND key=?", p.ID, idempotency).Scan(&runID, &stored)
		if err == nil {
			if stored != hash {
				fail(w, 409, "同一 Idempotency-Key 的请求内容不同。")
				return
			}
			s.respondRun(w, r, p, runID)
			return
		}
		if err != sql.ErrNoRows {
			internal(w)
			return
		}
	}

	s.mu.Lock()
	agent, err := s.agentByID(agentID)
	if err != nil {
		s.mu.Unlock()
		fail(w, 404, "Agent 不存在。")
		return
	}
	if input.AgentRevision != 0 && input.AgentRevision != agent.Revision {
		s.mu.Unlock()
		fail(w, 409, "Agent 配置已更新，请重新读取 agent_revision 后重试。")
		return
	}
	conversationID, err := s.integrationConversation(p, agentID, input.ExternalConversationID)
	if err != nil {
		s.mu.Unlock()
		internal(w)
		return
	}
	run, err := s.enqueueRun(r.Context(), conversationID, input.Input, 0, nil)
	if err == nil {
		if _, execErr := s.store.db.Exec("UPDATE runs SET agent_revision=?,source='api' WHERE id=?", agent.Revision, run.ID); execErr != nil {
			err = execErr
		}
	}
	s.mu.Unlock()
	if err != nil {
		var rejected *admissionError
		if errors.As(err, &rejected) {
			fail(w, rejected.Status, rejected.Message)
		} else {
			internal(w)
		}
		return
	}
	if idempotency != "" {
		_, _ = s.store.db.Exec("INSERT OR IGNORE INTO api_idempotency(api_key_id,key,run_id,request_hash,created) VALUES(?,?,?,?,?)", p.ID, idempotency, run.ID, hash, time.Now().UnixMilli())
	}
	s.audit("api_run.create", run.ID, agentID)
	respond(w, 202, map[string]any{"run_id": run.ID, "conversation_id": conversationID, "status": run.Status, "agent_revision": agent.Revision})
}

func requestHash(values ...any) string {
	raw, _ := json.Marshal(values)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// integrationConversation maps a caller conversation id to an internal one.
func (s *Server) integrationConversation(p principal, agentID, external string) (string, error) {
	if external == "" {
		value := Conversation{ID: id(), Title: "API 对话", Created: time.Now().UnixMilli(), AgentID: agentID}
		if _, err := s.store.db.Exec("INSERT INTO conversations(id,title,created,agent_id) VALUES(?,?,?,?)", value.ID, value.Title, value.Created, value.AgentID); err != nil {
			return "", err
		}
		return value.ID, nil
	}
	var conversationID string
	err := s.store.db.QueryRow("SELECT conversation_id FROM api_conversations WHERE api_key_id=? AND external_id=?", p.ID, external).Scan(&conversationID)
	if err == nil {
		return conversationID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	value := Conversation{ID: id(), Title: external, Created: time.Now().UnixMilli(), AgentID: agentID}
	if _, err := s.store.db.Exec("INSERT INTO conversations(id,title,created,agent_id) VALUES(?,?,?,?)", value.ID, value.Title, value.Created, value.AgentID); err != nil {
		return "", err
	}
	if _, err := s.store.db.Exec("INSERT INTO api_conversations(api_key_id,external_id,conversation_id,agent_id,created) VALUES(?,?,?,?,?)", p.ID, external, value.ID, agentID, value.Created); err != nil {
		return "", err
	}
	return value.ID, nil
}

// runView is the stable /v1 run payload.
type runView struct {
	RunID         string       `json:"run_id"`
	Conversation  string       `json:"conversation_id"`
	Status        string       `json:"status"`
	Error         string       `json:"error,omitempty"`
	Answer        string       `json:"answer,omitempty"`
	EvidenceState string       `json:"evidence_state,omitempty"`
	CitationValid int          `json:"citation_valid"`
	Sources       []sourceView `json:"sources"`
	Created       int64        `json:"created"`
}

type sourceView struct {
	SourceID string `json:"source_id"`
	Title    string `json:"title"`
	Version  int    `json:"version"`
	URL      string `json:"url"`
}

func (s *Server) loadRunView(runID string) (runView, error) {
	var value runView
	var evidence string
	err := s.store.db.QueryRow("SELECT r.id,r.conversation_id,r.status,r.error,r.created,r.citation_valid,r.evidence_state FROM runs r WHERE r.id=?", runID).Scan(&value.RunID, &value.Conversation, &value.Status, &value.Error, &value.Created, &value.CitationValid, &evidence)
	if err != nil {
		return value, err
	}
	value.EvidenceState = evidence
	_ = s.store.db.QueryRow("SELECT content FROM messages WHERE run_id=? AND role='assistant'", runID).Scan(&value.Answer)
	value.Sources = []sourceView{}
	rows, err := s.store.db.Query("SELECT source_id,reference FROM run_sources WHERE run_id=? ORDER BY rowid", runID)
	if err != nil {
		return value, err
	}
	defer rows.Close()
	for rows.Next() {
		var source sourceView
		var raw string
		if err := rows.Scan(&source.SourceID, &raw); err != nil {
			return value, err
		}
		var hit knowledge.Hit
		_ = json.Unmarshal([]byte(raw), &hit)
		source.Title = hit.Title
		source.Version = hit.Version
		source.URL = hit.SourceURL
		value.Sources = append(value.Sources, source)
	}
	return value, rows.Err()
}

// runOwned reports whether the run belongs to an Agent this key may read.
func (s *Server) runOwned(p principal, runID string) (bool, error) {
	var agentID string
	err := s.store.db.QueryRow("SELECT c.agent_id FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE r.id=?", runID).Scan(&agentID)
	if err != nil {
		return false, err
	}
	return p.authorizeAgent(agentID), nil
}

func (s *Server) respondRun(w http.ResponseWriter, r *http.Request, p principal, runID string) {
	ok, err := s.runOwned(p, runID)
	if err == sql.ErrNoRows {
		fail(w, 404, "任务不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if !ok {
		fail(w, 403, "密钥无权访问该任务。")
		return
	}
	value, err := s.loadRunView(runID)
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, value)
}

func (s *Server) v1GetRun(w http.ResponseWriter, r *http.Request, p principal) {
	s.respondRun(w, r, p, r.PathValue("run"))
}

func (s *Server) v1CancelRun(w http.ResponseWriter, r *http.Request, p principal) {
	ok, err := s.runOwned(p, r.PathValue("run"))
	if err != nil {
		fail(w, 404, "任务不存在。")
		return
	}
	if !ok {
		fail(w, 403, "密钥无权访问该任务。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runID := r.PathValue("run")
	var status string
	if err := s.store.db.QueryRow("SELECT status FROM runs WHERE id=?", runID).Scan(&status); err != nil {
		fail(w, 404, "任务不存在。")
		return
	}
	if status != "queued" && status != "running" && status != "cancelling" {
		respond(w, 200, map[string]string{"status": status})
		return
	}
	if status == "queued" {
		tx, err := s.store.db.Begin()
		if err != nil {
			internal(w)
			return
		}
		defer tx.Rollback()
		if _, err := tx.Exec("UPDATE runs SET status='cancelled',credential='' WHERE id=?", runID); err != nil {
			internal(w)
			return
		}
		if err := recordEvent(tx, runID, 0, "run_cancelled", map[string]any{"before_dispatch": true, "via": "api"}); err != nil {
			internal(w)
			return
		}
		if err := tx.Commit(); err != nil {
			internal(w)
			return
		}
		_ = s.dispatch()
		respond(w, 200, map[string]string{"status": "cancelled"})
		return
	}
	if _, err := s.store.db.Exec("UPDATE runs SET status='cancelling',cancel_started=? WHERE id=?", time.Now().UnixMilli(), runID); err != nil {
		internal(w)
		return
	}
	_ = recordEventEvent(s, runID)
	if err := s.runtime.Send(map[string]any{"v": 1, "type": "cancel", "request_id": "cancel-" + runID, "run_id": runID}); err != nil {
		_ = s.queryStatus(runID)
	}
	respond(w, 202, map[string]string{"status": "cancelling"})
}

func recordEventEvent(s *Server, runID string) error {
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordEvent(tx, runID, 0, "cancel_requested", map[string]any{"via": "api"}); err != nil {
		return err
	}
	return tx.Commit()
}

// v1RunEvents streams state changes and answer deltas until the run settles.
func (s *Server) v1RunEvents(w http.ResponseWriter, r *http.Request, p principal) {
	ok, err := s.runOwned(p, r.PathValue("run"))
	if err != nil {
		fail(w, 404, "任务不存在。")
		return
	}
	if !ok {
		fail(w, 403, "密钥无权访问该任务。")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		internal(w)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(event string, data any) bool {
		raw, _ := json.Marshal(data)
		controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.WriteString(w, "event: "+event+"\ndata: "+string(raw)+"\n\n"); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	sent := ""
	status := ""
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		view, err := s.loadRunView(r.PathValue("run"))
		if err != nil {
			return
		}
		if view.Status != status {
			status = view.Status
			if !send("run.state", map[string]any{"run_id": view.RunID, "status": view.Status, "error": view.Error}) {
				return
			}
		}
		if strings.HasPrefix(view.Answer, sent) && len(view.Answer) > len(sent) {
			if !send("message.delta", map[string]any{"run_id": view.RunID, "text": view.Answer[len(sent):]}) {
				return
			}
			sent = view.Answer
		}
		if terminal(view.Status) {
			send("run.settled", view)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
		}
	}
}

func (s *Server) v1ListKnowledge(w http.ResponseWriter, r *http.Request, p principal) {
	values, err := s.knowledge.List(r.Context())
	if err != nil {
		knowledgeError(w, err)
		return
	}
	visible := []knowledge.Base{}
	for _, value := range values {
		if p.authorizeKnowledge(value.ID) {
			visible = append(visible, value)
		}
	}
	respond(w, 200, visible)
}

func (s *Server) v1UploadDocument(w http.ResponseWriter, r *http.Request, p principal) {
	baseID := r.PathValue("base")
	if !p.authorizeKnowledge(baseID) {
		fail(w, 403, "密钥无权访问该知识库。")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, knowledge.MaxUpload+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "请使用 multipart 上传，字段名为 file。")
		return
	}
	external := strings.TrimSpace(r.URL.Query().Get("external_id"))
	if len(external) > 200 {
		fail(w, 400, "external_id 最多 200 字节。")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		fail(w, 400, "上传请求必须包含一个 file 文档。")
		return
	}
	defer part.Close()
	if !s.uploadExtensionSupported(part.FileName()) {
		fail(w, 400, "该文件格式需要先安装并启用解析扩展。")
		return
	}
	documentID := ""
	if external != "" {
		_ = s.store.db.QueryRow("SELECT document_id FROM knowledge_external WHERE knowledge_id=? AND external_id=?", baseID, external).Scan(&documentID)
	}
	value, err := s.knowledge.Upload(r.Context(), baseID, documentID, part.FileName(), io.MultiReader(part, uploadEnd{reader}))
	if err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			fail(w, 413, "文件超过 20 MiB 上限。")
			return
		}
		knowledgeError(w, err)
		return
	}
	if external != "" {
		_, _ = s.store.db.Exec("INSERT INTO knowledge_external(knowledge_id,external_id,document_id,created) VALUES(?,?,?,?) ON CONFLICT(knowledge_id,external_id) DO UPDATE SET document_id=excluded.document_id", baseID, external, value.ID, time.Now().UnixMilli())
	}
	s.audit("api_document.upload", value.ID, baseID)
	respond(w, 202, map[string]any{"document_id": value.ID, "state": value.State, "version": value.Version, "external_id": external})
}

func (s *Server) v1DocumentStatus(w http.ResponseWriter, r *http.Request, p principal) {
	value, err := s.knowledge.Document(r.Context(), r.PathValue("document"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	if !p.authorizeKnowledge(value.KnowledgeID) {
		fail(w, 403, "密钥无权访问该文档。")
		return
	}
	respond(w, 200, map[string]any{"document_id": value.ID, "knowledge_id": value.KnowledgeID, "state": value.State, "version": value.Version, "active_version": value.ActiveVersion, "error": value.Error, "characters": value.Characters, "chunks": value.Chunks, "deleted": value.Deleted})
}

func (s *Server) v1DeleteDocument(w http.ResponseWriter, r *http.Request, p principal) {
	value, err := s.knowledge.Document(r.Context(), r.PathValue("document"))
	if err != nil {
		knowledgeError(w, err)
		return
	}
	if !p.authorizeKnowledge(value.KnowledgeID) {
		fail(w, 403, "密钥无权访问该文档。")
		return
	}
	if err := s.knowledge.DeleteDocument(r.Context(), value.ID); err != nil {
		knowledgeError(w, err)
		return
	}
	s.audit("api_document.delete", value.ID, value.KnowledgeID)
	respond(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) v1Search(w http.ResponseWriter, r *http.Request, p principal) {
	baseID := r.PathValue("base")
	if !p.authorizeKnowledge(baseID) {
		fail(w, 403, "密钥无权访问该知识库。")
		return
	}
	var input struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Limit == 0 {
		input.Limit = 5
	}
	result, err := s.knowledge.Search(r.Context(), knowledge.Scope{KnowledgeIDs: []string{baseID}}, knowledge.SearchRequest{Query: input.Query, Limit: input.Limit})
	if err != nil {
		knowledgeError(w, err)
		return
	}
	if len(result.Hits) == 0 {
		s.knowledge.RecordGap(r.Context(), baseID, input.Query)
	}
	respond(w, 200, result)
}

func (s *Server) v1Source(w http.ResponseWriter, r *http.Request, p principal) {
	request := knowledge.ReadRequest{SourceID: r.PathValue("source")}
	if value := r.URL.Query().Get("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			fail(w, 400, "offset 无效。")
			return
		}
		request.Offset = parsed
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 4000 {
			fail(w, 400, "limit 须为 1–4000。")
			return
		}
		request.Limit = parsed
	}
	scope, err := s.knowledge.Scope(r.Context(), true)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	if p.KnowledgeID != "" {
		scope.KnowledgeIDs = []string{p.KnowledgeID}
	}
	value, err := s.knowledge.Read(r.Context(), scope, request)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	respond(w, 200, value)
}

var _ = multipart.ErrMessageTooLarge
