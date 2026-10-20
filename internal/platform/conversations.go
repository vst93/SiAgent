package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"siagent/internal/knowledge"
	"strings"
	"time"
)

type Conversation struct {
	ChannelID     string `json:"channel_id,omitempty"`
	ChannelKind   string `json:"channel_kind,omitempty"`
	ChannelSender string `json:"channel_sender,omitempty"`
	ChannelGroup  string `json:"channel_group,omitempty"`
	AgentID       string `json:"agent_id"`
	ID            string `json:"id"`
	Title         string `json:"title"`
	Created       int64  `json:"created"`
}
type Message struct {
	Attachments []Attachment `json:"attachments,omitempty"`
	ID          int64        `json:"id"`
	RunID       string       `json:"run_id"`
	Role        string       `json:"role"`
	Sender      string       `json:"sender,omitempty"`
	Content     string       `json:"content"`
	Created     int64        `json:"created"`
}
type Run struct {
	Output        *OutputPolicy `json:"output,omitempty"`
	Thinking      string        `json:"thinking,omitempty"`
	EvidenceState string        `json:"evidence_state,omitempty"`
	Tools         []ToolCall    `json:"tools,omitempty"`
	ID            string        `json:"id"`
	Status        string        `json:"status"`
	Error         string        `json:"error"`
	CitationValid int           `json:"citation_valid"`
	Created       int64         `json:"created"`
}
type RunSource struct {
	RunID string `json:"run_id"`
	knowledge.Hit
}
type Snapshot struct {
	Sources      []RunSource  `json:"sources"`
	Conversation Conversation `json:"conversation"`
	Messages     []Message    `json:"messages"`
	Runs         []Run        `json:"runs"`
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id,title,created,agent_id,channel_id,channel_kind,channel_sender,channel_group FROM conversations WHERE NOT EXISTS(SELECT 1 FROM conversation_cleanup WHERE conversation_id=conversations.id) ORDER BY created DESC,rowid DESC")
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	values := []Conversation{}
	for rows.Next() {
		var v Conversation
		if err := rows.Scan(&v.ID, &v.Title, &v.Created, &v.AgentID, &v.ChannelID, &v.ChannelKind, &v.ChannelSender, &v.ChannelGroup); err != nil {
			internal(w)
			return
		}
		values = append(values, v)
	}
	if rows.Err() != nil {
		internal(w)
		return
	}
	respond(w, 200, values)
}
func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AgentID string `json:"agent_id"`
	}
	if r.ContentLength != 0 {
		if !decode(w, r, &input) {
			return
		}
	}
	if input.AgentID == "" {
		input.AgentID = "default"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.agentByID(input.AgentID)
	if err == sql.ErrNoRows {
		fail(w, 409, "请先配置模型并创建助手。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if !a.Enabled || a.Archived {
		fail(w, 409, "助手已停用或归档，不能创建新对话。")
		return
	}
	if a.Policy.Session.Types == "group" {
		fail(w, 403, "此助手仅接收群聊消息，请在绑定的群聊中使用。")
		return
	}
	v := Conversation{ID: id(), Title: "新对话", Created: time.Now().UnixMilli(), AgentID: a.ID}
	if _, err := s.store.db.Exec("INSERT INTO conversations(id,title,created,agent_id) VALUES(?,?,?,?)", v.ID, v.Title, v.Created, v.AgentID); err != nil {
		internal(w)
		return
	}
	respond(w, 201, v)
}
func (s *Server) snapshot(conversationID string) (Snapshot, error) {
	result := Snapshot{Messages: []Message{}, Runs: []Run{}, Sources: []RunSource{}}
	err := s.store.db.QueryRow("SELECT id,title,created,agent_id,channel_id,channel_kind,channel_sender,channel_group FROM conversations WHERE id=? AND NOT EXISTS(SELECT 1 FROM conversation_cleanup WHERE conversation_id=conversations.id)", conversationID).Scan(&result.Conversation.ID, &result.Conversation.Title, &result.Conversation.Created, &result.Conversation.AgentID, &result.Conversation.ChannelID, &result.Conversation.ChannelKind, &result.Conversation.ChannelSender, &result.Conversation.ChannelGroup)
	if err != nil {
		return result, err
	}
	rows, err := s.store.db.Query("SELECT id,run_id,role,content,created,attachments FROM messages WHERE conversation_id=? ORDER BY id", conversationID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var message Message
		var attachments string
		if err := rows.Scan(&message.ID, &message.RunID, &message.Role, &message.Content, &message.Created, &attachments); err != nil {
			rows.Close()
			return result, err
		}
		if attachments != "" {
			if err := json.Unmarshal([]byte(attachments), &message.Attachments); err != nil {
				rows.Close()
				return result, err
			}
		}
		result.Messages = append(result.Messages, message)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	configs := map[string]runConfig{}
	rows, err = s.store.db.Query("SELECT r.id,r.status,r.error,r.created,r.config,r.thinking,r.citation_valid,r.evidence_state FROM runs r WHERE r.conversation_id=? ORDER BY r.created,r.rowid", conversationID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var run Run
		var config, thinking string
		if err := rows.Scan(&run.ID, &run.Status, &run.Error, &run.Created, &config, &thinking, &run.CitationValid, &run.EvidenceState); err != nil {
			rows.Close()
			return result, err
		}
		var configSnapshot runConfig
		if err := json.Unmarshal([]byte(config), &configSnapshot); err != nil {
			rows.Close()
			return result, err
		}
		configs[run.ID] = configSnapshot
		output := configSnapshot.Agent.effectivePolicy().Output
		run.Output = &output
		if output.Thinking {
			run.Thinking = thinking
		}
		result.Runs = append(result.Runs, run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	// Channel history names the sender of every user message, including shared
	// groups where one conversation holds several participants.
	if result.Conversation.ChannelID != "" {
		senders := map[string]string{}
		rows, err = s.store.db.Query("SELECT run_id,member FROM channel_inbox WHERE conversation_id=? AND run_id<>''", conversationID)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var runID, member string
			if err := rows.Scan(&runID, &member); err != nil {
				rows.Close()
				return result, err
			}
			if who := sessionParticipants(member); who.sender != "" {
				senders[runID] = who.sender
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
		for i := range result.Messages {
			if result.Messages[i].Role == "user" {
				result.Messages[i].Sender = senders[result.Messages[i].RunID]
			}
		}
	}
	rows, err = s.store.db.Query("SELECT t.run_id,t.call_id,t.name,t.status FROM tool_calls t JOIN runs r ON r.id=t.run_id WHERE r.conversation_id=? ORDER BY t.rowid", conversationID)
	if err != nil {
		return result, err
	}
	runIndex := map[string]int{}
	for i, run := range result.Runs {
		runIndex[run.ID] = i
	}
	for rows.Next() {
		var runID string
		var tool ToolCall
		if err := rows.Scan(&runID, &tool.ID, &tool.Name, &tool.Status); err != nil {
			rows.Close()
			return result, err
		}
		i := runIndex[runID]
		tool.DisplayName = toolDisplayName(configs[runID], tool.Name)
		if result.Runs[i].Output.Tools != "hidden" {
			result.Runs[i].Tools = append(result.Runs[i].Tools, tool)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = s.store.db.Query("SELECT s.run_id,s.reference FROM run_sources s JOIN runs r ON r.id=s.run_id WHERE r.conversation_id=? ORDER BY s.rowid", conversationID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var source RunSource
		var raw string
		if err := rows.Scan(&source.RunID, &raw); err != nil {
			return result, err
		}
		if err := json.Unmarshal([]byte(raw), &source.Hit); err != nil {
			return result, err
		}
		if i, ok := runIndex[source.RunID]; ok && result.Runs[i].Output.Sources {
			result.Sources = append(result.Sources, source)
		}
	}
	return result, rows.Err()
}

// runEvidence lists the claim-to-source map for one run.
func (s *Server) runEvidence(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT claim_index,source_ids FROM run_evidence WHERE run_id=? ORDER BY claim_index", r.PathValue("id"))
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	values := []map[string]any{}
	for rows.Next() {
		var index int
		var raw string
		if err := rows.Scan(&index, &raw); err != nil {
			internal(w)
			return
		}
		var ids []string
		_ = json.Unmarshal([]byte(raw), &ids)
		values = append(values, map[string]any{"claim_index": index, "source_ids": ids})
	}
	respond(w, 200, values)
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	snapshot, err := s.snapshot(r.PathValue("id"))
	s.mu.Unlock()
	if err == sql.ErrNoRows {
		fail(w, 404, "对话不存在，请从列表选择已有对话。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, snapshot)
}
func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Input         string   `json:"input"`
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Input = strings.TrimSpace(input.Input)
	if input.Input == "" && len(input.AttachmentIDs) > 0 {
		input.Input = "请查看附件。"
	}
	if input.Input == "" || len(input.Input) > 100000 {
		fail(w, 400, "请输入消息，单条消息不超过 100,000 字节。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var channel int
	if err := s.store.db.QueryRow("SELECT count(*) FROM conversations WHERE id=? AND channel_id<>''", r.PathValue("id")).Scan(&channel); err != nil {
		internal(w)
		return
	}
	if channel > 0 {
		fail(w, 403, "渠道会话在管理后台仅可查看和取消任务，请从原聊天平台发送新消息。")
		return
	}

	run, err := s.enqueueRunWithAttachments(r.Context(), r.PathValue("id"), input.Input, 0, input.AttachmentIDs, nil)
	if err != nil {
		var rejected *admissionError
		if errors.As(err, &rejected) {
			fail(w, rejected.Status, rejected.Message)
		} else {
			internal(w)
		}
		return
	}
	respond(w, 202, run)
}

type admissionError struct {
	Status  int
	Message string
}

func (e *admissionError) Error() string { return e.Message }

// enqueueRun is shared by Web and channel admission; callers hold s.mu.
// The admission callback joins the run transaction, so an acknowledged inbox
// message cannot lose its run link or be admitted twice after a crash.
func (s *Server) enqueueRun(ctx context.Context, conversationID, input string, receivedAt int64, admitted func(*sql.Tx, string) error) (Run, error) {
	return s.enqueueRunWithAttachments(ctx, conversationID, input, receivedAt, nil, admitted)
}

func (s *Server) enqueueRunWithAttachments(ctx context.Context, conversationID, input string, receivedAt int64, attachmentIDs []string, admitted func(*sql.Tx, string) error) (Run, error) {

	if s.stopping || !s.ready() || s.restarting {
		return Run{}, &admissionError{503, "对话服务正在启动或恢复，请稍后重试。"}
	}
	var agentID string
	err := s.store.db.QueryRow("SELECT agent_id FROM conversations WHERE id=? AND NOT EXISTS(SELECT 1 FROM conversation_cleanup WHERE conversation_id=conversations.id)", conversationID).Scan(&agentID)
	if err == sql.ErrNoRows {
		return Run{}, &admissionError{404, "对话不存在。"}
	}
	if err != nil {
		return Run{}, err
	}
	agent, err := s.agentByID(agentID)
	if err != nil {
		return Run{}, err
	}
	if !agent.Enabled || agent.Archived {
		return Run{}, &admissionError{409, "助手已停用或归档，新消息暂不接收；已排队任务正常处理。"}
	}
	if agent.Policy.Session.Types == "group" {
		var channel string
		if err := s.store.db.QueryRow("SELECT channel_id FROM conversations WHERE id=?", conversationID).Scan(&channel); err != nil {
			return Run{}, err
		}
		if channel == "" {
			return Run{}, &admissionError{403, "此助手仅接收群聊消息。"}
		}
	}
	primaryModel, primaryErr := s.store.modelByID(agent.ModelID)
	if primaryErr != nil && primaryErr != sql.ErrNoRows {
		return Run{}, primaryErr
	}
	candidates := []Model{}
	if primaryErr == nil && !primaryModel.Archived && s.store.modelProviderError(primaryModel) == "" {
		candidates = append(candidates, primaryModel)
	}
	for _, fallbackID := range agent.FallbackModelIDs {
		fallback, err := s.store.modelByID(fallbackID)
		if err != nil || fallback.Archived || s.store.modelProviderError(fallback) != "" {
			continue
		}
		candidates = append(candidates, fallback)
	}
	if len(candidates) == 0 {
		if primaryErr == sql.ErrNoRows || primaryModel.Archived {
			return Run{}, &admissionError{409, "关联模型已归档或不存在，请重新选择模型。"}
		}
		return Run{}, &admissionError{409, s.store.modelProviderError(primaryModel)}
	}
	model := candidates[0]
	fallbackModels := candidates[1:]
	attachments, err := s.loadAttachments(conversationID, attachmentIDs)
	if err != nil {
		return Run{}, &admissionError{400, err.Error()}
	}
	for _, item := range attachments {
		if item.Kind == "image" && !supportsModelInput(model, "image") {
			return Run{}, &admissionError{409, "当前模型未声明支持图片输入，请切换视觉模型或移除图片。"}
		}
	}
	var exists, waiting, total, occupied, sameActive int
	if err := s.store.db.QueryRow("SELECT count(*) FROM conversations WHERE id=? AND NOT EXISTS(SELECT 1 FROM conversation_cleanup WHERE conversation_id=conversations.id)", conversationID).Scan(&exists); err != nil {
		return Run{}, err
	}
	if exists == 0 {
		return Run{}, &admissionError{404, "对话不存在。"}
	}
	if err := s.dispatch(); err != nil {
		return Run{}, err
	}
	if err := s.store.db.QueryRow("SELECT count(*) FROM runs WHERE status='queued' AND conversation_id=?", conversationID).Scan(&waiting); err != nil {
		return Run{}, err
	}
	if err := s.store.db.QueryRow("SELECT count(*) FROM runs WHERE status='queued'").Scan(&total); err != nil {
		return Run{}, err
	}
	if err := s.store.db.QueryRow("SELECT count(*) FROM runs WHERE released=0").Scan(&occupied); err != nil {
		return Run{}, err
	}
	if err := s.store.db.QueryRow("SELECT count(*) FROM runs WHERE released=0 AND conversation_id=?", conversationID).Scan(&sameActive); err != nil {
		return Run{}, err
	}
	immediate := occupied < s.limits.Concurrent && sameActive == 0 && waiting == 0
	if !immediate && (waiting >= s.limits.ConversationQueue || total >= s.limits.GlobalQueue) {
		return Run{}, &admissionError{429, "等待队列已满，请等待已有任务完成或取消排队任务后再发送。"}
	}
	bindings, err := s.store.bindings(agent.ID)
	if err != nil {
		return Run{}, err
	}
	capabilities, toolSecrets, err := s.store.capabilitySnapshot(bindings)
	if err != nil {
		return Run{}, &admissionError{409, err.Error()}
	}
	skillBytes := 0
	for _, skill := range capabilities.Skills {
		skillBytes += len(skill.Content) + len(skill.Description) + len(skill.Name)
		for _, file := range skill.Files {
			skillBytes += len(file.Path) + 4
		}
	}
	var bytes int
	if agent.Policy.Context.Strategy == "full" {
		if err := s.store.db.QueryRow("SELECT coalesce(sum(length(CAST(content AS BLOB))),0) FROM messages WHERE conversation_id=?", conversationID).Scan(&bytes); err != nil {
			return Run{}, err
		}
	}
	contextBudget := 8 * 1024 * 1024
	attachmentBytes := 0
	for _, item := range attachments {
		attachmentBytes += len(item.Extracted) + len(item.Name)
	}
	if bytes+len(input)+len(agent.Instructions)+skillBytes+attachmentBytes+1024 > contextBudget {
		return Run{}, &admissionError{409, "会话数据量超过平台内部保护上限，请新建对话后发送。已有内容不会删除。"}
	}
	knowledgeIDs, err := s.knowledge.Bindings(ctx, agent.ID)
	if err != nil {
		return Run{}, err
	}
	runID := id()
	created := time.Now().UnixMilli()
	if receivedAt == 0 {
		receivedAt = created
	}
	timezone := agent.Policy.Timezone
	if timezone == "" {
		timezone = defaultAgentPolicy().Timezone
	}
	turnTime := &TurnTime{ReceivedAt: time.UnixMilli(receivedAt).UTC().Format(time.RFC3339Nano), Timezone: timezone}
	fallbackSnapshots := make([]runFallback, 0, len(fallbackModels))
	fallbackKeys := make([]string, 0, len(fallbackModels))
	for _, fallback := range fallbackModels {
		fallbackSnapshots = append(fallbackSnapshots, runFallback{ModelOptions: fallback.Options, ModelID: fallback.ID, ModelVersion: fallback.Version, Provider: fallback.Provider, BaseURL: fallback.BaseURL, Model: fallback.Name})
		fallbackKeys = append(fallbackKeys, fallback.APIKey)
	}
	config, _ := json.Marshal(runConfig{Fallbacks: fallbackSnapshots, TimeContext: turnTime, ModelOptions: model.Options, ModelID: model.ID, ModelVersion: model.Version, Provider: model.Provider, BaseURL: model.BaseURL, Model: model.Name, Agent: agent, Limits: s.limits, Capabilities: &capabilities, KnowledgeIDs: knowledgeIDs})
	environment, err := s.store.agentEnvironment(agent.ID)
	if err != nil {
		return Run{}, err
	}
	credentials, _ := json.Marshal(RunCredentials{ModelKey: model.APIKey, FallbackKeys: fallbackKeys, MCPs: toolSecrets, Env: environment})
	tx, err := s.store.db.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO runs(id,conversation_id,status,config,credential,created) VALUES(?,?,'queued',?,?,?)", runID, conversationID, string(config), s.store.encrypt(string(credentials)), created)
	if err == nil {
		public := make([]Attachment, 0, len(attachments))
		for _, item := range attachments {
			public = append(public, item.Attachment)
		}
		publicAttachments, _ := json.Marshal(public)
		_, err = tx.Exec("INSERT INTO messages(conversation_id,run_id,role,content,created,attachments) VALUES(?,?,'user',?,?,?),(?,?,'assistant','',?,'[]')", conversationID, runID, input, receivedAt, string(publicAttachments), conversationID, runID, created)
		if err == nil {
			err = s.linkAttachments(tx, runID, attachments)
		}
	}
	title := []rune(input)
	if len(title) > 32 {
		title = title[:32]
	}
	if err == nil {
		_, err = tx.Exec("UPDATE conversations SET title=? WHERE id=? AND title='新对话'", string(title), conversationID)
	}
	if err == nil {
		err = recordEvent(tx, runID, 0, "queued", map[string]any{"model_version": model.Version})
	}
	if err == nil && admitted != nil {
		err = admitted(tx, runID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return Run{}, err
	}
	if err := s.dispatch(); err != nil {
		return Run{}, err
	}
	var status string
	if err := s.store.db.QueryRow("SELECT status FROM runs WHERE id=?", runID).Scan(&status); err != nil {
		return Run{}, err
	}
	return Run{ID: runID, Status: status, Created: created}, nil
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runID := r.PathValue("id")
	var status string
	err := s.store.db.QueryRow("SELECT status FROM runs WHERE id=?", runID).Scan(&status)
	if err == sql.ErrNoRows {
		fail(w, 404, "任务不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if status != "queued" && status != "running" && status != "cancelling" {
		fail(w, 409, "任务已结束，请查看执行结果。")
		return
	}
	if status == "queued" {
		tx, err := s.store.db.Begin()
		if err != nil {
			internal(w)
			return
		}
		defer tx.Rollback()
		_, err = tx.Exec("UPDATE runs SET status='cancelled',credential='' WHERE id=?", runID)
		if err == nil {
			err = recordEvent(tx, runID, 0, "run_cancelled", map[string]bool{"before_dispatch": true})
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			internal(w)
			return
		}
		if err := s.dispatch(); err != nil {
			internal(w)
			return
		}
		respond(w, 200, map[string]string{"status": "cancelled"})
		return
	}
	if status != "cancelling" {
		tx, err := s.store.db.Begin()
		if err != nil {
			internal(w)
			return
		}
		defer tx.Rollback()
		_, err = tx.Exec("UPDATE runs SET status='cancelling',cancel_started=? WHERE id=?", time.Now().UnixMilli(), runID)
		if err == nil {
			err = recordEvent(tx, runID, 0, "cancel_requested", map[string]any{})
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			internal(w)
			return
		}
		if err := s.runtime.Send(map[string]any{"v": 1, "type": "cancel", "request_id": "cancel-" + runID, "run_id": runID}); err != nil {
			_ = s.queryStatus(runID)
		}
	}
	respond(w, 202, map[string]string{"status": "cancelling"})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		internal(w)
		return
	}
	s.mu.Lock()
	snapshot, err := s.snapshot(r.PathValue("id"))
	s.mu.Unlock()
	if err == sql.ErrNoRows {
		fail(w, 404, "对话不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(data []byte) error {
		controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data); err != nil {
			return err
		}
		return controller.Flush()
	}
	data, _ := json.Marshal(snapshot)
	if err := send(data); err != nil {
		return
	}
	previous := string(data)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		case <-ticker.C:
			if _, ok := s.session(r); !ok {
				return
			}
			s.mu.Lock()
			snapshot, err = s.snapshot(r.PathValue("id"))
			s.mu.Unlock()
			if err != nil {
				return
			}
			data, _ = json.Marshal(snapshot)
			if string(data) != previous {
				if err := send(data); err != nil {
					return
				}
				previous = string(data)
			}
		}
	}
}
