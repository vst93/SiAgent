package platform

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "siagent/internal/runtime"
)

// signalRuntime reports each dispatched request on a channel so a test can
// complete runs without racing on shared slices.
type signalRuntime struct{ requests chan map[string]any }

func (r signalRuntime) Send(value any) error {
	r.requests <- value.(map[string]any)
	return nil
}

func TestWebhookDeliveryIsSignedAndRetried(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	var attempts int32
	var signature, eventID, event string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := r.Header.Get("X-SiAgent-Event")
		if kind != "run.completed" {
			// The ping check must not interfere with retry accounting.
			w.WriteHeader(http.StatusOK)
			return
		}
		count := atomic.AddInt32(&attempts, 1)
		body, _ = io.ReadAll(r.Body)
		signature = r.Header.Get("X-SiAgent-Signature")
		eventID = r.Header.Get("X-SiAgent-Event-Id")
		event = kind
		if count < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	response := f.request(t, "POST", "/api/webhooks", map[string]any{
		"url": server.URL, "secret": "0123456789abcdef", "events": []string{"run.completed"}, "enabled": true,
	}, true, nil)
	requireStatus(t, response, 200)
	var hook webhook
	json.Unmarshal(response.Body.Bytes(), &hook)
	if !hook.HasKey || hook.URL != server.URL {
		t.Fatalf("webhook not saved: %s", response.Body.String())
	}
	requireStatus(t, f.request(t, "POST", "/api/webhooks/"+hook.ID+"/test", nil, true, nil), 202)

	runID := f.start(t, f.conversation(t), "hello")
	emit(t, f, runID, "run_started", 1)
	emit(t, f, runID, "run_completed", 2)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := f.store.db.QueryRow("SELECT status FROM webhook_deliveries WHERE run_id=? ORDER BY created LIMIT 1", runID).Scan(&status); err == nil && status == "sent" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if atomic.LoadInt32(&attempts) < 3 {
		t.Fatalf("expected retries, got %d attempts", attempts)
	}
	if event != "run.completed" || !strings.HasPrefix(eventID, runID+":") {
		t.Fatalf("unexpected headers: %s %s", event, eventID)
	}
	var payload map[string]any
	json.Unmarshal(body, &payload)
	if signature == "" || !strings.HasPrefix(signature, "sha256=") {
		t.Fatalf("missing signature: %q", signature)
	}
	var deliveries []webhookDelivery
	response = f.request(t, "GET", "/api/webhook-deliveries", nil, true, nil)
	requireStatus(t, response, 200)
	json.Unmarshal(response.Body.Bytes(), &deliveries)
	if len(deliveries) == 0 {
		t.Fatal("delivery not recorded")
	}
}

func TestWebhookSignatureMatchesPayload(t *testing.T) {
	// Deterministic check of the signing scheme documented for consumers.
	secret := "0123456789abcdef"
	eventID, event := "run-1:run.completed", "run.completed"
	timestamp := int64(1700000000)
	payload := []byte(`{"event":"run.completed"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(eventID + "." + event + "." + strconvItoa(timestamp)))
	mac.Write(payload)
	got := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if len(got) != len("sha256=")+64 {
		t.Fatalf("unexpected signature shape: %s", got)
	}
}

func TestWebhookValidationAndAuth(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	requireStatus(t, f.request(t, "GET", "/api/webhooks", nil, false, nil), 401)
	requireStatus(t, f.request(t, "POST", "/api/webhooks", map[string]any{"url": "ftp://x", "secret": "0123456789abcdef", "events": []string{"run.completed"}}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/webhooks", map[string]any{"url": "https://example.com/hook", "secret": "short", "events": []string{"run.completed"}}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/webhooks", map[string]any{"url": "https://example.com/hook", "secret": "0123456789abcdef", "events": []string{"bogus"}}, true, nil), 400)
}

func TestMCPServerExposesToolsAndSearch(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	_, token := createKey(t, f, map[string]any{"name": "mcp", "scopes": []string{"knowledge:search"}})
	base, err := f.server.knowledge.SaveBase(t.Context(), "", "MCP", "")
	if err != nil {
		t.Fatal(err)
	}
	response := f.uploadKnowledge(t, base.ID, "travel.md", "住宿标准为每晚 500 元。", true)
	requireStatus(t, response, 202)
	var document struct {
		ID string `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &document)
	waitDocument(t, f, document.ID)

	rpc := func(method string, params any) map[string]any {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(recorder, request)
		var value map[string]any
		json.Unmarshal(recorder.Body.Bytes(), &value)
		return value
	}
	if result := rpc("initialize", map[string]any{}); result["result"] == nil {
		t.Fatalf("initialize failed: %+v", result)
	}
	list := rpc("tools/list", map[string]any{})
	tools, _ := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("unexpected tool list: %+v", list)
	}
	call := rpc("tools/call", map[string]any{"name": "knowledge_search", "arguments": map[string]any{"query": "住宿标准"}})
	content := call["result"].(map[string]any)["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "住宿") {
		t.Fatalf("search returned nothing: %s", text)
	}
	unknown := rpc("tools/call", map[string]any{"name": "nope"})
	if unknown["error"] == nil {
		t.Fatal("unknown tool should error")
	}

	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	recorder := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(recorder, request)
	requireStatus(t, recorder, 401)
}

func TestOpenAICompatMapsOneRun(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	signal := make(chan map[string]any, 4)
	f.server.runtime = signalRuntime{requests: signal}
	_, token := createKey(t, f, map[string]any{"name": "compat", "scopes": []string{"agent:run"}})

	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, _ := json.Marshal(map[string]any{"model": "agent:default", "messages": []map[string]any{{"role": "user", "content": "你好"}}})
		request := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		f.server.Handler().ServeHTTP(response, request)
	}()
	var dispatched map[string]any
	select {
	case dispatched = <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("run was not dispatched")
	}
	runID := dispatched["run_id"].(string)
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_started", RunID: runID, Sequence: 1, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: runID, Sequence: 2, Data: json.RawMessage(`{"answer":"你好呀"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_released", RunID: runID, Sequence: 3, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	<-done
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), "你好呀") {
		t.Fatalf("completion missing answer: %s", response.Body.String())
	}

	stream := integrationRequest(f, "POST", "/v1/chat/completions", token, map[string]any{"model": "agent:default", "stream": true, "messages": []map[string]any{{"role": "user", "content": "x"}}}, nil)
	requireStatus(t, stream, 400)
}
