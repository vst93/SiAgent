package platform

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func uploadForTest(t *testing.T, f *fixture, conversation, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://localhost/api/conversations/"+conversation+"/attachments", &buffer)
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(f.cookie)
	request.Header.Set("X-CSRF-Token", f.csrf)
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	return response
}

func TestChatAttachmentUploadRunAndCleanup(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	conversation := f.conversation(t)
	response := uploadForTest(t, f, conversation, "notes.txt", "出差住宿标准为 500 元。")
	requireStatus(t, response, 201)
	var attachment Attachment
	if err := json.Unmarshal(response.Body.Bytes(), &attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Kind != "file" || attachment.Size == 0 || attachment.Name != "notes.txt" {
		t.Fatalf("unexpected attachment: %+v", attachment)
	}
	var extracted string
	if err := f.store.db.QueryRow("SELECT extracted FROM attachments WHERE id=?", attachment.ID).Scan(&extracted); err != nil || !strings.Contains(extracted, "500") {
		t.Fatalf("missing extracted text: %q %v", extracted, err)
	}
	w := f.request(t, "POST", "/api/conversations/"+conversation+"/runs", map[string]any{"input": "标准是多少？", "attachment_ids": []string{attachment.ID}}, true, nil)
	requireStatus(t, w, 202)
	if len(f.runtime.requests) == 0 {
		t.Fatal("run not dispatched")
	}
	attachments, _ := f.runtime.requests[len(f.runtime.requests)-1]["attachments"].([]map[string]any)
	if len(attachments) != 1 || attachments[0]["name"] != "notes.txt" || !strings.Contains(attachments[0]["text"].(string), "500") {
		t.Fatalf("runtime did not receive attachment: %#v", attachments)
	}
	var snapshotResponse = f.request(t, "GET", "/api/conversations/"+conversation, nil, true, nil)
	requireStatus(t, snapshotResponse, 200)
	var snapshot Snapshot
	if err := json.Unmarshal(snapshotResponse.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) < 1 || len(snapshot.Messages[0].Attachments) != 1 {
		t.Fatalf("snapshot lost attachment: %+v", snapshot.Messages)
	}
	var relative string
	f.store.db.QueryRow("SELECT path FROM attachments WHERE id=?", attachment.ID).Scan(&relative)
	if _, err := os.Stat(filepath.Join(f.directory, relative)); err != nil {
		t.Fatal("attachment file missing")
	}
	requireStatus(t, f.request(t, "DELETE", "/api/attachments/"+attachment.ID, nil, true, nil), 409)

	// A second, unused attachment can be removed immediately.
	second := uploadForTest(t, f, conversation, "draft.md", "draft")
	requireStatus(t, second, 201)
	var draft Attachment
	json.Unmarshal(second.Body.Bytes(), &draft)
	requireStatus(t, f.request(t, "DELETE", "/api/attachments/"+draft.ID, nil, true, nil), 204)
	var count int
	f.store.db.QueryRow("SELECT count(*) FROM attachments WHERE id=?", draft.ID).Scan(&count)
	if count != 0 {
		t.Fatal("unused attachment was not deleted")
	}
}

func TestImageAttachmentRequiresVisionModel(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	conversation := f.conversation(t)
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	response := uploadForTest(t, f, conversation, "pixel.png", string(png))
	requireStatus(t, response, 201)
	var image Attachment
	if err := json.Unmarshal(response.Body.Bytes(), &image); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+conversation+"/runs", map[string]any{"input": "看图", "attachment_ids": []string{image.ID}}, true, nil), 409)
	requireStatus(t, f.request(t, "PUT", "/api/model", map[string]any{
		"base_url": "http://localhost:9999/v1", "name": "fixture", "api_key": "secret",
		"options": map[string]any{"input": []string{"text", "image"}},
	}, true, nil), 200)
	w := f.request(t, "POST", "/api/conversations/"+conversation+"/runs", map[string]any{"input": "看图", "attachment_ids": []string{image.ID}}, true, nil)
	requireStatus(t, w, 202)
	attachments := f.runtime.requests[len(f.runtime.requests)-1]["attachments"].([]map[string]any)
	if len(attachments) != 1 || attachments[0]["kind"] != "image" || attachments[0]["data"] == "" {
		t.Fatalf("image not sent to runtime: %#v", attachments)
	}
}
