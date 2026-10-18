package platform

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEvalQuestionImportExportAndBaseConfigAPI(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	response := f.request(t, "POST", "/api/knowledge/bases", map[string]string{"name": "IO"}, true, nil)
	requireStatus(t, response, 201)
	var base struct {
		ID string `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &base)
	response = f.uploadKnowledge(t, base.ID, "policy.txt", "住宿标准为每晚 500 元。", true)
	requireStatus(t, response, 202)
	var document struct {
		ID string `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &document)
	waitDocument(t, f, document.ID)

	csv := "question,expected_document_ids,tags,answerable,expected_answer\n" +
		"住宿标准是多少," + document.ID + ",差旅,true,500 元\n" +
		"火星办公室地址, , ,false,\n"
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/knowledge/bases/"+base.ID+"/eval/questions/import", strings.NewReader(csv))
	request.Header.Set("Content-Type", "text/csv")
	request.Header.Set("X-CSRF-Token", f.csrf)
	request.AddCookie(f.cookie)
	recorder := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(recorder, request)
	requireStatus(t, recorder, 200)
	if !strings.Contains(recorder.Body.String(), `"imported":2`) {
		t.Fatalf("import failed: %s", recorder.Body.String())
	}

	response = f.request(t, "GET", "/api/knowledge/bases/"+base.ID+"/eval/questions", nil, true, nil)
	requireStatus(t, response, 200)
	var questions []struct {
		Answerable bool `json:"answerable"`
	}
	json.Unmarshal(response.Body.Bytes(), &questions)
	if len(questions) != 2 || questions[1].Answerable {
		t.Fatalf("answerable flag lost: %s", response.Body.String())
	}

	response = f.request(t, "GET", "/api/knowledge/bases/"+base.ID+"/eval/questions/export?format=csv", nil, true, nil)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), "住宿标准是多少") || !strings.Contains(response.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv export failed: %s", response.Body.String())
	}

	response = f.request(t, "GET", "/api/knowledge/bases/"+base.ID+"/config", nil, true, nil)
	requireStatus(t, response, 200)
	response = f.request(t, "PUT", "/api/knowledge/bases/"+base.ID+"/config", map[string]any{"expand": false, "llm_expand": false, "gate": true}, true, nil)
	requireStatus(t, response, 200)
	response = f.request(t, "GET", "/api/knowledge/bases/"+base.ID+"/config", nil, true, nil)
	if !strings.Contains(response.Body.String(), `"gate":true`) {
		t.Fatalf("config not saved: %s", response.Body.String())
	}
	response = f.request(t, "POST", "/api/knowledge/bases/"+base.ID+"/vocabulary", map[string]string{"term": "酒店开销", "expansion": "住宿费"}, true, nil)
	requireStatus(t, response, 200)
	response = f.request(t, "GET", "/api/knowledge/bases/"+base.ID+"/vocabulary/candidates", nil, true, nil)
	requireStatus(t, response, 200)
}

func TestAnswerScoreMetrics(t *testing.T) {
	sources := []string{"住宿标准为每晚 500 元。", "报销需要发票。"}
	grounded := scoreAnswer("住宿标准为每晚 500 元 [1]。", sources)
	if grounded.CitationPrecision != 1 || grounded.Faithfulness != 1 || grounded.Abstained {
		t.Fatalf("grounded answer scored incorrectly: %+v", grounded)
	}
	unknown := scoreAnswer("住宿标准为每晚 800 元 [3]。", sources)
	if unknown.CitationPrecision != 0 || unknown.Faithfulness != 0 {
		t.Fatalf("unknown citation scored incorrectly: %+v", unknown)
	}
	abstain := scoreAnswer("资料中未找到相关内容。", sources)
	if !abstain.Abstained || abstain.CitationPrecision != 0 {
		t.Fatalf("abstention scored incorrectly: %+v", abstain)
	}
}

func TestConflictOutdatedAndGateAPIs(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	response := f.request(t, "POST", "/api/knowledge/bases", map[string]string{"name": "Conflicts"}, true, nil)
	requireStatus(t, response, 201)
	var base struct {
		ID string `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &base)
	documents := map[string]string{}
	for name, text := range map[string]string{"a.txt": "住宿标准为每晚 500 元。", "b.txt": "住宿标准为每晚 600 元。"} {
		response = f.uploadKnowledge(t, base.ID, name, text, true)
		requireStatus(t, response, 202)
		var document struct {
			ID string `json:"id"`
		}
		json.Unmarshal(response.Body.Bytes(), &document)
		waitDocument(t, f, document.ID)
		documents[name] = document.ID
	}
	response = f.request(t, "POST", "/api/knowledge/bases/"+base.ID+"/conflicts/detect", nil, true, nil)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), `"found":1`) {
		t.Fatalf("conflict not detected: %s", response.Body.String())
	}
	response = f.request(t, "GET", "/api/knowledge/bases/"+base.ID+"/conflicts?status=open", nil, true, nil)
	requireStatus(t, response, 200)
	var conflicts []struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &conflicts)
	if len(conflicts) != 1 {
		t.Fatalf("conflict not listed: %s", response.Body.String())
	}
	response = f.request(t, "POST", "/api/knowledge/conflicts/"+itoa(conflicts[0].ID)+"/status", map[string]string{"status": "resolved"}, true, nil)
	requireStatus(t, response, 200)

	response = f.request(t, "GET", "/api/knowledge/outdated?knowledge_id="+base.ID, nil, true, nil)
	requireStatus(t, response, 200)

	// Enable the gate and record a regressed retrieval run.
	response = f.request(t, "PUT", "/api/knowledge/bases/"+base.ID+"/config", map[string]any{"expand": true, "gate": true}, true, nil)
	requireStatus(t, response, 200)
	metrics := `{"questions":1,"recall_at_5":0.2,"mrr":0.2,"previous_recall_at_5":1,"regressed":true}`
	if _, err := f.store.db.Exec("INSERT INTO knowledge_eval_runs(id,knowledge_id,created,metrics,cases,kind) VALUES('regressed',?,?,?, '[]','retrieval')", base.ID, 9999999999999, metrics); err != nil {
		t.Fatal(err)
	}
	// Replacing a document is rejected until forced.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "a.txt")
	part.Write([]byte("住宿标准为每晚 700 元。"))
	writer.Close()
	request := httptest.NewRequest(http.MethodPut, "http://localhost/api/knowledge/bases/"+base.ID+"/documents/"+documents["a.txt"], &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", f.csrf)
	request.AddCookie(f.cookie)
	recorder := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(recorder, request)
	requireStatus(t, recorder, 409)
}

func TestPluginUploadConfigAndHealth(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	manifest := `{"schema":"siagent.plugin/v1","id":"acme.zip","name":"Acme Zip","version":"1.0.0","type":"parser","api_version":"1","entry":"parse.sh","extensions":[".zipx"],"config":[{"key":"endpoint","label":"地址","required":true},{"key":"token","label":"密钥","secret":true,"required":true}]}`
	manifestFile, _ := writer.Create("plugin.json")
	manifestFile.Write([]byte(manifest))
	header := &zip.FileHeader{Name: "parse.sh", Method: zip.Deflate}
	header.SetMode(0700)
	entry, _ := writer.CreateHeader(header)
	entry.Write([]byte("#!/bin/sh\nprintf '解析成功。\\n'\n"))
	writer.Close()

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	part, _ := multipartWriter.CreateFormFile("file", "acme.zip")
	part.Write(archive.Bytes())
	multipartWriter.Close()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/plugins/upload", &body)
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	request.Header.Set("X-CSRF-Token", f.csrf)
	request.AddCookie(f.cookie)
	recorder := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(recorder, request)
	requireStatus(t, recorder, 200)
	if !strings.Contains(recorder.Body.String(), `"id":"acme.zip"`) {
		t.Fatalf("upload failed: %s", recorder.Body.String())
	}

	response := f.request(t, "PUT", "/api/plugins/acme.zip/config", map[string]any{
		"values":  map[string]string{"endpoint": "https://example.com"},
		"secrets": map[string]string{"token": "secret-token"},
	}, true, nil)
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "secret-token") {
		t.Fatal("plugin secret leaked")
	}
	response = f.request(t, "POST", "/api/plugins/acme.zip/health", nil, true, nil)
	requireStatus(t, response, 200)
	response = f.request(t, "GET", "/api/plugins", nil, true, nil)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), "acme.zip") || !strings.Contains(response.Body.String(), "channel.") {
		t.Fatalf("installed plugin or builtin channel missing: %s", response.Body.String())
	}
}

func TestOpenAPISpecIsPublished(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	request := httptest.NewRequest(http.MethodGet, "http://localhost/v1/openapi.yaml", nil)
	recorder := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(recorder, request)
	requireStatus(t, recorder, 200)
	if !strings.Contains(recorder.Body.String(), "openapi: 3.1.0") || !strings.Contains(recorder.Body.String(), "/agents/{agent_id}/runs") {
		t.Fatalf("openapi spec incomplete: %s", recorder.Body.String())
	}
}

func TestAnswerEvalUsesModelAndScoresMetrics(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"住宿标准为每晚 500 元 [1]。"}}]}`))
	}))
	defer modelServer.Close()
	response := f.request(t, "PUT", "/api/models/1", map[string]any{
		"base_url": modelServer.URL,
		"name":     "fake",
		"api_key":  "key",
		"provider": "openai",
		"options":  map[string]any{"api": "openai-completions"},
	}, true, nil)
	requireStatus(t, response, 200)

	response = f.request(t, "POST", "/api/knowledge/bases", map[string]string{"name": "Answers"}, true, nil)
	requireStatus(t, response, 201)
	var base struct {
		ID string `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &base)
	response = f.uploadKnowledge(t, base.ID, "policy.txt", "住宿标准为每晚 500 元。", true)
	requireStatus(t, response, 202)
	var document struct {
		ID string `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &document)
	waitDocument(t, f, document.ID)
	response = f.request(t, "POST", "/api/knowledge/bases/"+base.ID+"/eval/questions", map[string]any{
		"question":              "住宿标准是多少",
		"expected_document_ids": []string{document.ID},
		"answerable":            true,
	}, true, nil)
	requireStatus(t, response, 201)

	response = f.request(t, "POST", "/api/knowledge/bases/"+base.ID+"/eval/answers", nil, true, nil)
	requireStatus(t, response, 201)
	var run struct {
		Metrics struct {
			CitationPrecision float64 `json:"citation_precision"`
			Faithfulness      float64 `json:"faithfulness"`
			AbstentionAcc     float64 `json:"abstention_accuracy"`
		} `json:"metrics"`
		Kind string `json:"kind"`
	}
	json.Unmarshal(response.Body.Bytes(), &run)
	if run.Kind != "answer" || run.Metrics.CitationPrecision != 1 || run.Metrics.Faithfulness != 1 || run.Metrics.AbstentionAcc != 1 {
		t.Fatalf("unexpected answer metrics: %s", response.Body.String())
	}
}

func TestProviderIsForwardedToRuntime(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	requireStatus(t, f.request(t, "POST", "/api/model-providers/packs/pi-providers/install", nil, true, nil), 200)
	response := f.request(t, "POST", "/api/models", map[string]any{
		"base_url": "http://localhost:9999/v1",
		"name":     "deepseek-chat",
		"api_key":  "key",
		"provider": "deepseek",
		"options":  map[string]any{"api": "openai-completions"},
	}, true, nil)
	requireStatus(t, response, 201)
	var model struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &model)
	agent, err := f.server.agentByID("default")
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", map[string]any{
		"name": "默认助手", "instructions": agent.Instructions, "enabled": true, "model_id": model.ID,
	}, true, nil), 200)
	f.start(t, f.conversation(t), "hello")
	config := f.runtime.requests[0]["model"].(map[string]any)
	if config["provider"] != "deepseek" {
		t.Fatalf("provider not forwarded: %+v", config)
	}
}
