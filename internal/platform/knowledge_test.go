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
	// Cold dictionary initialization is substantially slower with race
	// instrumentation, and slower still when the whole platform suite runs in
	// parallel: the observed worst case exceeds 40 seconds. Allow a generous
	// budget so the wait tests completion, not machine speed.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: runID, Sequence: 1, Data: json.RawMessage(`{"answer":"依据来源 [1] 回答。"}`)}); err != nil {
		t.Fatal(err)
	}
	completed, err := f.server.snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Runs[0].EvidenceState != "used" || completed.Runs[0].CitationValid != 1 {
		t.Fatalf("run completion did not record evidence: %+v", completed.Runs[0])
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

func TestKnowledgeTraceAndEvaluationAPI(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	response := f.request(t, "POST", "/api/knowledge/bases", map[string]string{"name": "Eval"}, true, nil)
	requireStatus(t, response, 201)
	var base knowledge.Base
	json.Unmarshal(response.Body.Bytes(), &base)
	response = f.uploadKnowledge(t, base.ID, "travel.md", "出差住宿标准为每晚 500 元，超标部分自理。", true)
	requireStatus(t, response, 202)
	var document knowledge.Document
	json.Unmarshal(response.Body.Bytes(), &document)
	if waitDocument(t, f, document.ID).State != "ready" {
		t.Fatal("document did not index")
	}

	response = f.request(t, "POST", "/api/knowledge/search", map[string]any{"query": "住宿标准", "knowledge_ids": []string{base.ID}, "trace": true}, true, nil)
	requireStatus(t, response, 200)
	var result knowledge.SearchResult
	json.Unmarshal(response.Body.Bytes(), &result)
	if result.Trace == nil || len(result.Trace.Terms) == 0 || len(result.Trace.Stages) == 0 {
		t.Fatalf("management search did not return a trace: %s", response.Body.String())
	}
	if len(result.Trace.Selected) != len(result.Hits) {
		t.Fatal("trace selection diverged from returned hits")
	}

	// Trace stays optional so Agent-facing payloads are unchanged.
	response = f.request(t, "POST", "/api/knowledge/search", map[string]any{"query": "住宿", "knowledge_ids": []string{base.ID}}, true, nil)
	requireStatus(t, response, 200)
	result = knowledge.SearchResult{}
	json.Unmarshal(response.Body.Bytes(), &result)
	if result.Trace != nil {
		t.Fatal("trace should be opt-in")
	}

	requireStatus(t, f.request(t, "GET", "/api/knowledge/bases/"+base.ID+"/eval/questions", nil, true, nil), 200)
	response = f.request(t, "POST", "/api/knowledge/bases/"+base.ID+"/eval/questions", map[string]any{"question": "住宿标准是多少", "expected_document_ids": []string{document.ID}}, true, nil)
	requireStatus(t, response, 201)
	var question knowledge.EvalQuestion
	json.Unmarshal(response.Body.Bytes(), &question)
	if question.ID == "" || len(question.ExpectedDocumentIDs) != 1 {
		t.Fatalf("question not stored: %s", response.Body.String())
	}
	requireStatus(t, f.request(t, "POST", "/api/knowledge/bases/"+base.ID+"/eval/questions", map[string]any{"question": "越权", "expected_document_ids": []string{"missing"}}, true, nil), 400)

	response = f.request(t, "POST", "/api/knowledge/bases/"+base.ID+"/eval/run", nil, true, nil)
	requireStatus(t, response, 201)
	var run knowledge.EvalRun
	json.Unmarshal(response.Body.Bytes(), &run)
	if run.Metrics.Questions != 1 || run.Metrics.RecallAt5 != 1 {
		t.Fatalf("unexpected metrics: %s", response.Body.String())
	}
	if run.Metrics.BaselineRecallAt5 > run.Metrics.RecallAt5 || run.Metrics.BaselineMRR > run.Metrics.MRR {
		t.Fatalf("baseline should not beat the current pipeline: %+v", run.Metrics)
	}
	if !strings.Contains(response.Body.String(), `"baseline_recall_at_5"`) {
		t.Fatalf("baseline metrics missing from response: %s", response.Body.String())
	}
	requireStatus(t, f.request(t, "GET", "/api/knowledge/bases/"+base.ID+"/eval/runs", nil, true, nil), 200)
	requireStatus(t, f.request(t, "GET", "/api/knowledge/eval/runs/"+run.ID, nil, true, nil), 200)
	requireStatus(t, f.request(t, "GET", "/api/knowledge/eval/runs/missing", nil, true, nil), 404)
	requireStatus(t, f.request(t, "DELETE", "/api/knowledge/eval/questions/"+question.ID, nil, true, nil), 200)
	requireStatus(t, f.request(t, "POST", "/api/knowledge/bases/"+base.ID+"/eval/run", nil, true, nil), 400)
}

func TestBatchDeleteDocumentsDeletesSelectedAndReportsMissing(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	response := f.request(t, "POST", "/api/knowledge/bases", map[string]string{"name": "Batch", "description": ""}, true, nil)
	requireStatus(t, response, 201)
	var base knowledge.Base
	json.Unmarshal(response.Body.Bytes(), &base)
	ids := []string{}
	for _, name := range []string{"one.md", "two.md", "three.md"} {
		response = f.uploadKnowledge(t, base.ID, name, "# "+name+"\n\n批量删除测试内容。", true)
		requireStatus(t, response, 202)
		var document knowledge.Document
		json.Unmarshal(response.Body.Bytes(), &document)
		waitDocument(t, f, document.ID)
		ids = append(ids, document.ID)
	}
	requireStatus(t, f.request(t, "POST", "/api/knowledge/documents/batch-delete", map[string]any{"ids": ids, "confirm": false}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/knowledge/documents/batch-delete", map[string]any{"ids": []string{}, "confirm": true}, true, nil), 400)
	response = f.request(t, "POST", "/api/knowledge/documents/batch-delete", map[string]any{"ids": append(ids, "missing-document"), "confirm": true}, true, nil)
	requireStatus(t, response, 200)
	var result struct {
		Deleted []string `json:"deleted"`
		Failed  []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != len(ids) || len(result.Failed) != 1 || result.Failed[0].ID != "missing-document" {
		t.Fatalf("unexpected batch result: %s", response.Body.String())
	}
	// Cleaned-up deletions leave the list entirely; nothing stays searchable.
	response = f.request(t, "GET", "/api/knowledge/bases/"+base.ID+"/documents", nil, true, nil)
	requireStatus(t, response, 200)
	var documents []knowledge.Document
	json.Unmarshal(response.Body.Bytes(), &documents)
	if len(documents) != 0 {
		t.Fatalf("deleted documents still listed: %s", response.Body.String())
	}
	for _, id := range ids {
		document, err := f.server.knowledge.Document(t.Context(), id)
		if err != nil || !document.Deleted {
			t.Fatalf("document %s not marked deleted: %+v %v", id, document, err)
		}
	}
}

func defaultKnowledgeLimitsForTest() int64 { return 20 << 20 }

func TestKnowledgeUploadLimitsAndFormatsAreConfigurable(t *testing.T) {
	limits := knowledge.Limits{MaxUploadBytes: 1 << 20, Extensions: []string{"txt"}, MaxCharacters: 2000, MaxChunks: 10, MaxPDFPages: 5}
	f := newFixture(t, ServerOptions{Knowledge: &limits})
	f.setup(t)
	knowledgeWorker(t, f)
	response := f.request(t, "GET", "/api/knowledge/status", nil, true, nil)
	requireStatus(t, response, 200)
	var status struct {
		MaxFileBytes      int64    `json:"max_file_bytes"`
		MaxCharacters     int      `json:"max_characters"`
		MaxChunks         int      `json:"max_chunks"`
		MaxPDFPages       int      `json:"max_pdf_pages"`
		AllowedExtensions []string `json:"allowed_extensions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.MaxFileBytes != 1<<20 || status.MaxCharacters != 2000 || status.MaxChunks != 10 || status.MaxPDFPages != 5 ||
		len(status.AllowedExtensions) != 1 || status.AllowedExtensions[0] != "txt" {
		t.Fatalf("configured limits not reported: %s", response.Body.String())
	}
	response = f.request(t, "POST", "/api/knowledge/bases", map[string]string{"name": "Limited", "description": ""}, true, nil)
	requireStatus(t, response, 201)
	var base knowledge.Base
	json.Unmarshal(response.Body.Bytes(), &base)
	// A format outside the configured list is rejected before any parser runs.
	response = f.uploadKnowledge(t, base.ID, "guide.pdf", "%PDF-1.4", true)
	requireStatus(t, response, 400)
	if !strings.Contains(response.Body.String(), "TXT") {
		t.Fatalf("format error does not name the allowed formats: %s", response.Body.String())
	}
	// The configured size limit is enforced instead of the default 20 MiB.
	response = f.uploadKnowledge(t, base.ID, "large.txt", strings.Repeat("x", (1<<20)+1), true)
	requireStatus(t, response, 413)
	if !strings.Contains(response.Body.String(), "1 MiB") {
		t.Fatalf("size error does not report the configured limit: %s", response.Body.String())
	}
	response = f.uploadKnowledge(t, base.ID, "small.txt", "Allowed text.", true)
	requireStatus(t, response, 202)
	var document knowledge.Document
	json.Unmarshal(response.Body.Bytes(), &document)
	waitDocument(t, f, document.ID)
}

func TestKnowledgeLimitsUpdateFromSettingsPageApplyImmediately(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	response := f.request(t, "GET", "/api/knowledge/limits", nil, true, nil)
	requireStatus(t, response, 200)
	var limits knowledgeLimitSettings
	if err := json.Unmarshal(response.Body.Bytes(), &limits); err != nil {
		t.Fatal(err)
	}
	if limits.MaxFileBytes != defaultKnowledgeLimitsForTest() {
		t.Fatalf("unexpected defaults: %s", response.Body.String())
	}
	// Out-of-range values are rejected and leave the active limits untouched.
	if err := json.Unmarshal([]byte(`{"max_file_bytes":1024}`), &limits); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/knowledge/limits", limits, true, nil), 400)
	response = f.request(t, "GET", "/api/knowledge/status", nil, true, nil)
	requireStatus(t, response, 200)
	var status struct {
		MaxFileBytes int64 `json:"max_file_bytes"`
	}
	json.Unmarshal(response.Body.Bytes(), &status)
	if status.MaxFileBytes != defaultKnowledgeLimitsForTest() {
		t.Fatal("invalid update changed the active limits")
	}
	// A valid update applies without restarting: formats and size change at once.
	update := knowledgeLimitSettings{MaxFileBytes: 2 << 20, MaxCharacters: 2000, MaxChunks: 10, MaxPDFPages: 5, AllowedExtensions: []string{"txt", "markdown"}}
	response = f.request(t, "PUT", "/api/knowledge/limits", update, true, nil)
	requireStatus(t, response, 200)
	response = f.request(t, "POST", "/api/knowledge/bases", map[string]string{"name": "Live limits", "description": ""}, true, nil)
	requireStatus(t, response, 201)
	var base knowledge.Base
	json.Unmarshal(response.Body.Bytes(), &base)
	response = f.uploadKnowledge(t, base.ID, "guide.pdf", "%PDF-1.4", true)
	requireStatus(t, response, 400)
	// The saved limit accepts a 1.5 MiB document; processing then rejects it for
	// exceeding the saved character budget, so the upload itself is admitted.
	response = f.uploadKnowledge(t, base.ID, "large.md", strings.Repeat("x", (3<<19)), true)
	requireStatus(t, response, 202)
	// The saved values survive a new server instance.
	restarted, err := New(f.store, f.runtime, func() bool { return true }, "")
	if err != nil {
		t.Fatal(err)
	}
	active := restarted.knowledge.Limits()
	if active.MaxUploadBytes != 2<<20 || len(active.Extensions) != 2 || active.ExtendWithParsers {
		t.Fatalf("saved limits not restored: %+v", active)
	}
}
