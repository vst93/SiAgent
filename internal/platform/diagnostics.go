package platform

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
)

type ToolCall struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Name        string `json:"name"`
	Status      string `json:"status"`
}
type DiagnosticEvent struct {
	Sequence int64           `json:"seq"`
	Kind     string          `json:"kind"`
	Data     json.RawMessage `json:"data"`
	Created  int64           `json:"created"`
}

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{}
	rows, err := s.store.db.Query("SELECT status,count(*) FROM runs GROUP BY status")
	if err != nil {
		internal(w)
		return
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			internal(w)
			return
		}
		counts[status] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]any{"runtime_ready": s.ready() && !s.restarting, "limits": s.limits, "counts": counts})
}
func (s *Server) runDetails(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runID := r.PathValue("id")
	var run Run
	var config string
	var started int64
	var released bool
	err := s.store.db.QueryRow("SELECT id,status,error,created,config,started,released FROM runs WHERE id=?", runID).Scan(&run.ID, &run.Status, &run.Error, &run.Created, &config, &started, &released)
	if err == sql.ErrNoRows {
		fail(w, 404, "任务不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	var snapshot runConfig
	if err := json.Unmarshal([]byte(config), &snapshot); err != nil {
		internal(w)
		return
	}
	rows, err := s.store.db.Query("SELECT call_id,name,status FROM tool_calls WHERE run_id=? ORDER BY rowid", runID)
	if err != nil {
		internal(w)
		return
	}
	tools := []ToolCall{}
	for rows.Next() {
		var call ToolCall
		if err := rows.Scan(&call.ID, &call.Name, &call.Status); err != nil {
			rows.Close()
			internal(w)
			return
		}
		call.DisplayName = toolDisplayName(snapshot, call.Name)
		tools = append(tools, call)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w)
		return
	}
	rows, err = s.store.db.Query("SELECT seq,kind,data,created FROM run_events WHERE run_id=? ORDER BY id DESC LIMIT 200", runID)
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	events := []DiagnosticEvent{}
	for rows.Next() {
		var event DiagnosticEvent
		var data string
		if err := rows.Scan(&event.Sequence, &event.Kind, &data, &event.Created); err != nil {
			internal(w)
			return
		}
		event.Data = json.RawMessage(data)
		events = append(events, event)
	}
	if rows.Err() != nil {
		internal(w)
		return
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	respond(w, 200, map[string]any{"run": run, "started": started, "released": released, "agent_id": snapshot.Agent.ID, "agent_name": snapshot.Agent.Name, "model_id": snapshot.ModelID, "model": snapshot.Model, "model_version": snapshot.ModelVersion, "limits": snapshot.Limits, "output": snapshot.Agent.effectivePolicy().Output, "context_policy": snapshot.Agent.effectivePolicy().Context, "tools": tools, "events": events, "event_limit": 200})
}

// Tool arguments/results are private business data, fetched only on explicit expansion.
// Ordinary diagnostics retain summaries only.
func (s *Server) toolDetails(w http.ResponseWriter, r *http.Request) {
	var call ToolCall
	var arguments, result string
	err := s.store.db.QueryRow("SELECT call_id,name,status,arguments,result FROM tool_calls WHERE run_id=? AND call_id=?", r.PathValue("id"), r.PathValue("call")).Scan(&call.ID, &call.Name, &call.Status, &arguments, &result)
	if err == sql.ErrNoRows {
		fail(w, 404, "工具调用不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]any{"call": call, "arguments": arguments, "result": result})
}

func toolDisplayName(snapshot runConfig, name string) string {
	if name == "skill_resource" {
		return "技能资源读取"
	}
	if snapshot.Capabilities != nil {
		for _, server := range snapshot.Capabilities.MCPs {
			for _, tool := range server.Tools {
				digest := sha256.Sum256([]byte(tool.Name))
				if name == fmt.Sprintf("mcp_%s_%x", server.ID, digest[:8]) {
					return server.Name + " / " + tool.Name
				}
			}
		}
	}
	return name
}
