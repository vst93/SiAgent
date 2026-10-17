package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"siagent/internal/knowledge"
)

// mcpAuth accepts any valid key that can read knowledge or run an Agent.
func (s *Server) mcpAuth(next func(http.ResponseWriter, *http.Request, principal)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			fail(w, 401, "缺少 MCP 密钥。")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		var value principal
		var scopes string
		var expires int64
		var revoked int
		var limit int
		err := s.store.db.QueryRow("SELECT id,scopes,agent_id,knowledge_id,rate_limit,expires,revoked FROM api_keys WHERE hash=?", apiKeyHash(token)).Scan(&value.ID, &scopes, &value.AgentID, &value.KnowledgeID, &limit, &expires, &revoked)
		if err != nil {
			fail(w, 401, "MCP 密钥无效。")
			return
		}
		if revoked == 1 || (expires > 0 && time.Now().Unix() > expires) {
			fail(w, 401, "MCP 密钥已吊销或过期。")
			return
		}
		value.Scopes = map[string]bool{}
		for _, item := range strings.Split(scopes, ",") {
			item = strings.Trim(strings.TrimSpace(item), `"[]`)
			if item != "" {
				value.Scopes[item] = true
			}
		}
		if !value.Scopes[scopeKnowledgeRead] && !value.Scopes[scopeAgentRun] {
			fail(w, 403, "密钥缺少知识检索或 Agent 运行作用域。")
			return
		}
		value.RateLimit = limit
		if !s.allow(value.ID, limit) {
			fail(w, 429, "请求过于频繁，请稍后重试。")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, value)), value)
	})
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any) {
	respond(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	respond(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{Code: code, Message: message}})
}

// mcpHandler implements the subset of MCP needed for agent-to-agent use:
// initialize, tools/list and tools/call over HTTP JSON-RPC.
func (s *Server) mcpHandler(w http.ResponseWriter, r *http.Request, p principal) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(w, 405, "MCP 端点只接受 POST。")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeRPCError(w, nil, -32700, "无法读取请求。")
		return
	}
	var request rpcRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.JSONRPC != "2.0" {
		writeRPCError(w, nil, -32600, "JSON-RPC 请求无效。")
		return
	}
	switch request.Method {
	case "initialize":
		writeRPC(w, request.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "siagent", "version": "1"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		writeRPC(w, request.ID, map[string]any{"tools": mcpTools()})
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &call); err != nil {
			writeRPCError(w, request.ID, -32602, "工具参数无效。")
			return
		}
		result, err := s.callMCPTool(r.Context(), p, call.Name, call.Arguments)
		if err != nil {
			writeRPCError(w, request.ID, -32000, err.Error())
			return
		}
		writeRPC(w, request.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": result}}, "isError": false})
	default:
		writeRPCError(w, request.ID, -32601, "不支持的方法。")
	}
}

func mcpTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "knowledge_search",
			"description": "在授权范围内检索知识库，返回真实片段与来源。",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 5}}, "required": []string{"query"}},
		},
		{
			"name":        "knowledge_read",
			"description": "读取来源片段及分页原文。",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"source_id": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"}}, "required": []string{"source_id"}},
		},
		{
			"name":        "agent_chat",
			"description": "向 Agent 提一个问题并等待回答。",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}, "agent_id": map[string]any{"type": "string"}}, "required": []string{"input"}},
		},
	}
}

func (s *Server) callMCPTool(ctx context.Context, p principal, name string, raw json.RawMessage) (string, error) {
	switch name {
	case "knowledge_search":
		var input struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(raw, &input); err != nil || strings.TrimSpace(input.Query) == "" {
			return "", errors.New("query 必填")
		}
		if input.Limit < 1 || input.Limit > 5 {
			input.Limit = 3
		}
		scope := knowledge.Scope{KnowledgeIDs: []string{}}
		if p.KnowledgeID != "" {
			scope.KnowledgeIDs = []string{p.KnowledgeID}
		} else {
			bases, err := s.knowledge.Scope(ctx, false)
			if err != nil {
				return "", err
			}
			scope = bases
		}
		result, err := s.knowledge.Search(ctx, scope, knowledge.SearchRequest{Query: input.Query, Limit: input.Limit, Compact: true})
		if err != nil {
			return "", err
		}
		out, _ := json.Marshal(result)
		return string(out), nil
	case "knowledge_read":
		var input struct {
			SourceID string `json:"source_id"`
			Offset   int    `json:"offset"`
			Limit    int    `json:"limit"`
		}
		if err := json.Unmarshal(raw, &input); err != nil || input.SourceID == "" {
			return "", errors.New("source_id 必填")
		}
		if input.Limit < 1 || input.Limit > 4000 {
			input.Limit = 2000
		}
		scope := knowledge.Scope{KnowledgeIDs: []string{}}
		if p.KnowledgeID != "" {
			scope.KnowledgeIDs = []string{p.KnowledgeID}
		} else {
			bases, err := s.knowledge.Scope(ctx, true)
			if err != nil {
				return "", err
			}
			scope = bases
		}
		value, err := s.knowledge.Read(ctx, scope, knowledge.ReadRequest{SourceID: input.SourceID, Offset: input.Offset, Limit: input.Limit})
		if err != nil {
			return "", err
		}
		out, _ := json.Marshal(value)
		return string(out), nil
	case "agent_chat":
		var input struct {
			Input   string `json:"input"`
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(raw, &input); err != nil || strings.TrimSpace(input.Input) == "" {
			return "", errors.New("input 必填")
		}
		agentID := input.AgentID
		if agentID == "" {
			agentID = p.AgentID
		}
		if agentID == "" {
			agentID = "default"
		}
		if !p.authorizeAgent(agentID) {
			return "", errors.New("密钥无权访问该 Agent")
		}
		view, err := s.syncAgentRun(ctx, p, agentID, input.Input, 60*time.Second)
		if err != nil {
			return "", err
		}
		out, _ := json.Marshal(view)
		return string(out), nil
	default:
		return "", errors.New("未知工具")
	}
}

// syncAgentRun creates a run and waits for a terminal state, bounded by wait.
func (s *Server) syncAgentRun(ctx context.Context, p principal, agentID, input string, wait time.Duration) (runView, error) {
	s.mu.Lock()
	conversationID, err := s.integrationConversation(p, agentID, "")
	if err != nil {
		s.mu.Unlock()
		return runView{}, err
	}
	run, err := s.enqueueRun(ctx, conversationID, input, 0, nil)
	s.mu.Unlock()
	if err != nil {
		return runView{}, err
	}
	deadline := time.Now().Add(wait)
	for {
		view, err := s.loadRunView(run.ID)
		if err != nil {
			return view, err
		}
		if terminal(view.Status) {
			return view, nil
		}
		if time.Now().After(deadline) {
			return view, errors.New("任务仍在执行，请改用 /v1 异步接口")
		}
		select {
		case <-ctx.Done():
			return view, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// openAICompat maps a chat completion request onto one Agent run.
func (s *Server) openAICompat(w http.ResponseWriter, r *http.Request, p principal) {
	var input struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Stream {
		fail(w, 400, "兼容层暂不支持流式输出，请使用 /v1/runs 的 SSE。")
		return
	}
	agentID := strings.TrimPrefix(input.Model, "agent:")
	if agentID == "" {
		agentID = "default"
	}
	if !p.authorizeAgent(agentID) {
		fail(w, 403, "密钥无权访问该 Agent。")
		return
	}
	text := ""
	for index := len(input.Messages) - 1; index >= 0; index-- {
		if input.Messages[index].Role != "user" {
			continue
		}
		var value string
		if err := json.Unmarshal(input.Messages[index].Content, &value); err != nil {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(input.Messages[index].Content, &parts); err == nil {
				for _, part := range parts {
					if part.Type == "text" {
						value += part.Text
					}
				}
			}
		}
		text = strings.TrimSpace(value)
		break
	}
	if text == "" {
		fail(w, 400, "请提供一条 user 消息。")
		return
	}
	view, err := s.syncAgentRun(r.Context(), p, agentID, text, 60*time.Second)
	if err != nil {
		fail(w, 504, err.Error())
		return
	}
	respond(w, 200, map[string]any{
		"id":      "chatcmpl-" + view.RunID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   input.Model,
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": view.Answer},
		}},
	})
}
