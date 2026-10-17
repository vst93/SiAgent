package platform

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentruntime "siagent/internal/runtime"
)

func createKey(t *testing.T, f *fixture, body map[string]any) (string, string) {
	t.Helper()
	response := f.request(t, "POST", "/api/api-keys", body, true, nil)
	requireStatus(t, response, 201)
	var value struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	json.Unmarshal(response.Body.Bytes(), &value)
	if value.Key == "" || !strings.HasPrefix(value.Key, "siagent_") {
		t.Fatalf("token not returned once: %s", response.Body.String())
	}
	return value.ID, value.Key
}

func integrationRequest(f *fixture, method, path, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	return response
}

func TestIntegrationKeyLifecycleAndScopes(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	id, token := createKey(t, f, map[string]any{"name": "crm", "scopes": []string{"agent:run"}})

	response := f.request(t, "GET", "/api/api-keys", nil, true, nil)
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), token) {
		t.Fatal("list must not expose the secret")
	}
	requireStatus(t, integrationRequest(f, "GET", "/v1/knowledge/bases", "", nil, nil), 401)
	requireStatus(t, integrationRequest(f, "GET", "/v1/knowledge/bases", token, nil, nil), 403)

	// Agent run scope works; read scope is still missing.
	conversation := f.conversation(t)
	response = integrationRequest(f, "POST", "/v1/agents/default/runs", token, map[string]string{"input": "hello"}, nil)
	requireStatus(t, response, 202)
	var created struct {
		RunID string `json:"run_id"`
	}
	json.Unmarshal(response.Body.Bytes(), &created)
	requireStatus(t, integrationRequest(f, "GET", "/v1/runs/"+created.RunID, token, nil, nil), 403)

	requireStatus(t, f.request(t, "DELETE", "/api/api-keys/"+id, nil, true, nil), 200)
	requireStatus(t, integrationRequest(f, "POST", "/v1/agents/default/runs", token, map[string]string{"input": "again"}, nil), 401)
	_ = conversation
}

func TestIntegrationRunIdempotencyRevisionAndCancel(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	_, token := createKey(t, f, map[string]any{"name": "crm", "scopes": []string{"agent:run", "agent:read"}})

	first := integrationRequest(f, "POST", "/v1/agents/default/runs", token, map[string]string{"input": "hello"}, map[string]string{"Idempotency-Key": "abc"})
	requireStatus(t, first, 202)
	var created struct {
		RunID string `json:"run_id"`
	}
	json.Unmarshal(first.Body.Bytes(), &created)

	again := integrationRequest(f, "POST", "/v1/agents/default/runs", token, map[string]string{"input": "hello"}, map[string]string{"Idempotency-Key": "abc"})
	requireStatus(t, again, 200)
	var repeated struct {
		RunID string `json:"run_id"`
	}
	json.Unmarshal(again.Body.Bytes(), &repeated)
	if repeated.RunID != created.RunID {
		t.Fatal("idempotency key created a second run")
	}
	requireStatus(t, integrationRequest(f, "POST", "/v1/agents/default/runs", token, map[string]string{"input": "different"}, map[string]string{"Idempotency-Key": "abc"}), 409)

	requireStatus(t, integrationRequest(f, "POST", "/v1/agents/default/runs", token, map[string]any{"input": "hello", "agent_revision": 999}, nil), 409)

	// The run may already be dispatched, in which case cancellation is confirmed
	// asynchronously.
	cancel := integrationRequest(f, "POST", "/v1/runs/"+created.RunID+"/cancel", token, nil, nil)
	if cancel.Code != 200 && cancel.Code != 202 {
		t.Fatalf("unexpected cancel status: %d %s", cancel.Code, cancel.Body.String())
	}
	view := integrationRequest(f, "GET", "/v1/runs/"+created.RunID, token, nil, nil)
	requireStatus(t, view, 200)
	if !strings.Contains(view.Body.String(), `"status":"cancelled"`) && !strings.Contains(view.Body.String(), `"status":"cancelling"`) {
		t.Fatalf("run not cancelled: %s", view.Body.String())
	}
}

func TestIntegrationKnowledgePushSearchAndDelete(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	_, token := createKey(t, f, map[string]any{"name": "kb", "scopes": []string{"knowledge:write", "knowledge:search"}})
	base, err := f.server.knowledge.SaveBase(t.Context(), "", "API", "")
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "travel.md")
	part.Write([]byte("# 差旅\n\n住宿标准为每晚 500 元。\n"))
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/v1/knowledge/"+base.ID+"/documents?external_id=doc-1", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	requireStatus(t, response, 202)
	var uploaded struct {
		DocumentID string `json:"document_id"`
	}
	json.Unmarshal(response.Body.Bytes(), &uploaded)
	if waitDocument(t, f, uploaded.DocumentID).State != "ready" {
		t.Fatal("document did not index")
	}

	// Re-pushing the same external id creates a new version of the same document.
	body.Reset()
	writer = multipart.NewWriter(&body)
	part, _ = writer.CreateFormFile("file", "travel.md")
	part.Write([]byte("# 差旅\n\n住宿标准为每晚 600 元。\n"))
	writer.Close()
	request = httptest.NewRequest(http.MethodPost, "http://localhost/v1/knowledge/"+base.ID+"/documents?external_id=doc-1", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	requireStatus(t, response, 202)
	var replaced struct {
		DocumentID string `json:"document_id"`
	}
	json.Unmarshal(response.Body.Bytes(), &replaced)
	if replaced.DocumentID != uploaded.DocumentID {
		t.Fatal("external id did not map to the same document")
	}

	search := integrationRequest(f, "POST", "/v1/knowledge/"+base.ID+"/search", token, map[string]any{"query": "住宿标准", "limit": 3}, nil)
	requireStatus(t, search, 200)
	if !strings.Contains(search.Body.String(), "住宿") {
		t.Fatalf("search returned no excerpt: %s", search.Body.String())
	}
	requireStatus(t, integrationRequest(f, "GET", "/v1/knowledge/documents/"+uploaded.DocumentID, token, nil, nil), 200)
	requireStatus(t, integrationRequest(f, "DELETE", "/v1/knowledge/documents/"+uploaded.DocumentID, token, nil, nil), 200)
}

func TestIntegrationRateLimitAndScopedAgent(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	other := f.request(t, "POST", "/api/agents", map[string]any{"name": "other", "instructions": "x", "enabled": true, "model_id": 1}, true, nil)
	requireStatus(t, other, 201)
	var agent Agent
	json.Unmarshal(other.Body.Bytes(), &agent)

	_, token := createKey(t, f, map[string]any{"name": "limited", "scopes": []string{"agent:run"}, "rate_limit": 1})
	requireStatus(t, integrationRequest(f, "POST", "/v1/agents/default/runs", token, map[string]string{"input": "a"}, nil), 202)
	requireStatus(t, integrationRequest(f, "POST", "/v1/agents/default/runs", token, map[string]string{"input": "b"}, nil), 429)

	_, scoped := createKey(t, f, map[string]any{"name": "scoped", "scopes": []string{"agent:run"}, "agent_id": agent.ID})
	requireStatus(t, integrationRequest(f, "POST", "/v1/agents/default/runs", scoped, map[string]string{"input": "a"}, nil), 403)
	requireStatus(t, integrationRequest(f, "POST", "/v1/agents/"+agent.ID+"/runs", scoped, map[string]string{"input": "a"}, nil), 202)
}

func TestIntegrationSSEStreamsStateAndDelta(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	_, token := createKey(t, f, map[string]any{"name": "sse", "scopes": []string{"agent:run", "agent:read"}})
	response := integrationRequest(f, "POST", "/v1/agents/default/runs", token, map[string]string{"input": "hello"}, nil)
	requireStatus(t, response, 202)
	var created struct {
		RunID string `json:"run_id"`
	}
	json.Unmarshal(response.Body.Bytes(), &created)

	request := httptest.NewRequest(http.MethodGet, "http://localhost/v1/runs/"+created.RunID+"/events", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	stream := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		f.server.Handler().ServeHTTP(stream, request)
		close(done)
	}()
	// Complete the run so the stream settles.
	emit(t, f, created.RunID, "run_started", 1)
	if err := f.server.HandleEvent(agentruntime.Event{Type: "message_delta", RunID: created.RunID, Sequence: 2, Data: json.RawMessage(`{"text":"hi"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: created.RunID, Sequence: 3, Data: json.RawMessage(`{"answer":"hi"}`)}); err != nil {
		t.Fatal(err)
	}
	emit(t, f, created.RunID, "run_released", 4)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE stream did not settle")
	}
	output := stream.Body.String()
	if !strings.Contains(output, "event: run.state") || !strings.Contains(output, "event: run.settled") {
		t.Fatalf("unexpected stream: %s", output)
	}
	if !strings.Contains(output, `"answer":"hi"`) {
		t.Fatalf("answer missing from settled event: %s", output)
	}
}
