package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "siagent/internal/runtime"
)

func skillDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: sample-skill\ndescription: A trusted local example.\ncompatibility: Python 3; no automatic installation.\n---\nUse the provided reference; never enable additional tools.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "example.py"), []byte("print('inspection only')\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestSkillImportIsExplicitImmutableAndAuthenticated(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	root := skillDirectory(t)
	requireStatus(t, f.request(t, "POST", "/api/skills/import", map[string]any{"directory": root, "trusted": true}, false, nil), 401)
	requireStatus(t, f.request(t, "POST", "/api/skills/import", map[string]any{"directory": root}, true, nil), 400)
	response := f.skillUpload(t, "skill.zip", skillZIP(t, root), true)
	requireStatus(t, response, 201)
	var skill Skill
	json.Unmarshal(response.Body.Bytes(), &skill)
	if skill.Name != "sample-skill" || len(skill.Files) != 2 || !strings.Contains(skill.Compatibility, "Python") {
		t.Fatalf("incorrect import: %+v", skill)
	}
	script := filepath.Join(f.directory, "skills", skill.ID, "scripts", "example.py")
	info, err := os.Stat(script)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("scripts must be stored without executable bits")
	}
	os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("changed source"), 0600)
	preview := f.request(t, "GET", "/api/skills/"+skill.ID+"/files/SKILL.md", nil, true, nil)
	requireStatus(t, preview, 200)
	if strings.Contains(preview.Body.String(), "changed source") {
		t.Fatal("import must copy its source")
	}
	requireStatus(t, f.request(t, "GET", "/api/skills/"+skill.ID+"/files/SKILL.md", nil, false, nil), 401)
	binding := map[string]any{"skills": []string{skill.ID}, "mcps": []MCPBinding{}, "confirm": true}
	requireStatus(t, f.request(t, "PUT", "/api/agent/capabilities", binding, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/skills/"+skill.ID, nil, true, nil), 409)
	requireStatus(t, f.request(t, "PUT", "/api/agent/capabilities", map[string]any{"skills": []string{}, "mcps": []MCPBinding{}}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/skills/"+skill.ID, nil, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/agent/capabilities", binding, true, nil), 400)
}
func TestSkillRejectsSymlinksAndMalformedMetadata(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	root := skillDirectory(t)
	if err := os.Symlink(filepath.Join(f.directory, "master.key"), filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.importSkill(root); err == nil {
		t.Fatal("unsafe or malformed skill accepted")
	}
	os.Remove(filepath.Join(root, "leak"))
	os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("no frontmatter"), 0600)
	if _, err := f.store.importSkill(root); err == nil {
		t.Fatal("unsafe or malformed skill accepted")
	}
	entries, err := os.ReadDir(filepath.Join(f.directory, "skills"))
	if err != nil || len(entries) != 0 {
		t.Fatal("failed import left installed files")
	}
}

type callbackRuntime struct{ callback func(map[string]any) }

func (r callbackRuntime) Send(value any) error { r.callback(value.(map[string]any)); return nil }
func TestMCPSecretsPermissionValidationAndConnectionRouting(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	input := map[string]any{"name": "Remote", "transport": "http", "url": "https://example.com/mcp", "headers": map[string]string{"Authorization": "Bearer hidden-mcp-key"}, "trusted": true}
	response := f.request(t, "POST", "/api/mcps", input, true, nil)
	requireStatus(t, response, 201)
	if strings.Contains(response.Body.String(), "hidden-mcp-key") {
		t.Fatal("secret was echoed")
	}
	var view MCPView
	json.Unmarshal(response.Body.Bytes(), &view)
	var raw, secrets string
	f.store.db.QueryRow("SELECT config,secrets FROM mcps WHERE id=?", view.ID).Scan(&raw, &secrets)
	if strings.Contains(raw+secrets, "hidden-mcp-key") {
		t.Fatal("plaintext secret stored")
	}
	f.server.runtime = callbackRuntime{func(request map[string]any) {
		secret := request["mcp_secret"].(MCPSecret)
		if secret.Headers["Authorization"] != "Bearer hidden-mcp-key" {
			t.Fatal("test did not use private credential")
		}
		data, _ := json.Marshal(map[string]any{"tools": []MCPTool{{Name: "read", Description: "Read a record.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), ReadOnly: true}, {Name: "write", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}})
		if err := f.server.HandleEvent(agentruntime.Event{Type: "mcp_test_result", RequestID: request["request_id"].(string), Data: data}); err != nil {
			t.Fatal(err)
		}
	}}
	requireStatus(t, f.request(t, "POST", "/api/mcps/"+view.ID+"/test", nil, true, nil), 200)
	binding := map[string]any{"skills": []string{}, "mcps": []MCPBinding{{ID: view.ID, Tools: []string{"write"}}}}
	requireStatus(t, f.request(t, "PUT", "/api/agent/capabilities", binding, true, nil), 400)
	binding["confirm"] = true
	requireStatus(t, f.request(t, "PUT", "/api/agent/capabilities", binding, true, nil), 200)
	binding["mcps"] = []MCPBinding{{ID: view.ID, Tools: []string{"unlisted"}}}
	requireStatus(t, f.request(t, "PUT", "/api/agent/capabilities", binding, true, nil), 400)
	input["url"] = "https://example.com/mcp?token=secret"
	requireStatus(t, f.request(t, "POST", "/api/mcps", input, true, nil), 400)
	input["url"] = "https://example.com/mcp"
	input["headers"] = map[string]string{"Host": "evil"}
	requireStatus(t, f.request(t, "POST", "/api/mcps", input, true, nil), 400)
	requireStatus(t, f.request(t, "GET", "/api/mcps", nil, false, nil), 401)
}
func TestQueueFreezesCapabilitiesAndCredentials(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "original-model-secret")
	root := skillDirectory(t)
	skill, err := f.store.importSkill(root)
	if err != nil {
		t.Fatal(err)
	}
	config := MCPConfig{ID: id(), Name: "Local", Transport: "stdio", Command: "/usr/bin/example", Args: []string{}, Tools: []MCPTool{{Name: "write", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}}
	raw, _ := json.Marshal(config)
	secret, _ := json.Marshal(MCPSecret{Env: map[string]string{"TOKEN": "original-tool-secret"}, Headers: map[string]string{}})
	if _, err := f.store.db.Exec("INSERT INTO mcps(id,config,secrets,version) VALUES(?,?,?,1)", config.ID, string(raw), f.store.encrypt(string(secret))); err != nil {
		t.Fatal(err)
	}
	binding := map[string]any{"skills": []string{skill.ID}, "mcps": []MCPBinding{{ID: config.ID, Tools: []string{"write"}}}, "confirm": true}
	requireStatus(t, f.request(t, "PUT", "/api/agent/capabilities", binding, true, nil), 200)
	f.server.limits.Concurrent = 1
	first := f.start(t, f.conversation(t), "first")
	second := f.start(t, f.conversation(t), "second")
	if len(f.runtime.requests) != 1 {
		t.Fatal("second run must be queued")
	}
	var saved, encrypted string
	f.store.db.QueryRow("SELECT config,credential FROM runs WHERE id=?", second).Scan(&saved, &encrypted)
	if strings.Contains(saved+encrypted, "original-tool-secret") {
		t.Fatal("snapshot leaked secret")
	}
	f.model(t, "changed-model", "changed-model-secret")
	if _, err := f.store.db.Exec("UPDATE mcps SET secrets=? WHERE id=?", f.store.encrypt(`{"env":{"TOKEN":"changed-tool-secret"},"headers":{}}`), config.ID); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/agent/capabilities", map[string]any{"skills": []string{}, "mcps": []MCPBinding{}}, true, nil), 200)
	event := func(kind string, seq int64) {
		if err := f.server.HandleEvent(agentruntime.Event{Type: kind, RunID: first, Sequence: seq, Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	event("run_completed", 1)
	event("run_released", 2)
	if len(f.runtime.requests) != 2 {
		t.Fatal("queued task was not dispatched")
	}
	request := f.runtime.requests[1]
	if request["model"].(map[string]any)["api_key"] != "original-model-secret" || request["mcp_secrets"].(map[string]MCPSecret)[config.ID].Env["TOKEN"] != "original-tool-secret" {
		t.Fatal("queued credentials were not frozen")
	}
	snapshot := request["capabilities"].(*Capabilities)
	if len(snapshot.Skills) != 1 || len(snapshot.MCPs) != 1 || snapshot.MCPs[0].Tools[0].Name != "write" {
		t.Fatal("queued permissions were not frozen")
	}
	if err := f.server.HandleEvent(agentruntime.Event{Type: "tool_start", RunID: second, Sequence: 1, Data: json.RawMessage(`{"toolName":"write","toolCallId":"call","args":{"record":"safe"}}`)}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.HandleEvent(agentruntime.Event{Type: "tool_end", RunID: second, Sequence: 2, Data: json.RawMessage(`{"toolName":"write","toolCallId":"call","outcome_unknown":true,"result":"unconfirmed"}`)}); err != nil {
		t.Fatal(err)
	}
	details := f.request(t, "GET", "/api/runs/"+second+"/tools/call", nil, true, nil)
	requireStatus(t, details, 200)
	if !strings.Contains(details.Body.String(), "interrupted") || !strings.Contains(details.Body.String(), "safe") {
		t.Fatal("call details were not preserved")
	}
	requireStatus(t, f.request(t, "GET", "/api/runs/"+second+"/tools/call", nil, false, nil), 401)
	diagnostic := f.request(t, "GET", "/api/runs/"+second, nil, true, nil)
	requireStatus(t, diagnostic, 200)
	if strings.Contains(diagnostic.Body.String(), "unconfirmed") || strings.Contains(diagnostic.Body.String(), "record") {
		t.Fatal("ordinary diagnostics exposed raw tool data")
	}
}

func TestEmptyCapabilitiesSerializeAsArrays(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	response := f.request(t, "PUT", "/api/agent/capabilities", map[string]any{}, true, nil)
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "null") {
		t.Fatal("empty bindings must serialize as arrays")
	}
	response = f.request(t, "GET", "/api/agent/capabilities", nil, true, nil)
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "null") {
		t.Fatal("saved empty bindings must serialize as arrays")
	}
}
