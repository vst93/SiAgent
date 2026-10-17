package platform

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	agentruntime "siagent/internal/runtime"
)

var citationMarker = regexp.MustCompile(`\[(\d{1,3})\]`)

// recordAnswerEvidence stores whether a knowledge-backed answer had retrieved
// sources and whether its numeric citations pointed at those sources. It never
// rewrites the answer: the flag is shown to the administrator instead.
func recordAnswerEvidence(tx *sql.Tx, runID string) error {
	var answer string
	if err := tx.QueryRow("SELECT content FROM messages WHERE run_id=? AND role='assistant'", runID).Scan(&answer); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	var toolUses, sources int
	if err := tx.QueryRow("SELECT count(*) FROM tool_calls WHERE run_id=? AND name IN ('knowledge_search','knowledge_read')", runID).Scan(&toolUses); err != nil {
		return err
	}
	if err := tx.QueryRow("SELECT count(*) FROM run_sources WHERE run_id=?", runID).Scan(&sources); err != nil {
		return err
	}
	// Persisted sources are the ground truth: a knowledge-backed answer either
	// cites real retrieval or it does not.
	state := ""
	if sources > 0 {
		state = "used"
	} else if toolUses > 0 {
		state = "none"
	}
	citation := -1
	highest := 0
	for _, match := range citationMarker.FindAllStringSubmatch(answer, -1) {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if value > highest {
			highest = value
		}
	}
	if highest > 0 {
		citation = 0
		if highest <= sources {
			citation = 1
		}
	}
	if _, err := tx.Exec("UPDATE runs SET citation_valid=?,evidence_state=? WHERE id=?", citation, state, runID); err != nil {
		return err
	}
	return recordEvent(tx, runID, 0, "answer_evidence", map[string]any{"evidence_state": state, "citation_valid": citation, "sources": sources, "knowledge_tool_uses": toolUses})
}

func terminal(status string) bool {
	return status != "running" && status != "cancelling" && status != "queued"
}
func (s *Server) HandleEvent(event agentruntime.Event) error {
	if event.Type == "knowledge_request" || event.Type == "knowledge_cancel" {
		return s.knowledgeRequest(event)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pending, ok := s.pendingTests[event.RequestID]; ok {
		select {
		case pending <- event:
		default:
		}
		return nil
	}
	if event.Type == "runtime_unavailable" {
		s.restarting = true
		return s.interruptAll("对话进程已退出，未完成任务不会自动重试；请核对已有内容和工具操作。", true)
	}
	if event.Type == "ready" {
		s.restarting = false
		return s.dispatch()
	}
	if event.Type == "status" && strings.HasPrefix(event.RequestID, "status-") {
		return s.reconcile(strings.TrimPrefix(event.RequestID, "status-"), event.Data)
	}
	if event.Type == "error" && strings.HasPrefix(event.RequestID, "cancel-") {
		return s.queryStatus(strings.TrimPrefix(event.RequestID, "cancel-"))
	}
	runID := event.RunID
	if runID == "" && event.Type == "error" {
		runID = event.RequestID
	}
	if runID == "" {
		return nil
	}
	switch event.Type {
	case "run_started", "message_delta", "thinking_delta", "compaction_start", "compaction_end", "run_completed", "run_failed", "run_cancelled", "run_cancelling", "run_released", "run_interrupted", "tool_start", "tool_end", "error":
	default:
		return nil
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var seq int64
	var released bool
	err = tx.QueryRow("SELECT status,last_seq,released FROM runs WHERE id=?", runID).Scan(&status, &seq, &released)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if event.Type != "error" && event.Sequence <= seq {
		return nil
	}
	if terminal(status) && event.Type != "run_released" {
		return nil
	}
	if event.Sequence > seq+1 {
		if err := recordEvent(tx, runID, event.Sequence, "event_gap", map[string]int64{"expected": seq + 1, "received": event.Sequence}); err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE runs SET status='interrupted',error='执行事件不完整，结果无法确认；请核对已有内容及工具操作。',last_seq=?,credential='',cancel_started=? WHERE id=?", event.Sequence, time.Now().UnixMilli(), runID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE tool_calls SET status='interrupted' WHERE run_id=? AND status='running'", runID); err != nil {
			return err
		}
		if event.Type == "run_released" {
			if _, err = tx.Exec("UPDATE runs SET released=1 WHERE id=?", runID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if event.Type != "run_released" {
			_ = s.runtime.Send(map[string]any{"v": 1, "type": "cancel", "request_id": "cancel-" + runID, "run_id": runID})
		}
		return s.dispatch()
	}
	message := ""
	data := map[string]any{}
	switch event.Type {
	case "thinking_delta":
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return err
		}
		var config string
		if err := tx.QueryRow("SELECT config FROM runs WHERE id=?", runID).Scan(&config); err != nil {
			return err
		}
		var snapshot runConfig
		if err := json.Unmarshal([]byte(config), &snapshot); err != nil {
			return err
		}
		if snapshot.Agent.effectivePolicy().Output.Thinking {
			if _, err := tx.Exec("UPDATE runs SET thinking=substr(thinking||?,1,64000) WHERE id=?", payload.Text, runID); err != nil {
				return err
			}
		}
		data["bytes"] = len(payload.Text)
	case "compaction_start", "compaction_end":
	// Compaction content stays in private Runtime storage; diagnostics show lifecycle only.
	case "message_delta":
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE messages SET content=content||? WHERE run_id=? AND role='assistant'", payload.Text, runID); err != nil {
			return err
		}
		data["bytes"] = len(payload.Text)
	case "run_completed":
		status = "completed"
		var payload struct {
			Answer *string `json:"answer"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return err
		}
		if payload.Answer != nil {
			if _, err = tx.Exec("UPDATE messages SET content=? WHERE run_id=? AND role='assistant'", *payload.Answer, runID); err != nil {
				return err
			}
			data["bytes"] = len(*payload.Answer)
		}
		if err = recordAnswerEvidence(tx, runID); err != nil {
			return err
		}
	case "run_failed":
		status = "failed"
		var payload struct {
			ErrorCode string `json:"error_code"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return err
		}
		message = "模型请求未完成，请检查服务地址、模型名称、密钥权限和额度。已产生的内容会保留。"
		switch payload.ErrorCode {
		case "auth":
			message = "模型密钥被拒绝，请更新密钥并确认模型访问权限。"
		case "quota":
			message = "模型服务限制请求或余额不足，请检查额度后稍后重试。"
		case "not_found":
			message = "模型接口或名称不存在，请检查服务地址的路径前缀及模型名称。"
		case "timeout":
			message = "运行超过时间限制，已确认停止。已产生的内容保留，不自动重试。"
		case "tool_timeout":
			message = "工具调用超过时间限制，已停止后续调用；已发生的外部操作不能撤销，请核对。"
		case "tool_rounds":
			message = "已达到工具调用轮数上限，已停止后续调用。"
		case "tool_setup":
			message = "工具连接失败或工具定义已变化，请检查 MCP 连接并重新测试。"
		case "tool_uncertain":
			message = "工具调用结果不明，已停止后续执行；请核对外部系统，不会自动重试。"
		case "request_size_limit":
			message = "请求数据量超过平台内部保护上限，请新建对话；已有内容未删除。"
		case "compaction_failed":
			message = "上下文摘要生成失败，本次任务已停止。请检查模型服务，或调整 Agent 上下文策略后重新发送；历史和已有工具记录保留。"
		case "context_limit":
			message = "本会话已接近上下文限制，请新建对话。已有内容未删除。"
		}
		data["reason"] = message
	case "run_interrupted":
		status = "interrupted"
		message = "运行已中断，工具操作的结果可能不明；请核对已有内容和外部系统。"
	case "run_cancelled":
		status = "cancelled"
	case "run_cancelling":
		status = "cancelling"
		if _, err = tx.Exec("UPDATE runs SET cancel_started=CASE WHEN cancel_started=0 THEN ? ELSE cancel_started END WHERE id=?", time.Now().UnixMilli(), runID); err != nil {
			return err
		}
	case "run_released":
		released = true
		if !terminal(status) {
			status = "interrupted"
			message = "未收到完整运行终态，结果无法确认；请核对已有内容。"
		}
	case "tool_start", "tool_end":
		var payload struct {
			Name      string          `json:"toolName"`
			Arguments json.RawMessage `json:"args"`
			Result    string          `json:"result"`
			Unknown   bool            `json:"outcome_unknown"`
			CallID    string          `json:"toolCallId"`
			Entry     *struct {
				Model []struct {
					IsError bool `json:"isError"`
				} `json:"model"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return err
		}
		if len(payload.Name) > 200 {
			payload.Name = payload.Name[:200]
		}
		if len(payload.CallID) > 200 {
			payload.CallID = payload.CallID[:200]
		}
		toolStatus := "running"
		if event.Type == "tool_end" {
			toolStatus = "completed"
			if payload.Entry == nil {
				toolStatus = "interrupted"
			} else {
				for _, m := range payload.Entry.Model {
					if m.IsError {
						toolStatus = "failed"
					}
				}
			}
		}
		if payload.Unknown {
			toolStatus = "interrupted"
		}
		arguments := ""
		if len(payload.Arguments) > 0 {
			arguments = string(payload.Arguments)
		}
		if len(arguments) > 32000 {
			arguments = "[参数超过 32 KB，未保留]"
		}
		if len(payload.Result) > 32000 {
			payload.Result = string([]rune(payload.Result)[:min(len([]rune(payload.Result)), 8000)]) + "\n[输出已截断]"
		}
		if _, err = tx.Exec("INSERT INTO tool_calls(run_id,call_id,name,status,arguments,result) VALUES(?,?,?,?,?,?) ON CONFLICT(run_id,call_id) DO UPDATE SET status=excluded.status,arguments=CASE WHEN excluded.arguments='' THEN tool_calls.arguments ELSE excluded.arguments END,result=excluded.result", runID, payload.CallID, payload.Name, toolStatus, arguments, payload.Result); err != nil {
			return err
		}
		data["tool"] = payload.Name
		data["status"] = toolStatus
	case "error":
		status = "failed"
		released = true
		message = "运行请求未被接受，请检查模型配置及对话服务状态后重试。"
		if event.Code == "duplicate_run" {
			status = "interrupted"
			released = false
			message = "运行标识已存在，结果需要核对。"
			if _, err = tx.Exec("UPDATE runs SET cancel_started=? WHERE id=?", time.Now().UnixMilli(), runID); err != nil {
				return err
			}
		}
	}
	if event.Sequence < seq {
		event.Sequence = seq
	}
	// A release acknowledgement must not erase a known terminal error.
	if terminal(status) && event.Type == "run_released" && message == "" {
		if err := tx.QueryRow("SELECT error FROM runs WHERE id=?", runID).Scan(&message); err != nil {
			return err
		}
	}
	_, err = tx.Exec("UPDATE runs SET status=?,error=?,last_seq=?,released=?,credential=CASE WHEN ? THEN '' ELSE credential END,cancel_started=CASE WHEN ? AND NOT ? AND cancel_started=0 THEN ? ELSE cancel_started END WHERE id=?", status, message, event.Sequence, released, terminal(status), terminal(status), released, time.Now().UnixMilli(), runID)
	if err != nil {
		return err
	}
	if terminal(status) {
		if _, err = tx.Exec("UPDATE tool_calls SET status='interrupted' WHERE run_id=? AND status='running'", runID); err != nil {
			return err
		}
	}
	logSeq := event.Sequence
	if event.Type == "error" {
		logSeq = 0
	}
	if err := recordEvent(tx, runID, logSeq, event.Type, data); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.dispatch()
}
func (s *Server) reconcile(runID string, data json.RawMessage) error {
	var remote *struct {
		RunID    string `json:"run_id"`
		Status   string `json:"status"`
		Released int    `json:"released"`
		Seq      int64  `json:"seq"`
	}
	if err := json.Unmarshal(data, &remote); err != nil {
		return err
	}
	var status string
	var released bool
	err := s.store.db.QueryRow("SELECT status,released FROM runs WHERE id=?", runID).Scan(&status, &released)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if released {
		return nil
	}
	if remote != nil && remote.RunID != runID {
		return nil
	}
	if remote != nil && remote.Released != 1 {
		return nil
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !terminal(status) {
		if _, err := tx.Exec("UPDATE runs SET status='interrupted',error='运行已停止但缺少完整结果，不能确认成功；请核对已有内容及工具操作。' WHERE id=?", runID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE runs SET released=1,credential='' WHERE id=?", runID); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE tool_calls SET status='interrupted' WHERE run_id=? AND status='running'", runID); err != nil {
		return err
	}
	if err := recordEvent(tx, runID, 0, "reconciled", map[string]bool{"released": true}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.dispatch()
}
