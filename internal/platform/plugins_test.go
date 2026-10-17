package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"siagent/internal/knowledge"
	"siagent/internal/plugins"
)

func writeParserPackage(t *testing.T, directory, version, body string) {
	t.Helper()
	root := filepath.Join(directory, "plugin-packages", "acme.parser", version)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":"siagent.plugin/v1","id":"acme.parser","name":"Acme Parser","version":"` + version + `","type":"parser","api_version":"1","entry":"parse.sh","extensions":[".xyz"],"timeout_ms":5000}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "parse.sh"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestParserExtensionLifecycle(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	knowledgeWorker(t, f)
	writeParserPackage(t, f.directory, "1.0.0", "#!/bin/sh\nprintf '扩展解析出的住宿标准为 500 元。\\n'\n")

	response := f.request(t, "GET", "/api/plugins", nil, true, nil)
	requireStatus(t, response, 200)
	var catalog struct {
		Installed  []plugins.Plugin    `json:"installed"`
		Candidates []plugins.Candidate `json:"candidates"`
		Builtin    []builtinPlugin     `json:"builtin"`
	}
	json.Unmarshal(response.Body.Bytes(), &catalog)
	if len(catalog.Candidates) != 1 || catalog.Candidates[0].Installed {
		t.Fatalf("candidate not discovered: %s", response.Body.String())
	}
	if len(catalog.Builtin) == 0 {
		t.Fatal("bundled channel plugins should be listed")
	}

	base, err := f.server.knowledge.SaveBase(t.Context(), "", "Custom", "")
	if err != nil {
		t.Fatal(err)
	}
	// Unknown formats are rejected until a parser extension is enabled.
	requireStatus(t, f.uploadKnowledge(t, base.ID, "custom.xyz", "payload", true), 400)

	requireStatus(t, f.request(t, "POST", "/api/plugins/acme.parser/install", map[string]string{"version": "1.0.0"}, true, nil), 200)
	requireStatus(t, f.request(t, "POST", "/api/plugins/acme.parser/enable", nil, true, nil), 200)

	response = f.uploadKnowledge(t, base.ID, "custom.xyz", "ignored", true)
	requireStatus(t, response, 202)
	var document knowledge.Document
	json.Unmarshal(response.Body.Bytes(), &document)
	if state := waitDocument(t, f, document.ID); state.State != "ready" {
		t.Fatalf("extension parsed document did not index: %+v", state)
	}
	scope := knowledge.Scope{KnowledgeIDs: []string{base.ID}}
	if result, err := f.server.knowledge.Search(t.Context(), scope, knowledge.SearchRequest{Query: "住宿标准"}); err != nil || len(result.Hits) == 0 {
		t.Fatalf("parsed text not searchable: %+v %v", result, err)
	}

	requireStatus(t, f.request(t, "POST", "/api/plugins/acme.parser/disable", nil, true, nil), 200)
	requireStatus(t, f.uploadKnowledge(t, base.ID, "second.xyz", "payload", true), 400)

	// A crashing extension fails the document but not the server.
	writeParserPackage(t, f.directory, "2.0.0", "#!/bin/sh\nexit 7\n")
	requireStatus(t, f.request(t, "POST", "/api/plugins/acme.parser/install", map[string]string{"version": "2.0.0"}, true, nil), 200)
	requireStatus(t, f.request(t, "POST", "/api/plugins/acme.parser/enable", nil, true, nil), 200)
	response = f.uploadKnowledge(t, base.ID, "broken.xyz", "payload", true)
	requireStatus(t, response, 202)
	json.Unmarshal(response.Body.Bytes(), &document)
	if waitDocument(t, f, document.ID).State != "failed" {
		t.Fatal("crashing parser should fail the document")
	}
	if err := f.server.Tick(time.Now()); err != nil {
		t.Fatal(err)
	}

	// Rollback returns to the working version.
	requireStatus(t, f.request(t, "POST", "/api/plugins/acme.parser/rollback", nil, true, nil), 200)
	response = f.request(t, "GET", "/api/plugins", nil, true, nil)
	json.Unmarshal(response.Body.Bytes(), &catalog)
	if len(catalog.Installed) != 1 || catalog.Installed[0].Version != "1.0.0" {
		t.Fatalf("rollback did not restore the previous version: %s", response.Body.String())
	}
	requireStatus(t, f.uploadKnowledge(t, base.ID, "after-rollback.xyz", "payload", true), 202)

	events := f.request(t, "GET", "/api/plugins/events?plugin_id=acme.parser", nil, true, nil)
	requireStatus(t, events, 200)
	var log []plugins.Event
	json.Unmarshal(events.Body.Bytes(), &log)
	if len(log) == 0 {
		t.Fatal("plugin events not recorded")
	}

	requireStatus(t, f.request(t, "DELETE", "/api/plugins/acme.parser", nil, true, nil), 200)
	requireStatus(t, f.uploadKnowledge(t, base.ID, "after-uninstall.xyz", "payload", true), 400)
}

func TestPluginInstallRejectsEscapingEntry(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	root := filepath.Join(f.directory, "plugin-packages", "evil.plugin", "1.0.0")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":"siagent.plugin/v1","id":"evil.plugin","name":"Evil","version":"1.0.0","type":"parser","api_version":"1","entry":"../../escape.sh","extensions":[".xyz"]}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "POST", "/api/plugins/evil.plugin/install", map[string]string{"version": "1.0.0"}, true, nil), 400)
}
