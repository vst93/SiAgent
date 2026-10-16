package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"siagent/internal/knowledge"
	agentruntime "siagent/internal/runtime"
)

func (f *fixture) uploadKnowledge(t *testing.T, baseID, name, text string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte(text))
	writer.Close()
	request := httptest.NewRequest("POST", "http://localhost/api/knowledge/bases/"+baseID+"/documents", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if authenticated {
		request.AddCookie(f.cookie)
		request.Header.Set("X-CSRF-Token", f.csrf)
	}
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	return response
}
func knowledgeWorker(t *testing.T, f *fixture) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.server.RunKnowledge(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		f.server.StopKnowledge()
	})
}
func waitDocument(t *testing.T, f *fixture, id string) knowledge.Document {
	t.Helper()
	// Cold dictionary initialization is substantially slower with race instrumentation.
	// Allow the supported 15-second parser budget plus startup/indexing overhead.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var last knowledge.Document
	for {
		document, err := f.server.knowledge.Document(ctx, id)
		if err != nil {
			t.Fatalf("document lookup failed (last state %s): %v", last.State, err)
		}
		last = document
		if last.State == "ready" || last.State == "failed" {
			return last
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("document did not finish processing: state=%s error=%s", last.State, last.Error)
		}
	}
}

func TestKnowledgeManagementAuthAndNativeRetrieval(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	requireStatus(t, f.request(t, "GET", "/api/knowledge/bases", nil, false, nil), 401)
	response := f.request(t, "POST", "/api/knowledge/bases", map[string]string{"name": "Invoices", "description": "Native knowledge"}, true, nil)
	requireStatus(t, response, 201)
	var base knowledge.Base
	json.Unmarshal(response.Body.Bytes(), &base)
	requireStatus(t, f.uploadKnowledge(t, base.ID, "invoice.md", "发票开具流程：七个工作日内办理。", false), 401)
	response = f.uploadKnowledge(t, base.ID, "invoice.md", "# 发票开具流程\n\n请提供订单编号，七个工作日内办理发票。", true)
	requireStatus(t, response, 202)
	var document knowledge.Document
	json.Unmarshal(response.Body.Bytes(), &document)
	if waitDocument(t, f, document.ID).State != "ready" {
		t.Fatal("document did not index")
	}
	// Native management and retrieval work before a model or MCP is configured.
	response = f.request(t, "POST", "/api/knowledge/search", map[string]any{"query": "发票开具", "knowledge_ids": []string{base.ID}}, true, nil)
	requireStatus(t, response, 200)
	var result knowledge.SearchResult
	json.Unmarshal(response.Body.Bytes(), &result)
	if len(result.Hits) == 0 {
		t.Fatal("native search returned no excerpt")
	}
	sourcePath := "/api/knowledge/sources/" + result.Hits[0].SourceID
	requireStatus(t, f.request(t, "GET", sourcePath, nil, false, nil), 401)
	requireStatus(t, f.request(t, "GET", sourcePath, nil, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/knowledge/bases/"+base.ID, map[string]string{"name": "Updated", "description": ""}, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/agent/knowledge", map[string]any{"knowledge_ids": []string{base.ID}, "confirm": true}, true, nil), 409)
	f.model(t, "fixture", "model-secret")
	requireStatus(t, f.request(t, "PUT", "/api/agent/knowledge", map[string]any{"knowledge_ids": []string{base.ID}}, true, nil), 400)
	requireStatus(t, f.request(t, "PUT", "/api/agent/knowledge", map[string]any{"knowledge_ids": []string{base.ID}, "confirm": true}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/knowledge/documents/"+document.ID, nil, true, nil), 200)
	response = f.request(t, "GET", sourcePath, nil, true, nil)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), `"deleted":true`) || strings.Contains(response.Body.String(), "七个工作日") {
		t.Fatal("deleted source leaked text or lost tombstone")
	}
	response = f.request(t, "POST", "/api/knowledge/search", map[string]any{"query": "发票", "knowledge_ids": []string{base.ID}}, true, nil)
	requireStatus(t, response, 200)
	json.Unmarshal(response.Body.Bytes(), &result)
	if len(result.Hits) != 0 {
		t.Fatal("deleted document stayed in retrieval")
	}
}

type knowledgeReplyRuntime struct{ responses chan map[string]any }

func (r knowledgeReplyRuntime) Send(value any) error {
	r.responses <- value.(map[string]any)
	return nil
}
func TestKnowledgeRPCUsesFrozenScopeAndPersistsActualSources(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	knowledgeWorker(t, f)
	allowed, err := f.server.knowledge.SaveBase(context.Background(), "", "Allowed", "")
	if err != nil {
		t.Fatal(err)
	}
	private, err := f.server.knowledge.SaveBase(context.Background(), "", "Private", "")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := f.server.knowledge.Upload(context.Background(), allowed.ID, "", "allowed.txt", strings.NewReader("Invoice policy seven business days."))
	if err != nil {
		t.Fatal(err)
	}
	waitDocument(t, f, doc.ID)
	privateDoc, err := f.server.knowledge.Upload(context.Background(), private.ID, "", "secret.txt", strings.NewReader("Private invoice secret details."))
	if err != nil {
		t.Fatal(err)
	}
	waitDocument(t, f, privateDoc.ID)
	requireStatus(t, f.request(t, "PUT", "/api/agent/knowledge", map[string]any{"knowledge_ids": []string{allowed.ID}, "confirm": true}, true, nil), 200)
	conversation := f.conversation(t)
	runID := f.start(t, conversation, "Find invoice policy")
	ids := f.runtime.requests[0]["knowledge_ids"].([]string)
	if len(ids) != 1 || ids[0] != allowed.ID {
		t.Fatal("run did not freeze knowledge bindings")
	}
	// Later edits do not expand the already admitted run.
	requireStatus(t, f.request(t, "PUT", "/api/agent/knowledge", map[string]any{"knowledge_ids": []string{private.ID}, "confirm": true}, true, nil), 200)
	replies := make(chan map[string]any, 8)
	f.server.runtime = knowledgeReplyRuntime{replies}
	rpc := func(requestID, operation string, input any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"operation": operation, "input": input})
		if err := f.server.HandleEvent(agentruntime.Event{Type: "knowledge_request", RequestID: requestID, RunID: runID, Data: raw}); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-replies:
			return result["data"].(map[string]any)
		case <-time.After(5 * time.Second):
			t.Fatal("knowledge RPC did not finish")
			return nil
		}
	}
	result := rpc("search-allowed", "search", map[string]any{"query": "invoice"})
	if result["ok"] != true {
		t.Fatal(result)
	}
	response := result["result"].(knowledge.SearchResult)
	if len(response.Hits) == 0 || response.Hits[0].KnowledgeID != allowed.ID {
		t.Fatal("RPC scope incorrect")
	}
	if data := rpc("search-private", "search", map[string]any{"query": "invoice", "knowledge_ids": []string{private.ID}}); data["ok"] != false {
		t.Fatal("RPC expanded binding scope")
	}
	privateHits, err := f.server.knowledge.Search(context.Background(), knowledge.Scope{KnowledgeIDs: []string{private.ID}}, knowledge.SearchRequest{Query: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if data := rpc("read-private", "read", map[string]any{"source_id": privateHits.Hits[0].SourceID}); data["ok"] != false {
		t.Fatal("RPC read unbound document")
	}
	snapshot, err := f.server.snapshot(conversation)
	if err != nil || len(snapshot.Sources) != len(response.Hits) {
		t.Fatal("real sources were not persisted")
	}
	for _, source := range snapshot.Sources {
		if source.Text != "" || source.RunID != runID || source.KnowledgeID != allowed.ID {
			t.Fatal("source summary leaked body or scope")
		}
	}
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: runID, Sequence: 1, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if data := rpc("late-request", "search", map[string]any{"query": "invoice"}); data["ok"] != false {
		t.Fatal("terminal run retained RPC access")
	}
}
func TestMultipartRejectsExtraFilesBeforeCreatingDocument(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	b, err := f.server.knowledge.SaveBase(context.Background(), "", "Files", "")
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, name := range []string{"one.txt", "two.txt"} {
		part, _ := writer.CreateFormFile("file", name)
		part.Write([]byte("content"))
	}
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/knowledge/bases/"+b.ID+"/documents", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", f.csrf)
	request.AddCookie(f.cookie)
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	requireStatus(t, response, 400)
	documents, err := f.server.knowledge.Documents(context.Background(), b.ID)
	if err != nil || len(documents) != 0 {
		t.Fatal("invalid multipart request committed a document")
	}
}

func TestUnboundKnowledgeDispatchesAnEmptyArray(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	f.start(t, f.conversation(t), "Ordinary chat")
	ids, ok := f.runtime.requests[0]["knowledge_ids"].([]string)
	if !ok || ids == nil || len(ids) != 0 {
		t.Fatal("unbound knowledge must dispatch an empty array, not null")
	}
}
