package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"siagent/internal/knowledge"
)

func TestParseRerankOrderAcceptsCommonShapes(t *testing.T) {
	order, err := parseRerankOrder("[2,0,1]", 3)
	if err != nil || len(order) != 3 || order[0] != 2 {
		t.Fatalf("plain array: %v %v", order, err)
	}
	order, err = parseRerankOrder("```json\n[1, 0]\n```", 3)
	if err != nil || order[0] != 1 {
		t.Fatalf("fenced array: %v %v", order, err)
	}
	order, err = parseRerankOrder("排序：2, 1, 0", 3)
	if err != nil || len(order) != 3 {
		t.Fatalf("loose numbers: %v %v", order, err)
	}
	if _, err := parseRerankOrder("[9,9]", 3); err == nil {
		t.Fatal("out-of-range indices should be rejected")
	}
	if _, err := parseRerankOrder("[1,1]", 3); err == nil {
		t.Fatal("a single distinct index should be rejected")
	}
	if _, err := parseRerankOrder("not json", 3); err == nil {
		t.Fatal("garbage should be rejected")
	}
}

func TestModelRerankerUsesChatCompletionsAndValidatesOrder(t *testing.T) {
	var seenPath, seenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenAuth = r.Header.Get("Authorization")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["temperature"] != float64(0) {
			t.Errorf("rerank should be deterministic")
		}
		w.Header().Set("Content-Type", "application/json")
		io := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "[1,0]"}}}}
		json.NewEncoder(w).Encode(io)
	}))
	defer server.Close()
	store := newFixture(t).store
	if _, err := store.db.Exec("INSERT INTO models(base_url,name,api_key,version) VALUES(?,?,?,1)", server.URL, "model", store.encrypt("secret")); err != nil {
		t.Fatal(err)
	}
	reranker := &modelReranker{store: store}
	ids, err := reranker.Rerank(t.Context(), "query", []knowledge.Hit{{SourceID: "a", Title: "A", Text: "first"}, {SourceID: "b", Title: "B", Text: "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "b" {
		t.Fatalf("unexpected order: %v", ids)
	}
	if seenPath != "/chat/completions" || seenAuth != "Bearer secret" {
		t.Fatalf("unexpected request: %s %s", seenPath, seenAuth)
	}
}

func TestModelRerankerFailsOnNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()
	store := newFixture(t).store
	if _, err := store.db.Exec("INSERT INTO models(base_url,name,api_key,version) VALUES(?,?,?,1)", server.URL, "model", store.encrypt("secret")); err != nil {
		t.Fatal(err)
	}
	reranker := &modelReranker{store: store}
	if _, err := reranker.Rerank(t.Context(), "query", []knowledge.Hit{{SourceID: "a"}, {SourceID: "b"}}); err == nil {
		t.Fatal("expected upstream failure")
	}
}
