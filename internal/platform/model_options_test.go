package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelOptionsValidationLegacyPreservationAndFrozenQueue(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	options := map[string]any{"display_name": "主力模型", "api": "openai-responses", "context_window": 65536, "max_tokens": 2048, "reasoning": true, "thinking_level": "low", "temperature": 0.25, "top_p": 0.8}
	body := map[string]any{"base_url": "http://localhost:9999/v1", "name": "fixture", "options": options}
	requireStatus(t, f.request(t, "PUT", "/api/model", body, true, nil), 200)
	// Legacy callers can change a connection without resetting new settings or its key.
	requireStatus(t, f.request(t, "PUT", "/api/model", map[string]any{"base_url": "http://localhost:9999/v1", "name": "fixture"}, true, nil), 200)
	model, err := f.store.model()
	if err != nil || model.APIKey != "secret" || model.Options.API != "openai-responses" || model.Options.MaxTokens != 2048 {
		t.Fatal("legacy update lost options or key", err)
	}
	f.server.limits.Concurrent = 1
	first := f.start(t, f.conversation(t), "hold")
	f.start(t, f.conversation(t), "queued")
	options["api"] = "anthropic-messages"
	options["top_p"] = nil
	options["temperature"] = nil
	options["max_tokens"] = 4096
	requireStatus(t, f.request(t, "PUT", "/api/model", body, true, nil), 200)
	emit(t, f, first, "run_started", 1)
	emit(t, f, first, "run_completed", 2)
	emit(t, f, first, "run_released", 3)
	queued := f.runtime.requests[1]["model"].(map[string]any)["options"].(ModelOptions)
	if queued.API != "openai-responses" || queued.MaxTokens != 2048 || queued.TopP == nil || *queued.TopP != 0.8 {
		t.Fatal("queued model options changed")
	}
	for _, bad := range []map[string]any{{"api": "unsupported"}, {"context_window": 0}, {"max_tokens": 65536}, {"temperature": 3}, {"top_p": 0}, {"thinking_level": "arbitrary"}, {"reasoning": false}, {"max_tokens_field": "max_tokens"}, {"unknown": true}, {"api": "anthropic-messages", "max_tokens": 32}} {
		invalid := map[string]any{"base_url": "http://localhost:9999/v1", "name": "fixture", "options": bad}
		requireStatus(t, f.request(t, "PUT", "/api/model", invalid, true, nil), 400)
	}
}
func TestModelConnectionProbeUsesSelectedProtocol(t *testing.T) {
	for _, api := range []string{"openai-completions", "openai-responses", "anthropic-messages"} {
		t.Run(api, func(t *testing.T) {
			called := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "probe-model" || body["stream"] != false {
					t.Error("probe did not use saved model")
				}
				w.Header().Set("Content-Type", "application/json")
				switch api {
				case "openai-completions":
					if r.URL.Path != "/v1/chat/completions" || body["max_completion_tokens"] != float64(16) || r.Header.Get("Authorization") != "Bearer probe-key" {
						t.Error("wrong completions request")
					}
					w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
				case "openai-responses":
					if r.URL.Path != "/v1/responses" || body["max_output_tokens"] != float64(16) || body["input"] != "Reply with OK." || r.Header.Get("Authorization") != "Bearer probe-key" {
						t.Error("wrong responses request")
					}
					w.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"OK"}]}]}`))
				case "anthropic-messages":
					if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "probe-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
						t.Error("wrong anthropic request")
					}
					w.Write([]byte(`{"content":[{"type":"text","text":"OK"}]}`))
				}
			}))
			defer upstream.Close()
			f := newFixture(t)
			f.setup(t)
			opts := map[string]any{"api": api}
			if api == "openai-completions" {
				opts["max_tokens_field"] = "max_completion_tokens"
			}
			requireStatus(t, f.request(t, "PUT", "/api/model", map[string]any{"base_url": upstream.URL + "/v1", "name": "probe-model", "api_key": "probe-key", "options": opts}, true, nil), 200)
			requireStatus(t, f.request(t, "POST", "/api/models/1/test", nil, true, nil), 200)
			if !called {
				t.Fatal("probe skipped upstream")
			}
		})
	}
}
func TestVersionSevenModelOptionsMigration(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "prior-model", "prior-secret")
	downgradeChannelFixture(t, f.store)
	if _, err := f.store.db.Exec("ALTER TABLE models DROP COLUMN options; ALTER TABLE runs DROP COLUMN citation_valid; ALTER TABLE runs DROP COLUMN evidence_state;  DROP TABLE retrieval_traces; DROP TABLE knowledge_eval_runs; DROP TABLE knowledge_eval_questions; ALTER TABLE knowledge_chunks DROP COLUMN parent_id; ALTER TABLE knowledge_chunks DROP COLUMN block_type; ALTER TABLE knowledge_chunks DROP COLUMN section_path; ALTER TABLE knowledge_chunks DROP COLUMN clause; ALTER TABLE knowledge_chunks DROP COLUMN content_hash; DROP TABLE answer_feedback; DROP TABLE knowledge_gaps; ALTER TABLE models DROP COLUMN provider; DROP TABLE provider_packs; PRAGMA user_version=7;"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	model, err := upgraded.model()
	if err != nil || model.APIKey != "prior-secret" || model.Name != "prior-model" || model.Options.API != "openai-completions" || model.Options.ContextWindow != 32768 || model.Options.MaxTokens != 4096 {
		t.Fatal("migration changed model or credential", err)
	}
	if model.Provider != "openai" {
		t.Fatalf("legacy connection did not receive a provider: %q", model.Provider)
	}
}

func TestLegacyByteLimitNoLongerRestrictsModelContext(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "large", "key")
	limits := defaultLimits()
	limits.ContextBytes = 1024

	legacy, _ := json.Marshal(limits)
	if _, err := f.store.db.Exec("INSERT INTO settings VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config", string(legacy)); err != nil {
		t.Fatal(err)
	}
	restored, err := f.server.loadLimits()
	if err != nil || restored.ContextBytes != 0 {
		t.Fatal("saved legacy byte limit remained active", err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/limits", limits, true, nil), 200)
	if f.server.limits.ContextBytes != 0 {
		t.Fatal("legacy byte limit remained active")
	}
	requireStatus(t, f.request(t, "PUT", "/api/model", map[string]any{"base_url": "http://localhost:9999/v1", "name": "large", "options": map[string]any{"context_window": 131072, "max_tokens": 4096}}, true, nil), 200)
	conversation := f.conversation(t)
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+conversation+"/runs", map[string]any{"input": strings.Repeat("context ", 5000)}, true, nil), 202)
	if len(requestsOf(f, "start")) != 1 {
		t.Fatal("long input was still restricted by legacy bytes")
	}
}
