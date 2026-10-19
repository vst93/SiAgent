package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "siagent/internal/runtime"
)

type fakeRuntime struct {
	requests []map[string]any
	restarts int
}

func (f *fakeRuntime) Restart() error { f.restarts++; return nil }

func (f *fakeRuntime) Send(value any) error {
	f.requests = append(f.requests, value.(map[string]any))
	return nil
}

type fixture struct {
	server    *Server
	store     *Store
	runtime   *fakeRuntime
	directory string
	cookie    *http.Cookie
	csrf      string
}

func newFixture(t *testing.T, options ...ServerOptions) *fixture {
	t.Helper()
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runtime := &fakeRuntime{}
	server, err := New(store, runtime, func() bool { return true }, "", options...)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{server: server, store: store, runtime: runtime, directory: directory}
}
func (f *fixture) request(t *testing.T, method, path string, body any, authenticated bool, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(data))
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("Content-Type", "application/json")
	if authenticated && f.cookie != nil {
		r.AddCookie(f.cookie)
		r.Header.Set("X-CSRF-Token", f.csrf)
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	return w
}
func requireStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("want HTTP %d, got %d: %s", status, w.Code, w.Body.String())
	}
}
func (f *fixture) setup(t *testing.T) {
	t.Helper()
	w := f.request(t, "POST", "/api/auth/setup", map[string]string{"password": "a-long-test-password", "token": f.server.SetupToken()}, false, nil)
	requireStatus(t, w, 201)
	f.cookie = w.Result().Cookies()[0]
	var result struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	f.csrf = result.CSRF
}
func (f *fixture) model(t *testing.T, name, key string) {
	t.Helper()
	w := f.request(t, "PUT", "/api/model", map[string]string{"base_url": "http://localhost:9999/v1", "name": name, "api_key": key}, true, nil)
	requireStatus(t, w, 200)
}
func (f *fixture) conversation(t *testing.T) string {
	t.Helper()
	w := f.request(t, "POST", "/api/conversations", nil, true, nil)
	requireStatus(t, w, 201)
	var value Conversation
	json.Unmarshal(w.Body.Bytes(), &value)
	return value.ID
}
func (f *fixture) start(t *testing.T, conversation, input string) string {
	t.Helper()
	w := f.request(t, "POST", "/api/conversations/"+conversation+"/runs", map[string]string{"input": input}, true, nil)
	requireStatus(t, w, 202)
	var value Run
	json.Unmarshal(w.Body.Bytes(), &value)
	return value.ID
}

func TestAuthenticationAndCSRF(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/api/model", "/api/agent", "/api/diagnostics", "/api/limits", "/api/runs/missing", "/api/conversations", "/api/conversations/nope", "/api/conversations/nope/events"} {
		requireStatus(t, f.request(t, "GET", path, nil, false, nil), 401)
	}
	requireStatus(t, f.request(t, "POST", "/api/auth/setup", map[string]string{"password": "a-long-test-password", "token": "wrong"}, false, nil), 403)
	f.setup(t)
	if !f.cookie.HttpOnly || f.cookie.SameSite != http.SameSiteStrictMode || f.cookie.Secure {
		t.Fatal("invalid session cookie policy")
	}
	requireStatus(t, f.request(t, "POST", "/api/auth/setup", map[string]string{"password": "another-long-password"}, false, nil), 409)
	requireStatus(t, f.request(t, "POST", "/api/conversations", nil, true, map[string]string{"X-CSRF-Token": "wrong"}), 403)
	requireStatus(t, f.request(t, "POST", "/api/auth/login", map[string]string{"password": "a-long-test-password"}, false, map[string]string{"Origin": "https://attacker.invalid"}), 403)
	requireStatus(t, f.request(t, "POST", "/api/auth/login", map[string]string{"password": "incorrect"}, false, nil), 401)
	requireStatus(t, f.request(t, "POST", "/api/auth/login", map[string]string{"password": "a-long-test-password"}, false, nil), 200)
	f.server.loginFailures = 10
	f.server.loginWindow = time.Now()
	requireStatus(t, f.request(t, "POST", "/api/auth/login", map[string]string{"password": "a-long-test-password"}, false, nil), 429)
	var hash []byte
	if err := f.store.db.QueryRow("SELECT password_hash FROM admin").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(hash, []byte("$2a$12$")) || bytes.Contains(hash, []byte("a-long-test-password")) {
		t.Fatal("password was not slow-hashed")
	}
	requireStatus(t, f.request(t, "POST", "/api/auth/logout", nil, true, nil), 200)
	requireStatus(t, f.request(t, "GET", "/api/model", nil, true, nil), 401)
}

func TestSecureCookieAndOrigin(t *testing.T) {
	f := newFixture(t)
	s, err := New(f.store, f.runtime, func() bool { return true }, "https://agent.example")
	if err != nil {
		t.Fatal(err)
	}
	f.server = s
	w := f.request(t, "POST", "/api/auth/setup", map[string]string{"password": "a-long-test-password", "token": s.SetupToken()}, false, map[string]string{"Origin": "https://agent.example"})
	requireStatus(t, w, 201)
	if !w.Result().Cookies()[0].Secure {
		t.Fatal("HTTPS origin must enable Secure cookie")
	}
}

func TestModelEncryptionSnapshotEventsAndRecovery(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "first-model", "secret-model-key")
	w := f.request(t, "GET", "/api/model", nil, true, nil)
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "secret-model-key") || strings.Contains(w.Body.String(), "api_key") {
		t.Fatal("credential echoed to client")
	}
	var encrypted string
	if err := f.store.db.QueryRow("SELECT api_key FROM models").Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, "secret-model-key") {
		t.Fatal("credential saved as plaintext")
	}
	if info, err := os.Stat(filepath.Join(f.directory, "master.key")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("master key permissions")
	}
	conversation := f.conversation(t)
	run := f.start(t, conversation, "first input")
	request := f.runtime.requests[0]
	if request["model"].(map[string]any)["api_key"] != "secret-model-key" {
		t.Fatal("private runtime config missing")
	}
	f.model(t, "second-model", "changed-key")
	if request["model"].(map[string]any)["name"] != "first-model" {
		t.Fatal("running model snapshot changed")
	}

	for _, event := range []agentruntime.Event{
		{Type: "run_started", RunID: run, Sequence: 1},
		{Type: "message_delta", RunID: run, Sequence: 2, Data: json.RawMessage(`{"text":"partial"}`)},
		{Type: "message_delta", RunID: run, Sequence: 2, Data: json.RawMessage(`{"text":"partial"}`)},
		{Type: "run_completed", RunID: run, Sequence: 3, Data: json.RawMessage(`{"answer":"final answer"}`)},
		{Type: "run_released", RunID: run, Sequence: 4},
	} {
		if err := f.server.HandleEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := f.server.snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runs[0].Status != "completed" || len(snapshot.Messages) != 2 || snapshot.Messages[1].Content != "final answer" {
		t.Fatalf("invalid persisted result: %+v", snapshot)
	}
	run2 := f.start(t, conversation, "second input")
	if err := f.server.HandleEvent(agentruntime.Event{Type: "message_delta", RunID: run2, Sequence: 1, Data: json.RawMessage(`{"text":"preserved"}`)}); err != nil {
		t.Fatal(err)
	}
	// Reopen both database and platform service using the same key, like a full process restart.
	f.store.Close()
	reopened, err := Open(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server, err := New(reopened, f.runtime, func() bool { return true }, "")
	if err != nil {
		t.Fatal(err)
	}
	f.server = server
	f.store = reopened
	if server.SetupToken() != "" {
		t.Fatal("setup reopened after initialization")
	}
	snapshot, err = server.snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runs[1].Status != "interrupted" || snapshot.Messages[3].Content != "preserved" {
		t.Fatalf("recovery lost state: %+v", snapshot)
	}
	requireStatus(t, f.request(t, "GET", "/api/model", nil, true, nil), 200)
	model, err := reopened.model()
	if err != nil || model.APIKey != "changed-key" {
		t.Fatal("credential did not survive restart")
	}
	var config string
	if err := reopened.db.QueryRow("SELECT config FROM runs WHERE id=?", run).Scan(&config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, "secret-model-key") || strings.Contains(config, "changed-key") {
		t.Fatal("run snapshot leaked a credential")
	}
}

func TestModelConnectionErrorAndSSEAuthentication(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer bad-key" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"bad-key should not leak"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer provider.Close()
	for _, key := range []string{"bad-key", "good-key"} {
		requireStatus(t, f.request(t, "PUT", "/api/model", map[string]string{"base_url": provider.URL + "/v1", "name": "fixture", "api_key": key}, true, nil), 200)
		w := f.request(t, "POST", "/api/model/test", nil, true, nil)
		if key == "bad-key" {
			requireStatus(t, w, 502)
			if !strings.Contains(w.Body.String(), "密钥被拒绝") || strings.Contains(w.Body.String(), "bad-key") {
				t.Fatal("unsafe or unhelpful model error")
			}
		} else {
			requireStatus(t, w, 200)
		}
	}
	conversation := f.conversation(t)
	server := httptest.NewServer(f.server.Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/conversations/"+conversation+"/events", nil)
	request.AddCookie(f.cookie)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buffer := make([]byte, 256)
	n, err := response.Body.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buffer[:n]), "event: snapshot") || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("missing SSE snapshot")
	}
	cancel()
	if _, err := f.store.db.Exec("UPDATE sessions SET expires=0"); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "GET", "/api/conversations/"+conversation+"/events", nil, true, nil), 401)
}

func TestRefuseMissingKeyAndNewerSchema(t *testing.T) {
	f := newFixture(t)
	f.store.Close()
	if err := os.Remove(filepath.Join(f.directory, "master.key")); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(f.directory); err == nil {
		store.Close()
		t.Fatal("must refuse missing encryption key")
	}
	other := newFixture(t)
	if _, err := other.store.db.Exec("PRAGMA user_version=25"); err != nil {
		t.Fatal(err)
	}
	other.store.Close()
	if store, err := Open(other.directory); err == nil {
		store.Close()
		t.Fatal("must refuse newer database")
	}
}
