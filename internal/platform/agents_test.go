package platform

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func createTestModel(t *testing.T, f *fixture, name, key string) Model {
	t.Helper()
	response := f.request(t, "POST", "/api/models", map[string]any{"base_url": "http://localhost:9999/v1", "name": name, "api_key": key}, true, nil)
	requireStatus(t, response, 201)
	var model Model
	if err := json.Unmarshal(response.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Body.String(), key) {
		t.Fatal("model response leaked credential")
	}
	return model
}
func saveTestAgent(t *testing.T, f *fixture, a Agent) Agent {
	t.Helper()
	path := "/api/agents"
	method := "POST"
	status := 201
	if a.ID != "" {
		path += "/" + a.ID
		method = "PUT"
		status = 200
	}
	response := f.request(t, method, path, map[string]any{"name": a.Name, "instructions": a.Instructions, "enabled": a.Enabled, "model_id": a.ModelID, "fallback_model_ids": a.FallbackModelIDs}, true, nil)
	requireStatus(t, response, status)
	var saved Agent
	json.Unmarshal(response.Body.Bytes(), &saved)
	return saved
}
func agentConversation(t *testing.T, f *fixture, agent string) string {
	t.Helper()
	response := f.request(t, "POST", "/api/conversations", map[string]string{"agent_id": agent}, true, nil)
	requireStatus(t, response, 201)
	var conversation Conversation
	json.Unmarshal(response.Body.Bytes(), &conversation)
	if conversation.AgentID != agent {
		t.Fatal("conversation lost immutable owner")
	}
	return conversation.ID
}
func TestAgentQueueFreezesIndependentModelsAndCapabilities(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "default-model", "default-secret")
	f.server.limits.Concurrent = 1
	model := createTestModel(t, f, "role-model", "role-secret-original")
	a := saveTestAgent(t, f, Agent{Name: "Finance", Instructions: "original finance role", Enabled: true, ModelID: model.ID})
	skill, err := f.store.importSkill(skillDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+a.ID+"/capabilities", map[string]any{"skills": []string{skill.ID}, "mcps": []MCPBinding{}, "confirm": true}, true, nil), 200)
	base, err := f.server.knowledge.SaveBase(context.Background(), "", "Finance docs", "")
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+a.ID+"/knowledge", map[string]any{"knowledge_ids": []string{base.ID}, "confirm": true}, true, nil), 200)
	defaultCaps, err := f.store.bindings("default")
	if err != nil || len(defaultCaps.Skills) != 0 {
		t.Fatal("role capabilities leaked to default agent")
	}
	first := f.start(t, f.conversation(t), "hold default role")
	conversation := agentConversation(t, f, a.ID)
	second := f.start(t, conversation, "queued finance question")
	if len(f.runtime.requests) != 1 {
		t.Fatal("role bypassed global concurrency")
	}
	a.ModelID = 1
	a.Instructions = "changed role"
	a.Enabled = false
	saveTestAgent(t, f, a)
	requireStatus(t, f.request(t, "PUT", "/api/models/"+strconv.FormatInt(model.ID, 10), map[string]any{"base_url": "http://localhost:9999/v1", "name": "changed-model", "api_key": "changed-secret"}, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+a.ID+"/capabilities", map[string]any{"skills": []string{}, "mcps": []MCPBinding{}}, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+a.ID+"/knowledge", map[string]any{"knowledge_ids": []string{}}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+a.ID, nil, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/models/"+strconv.FormatInt(model.ID, 10), nil, true, nil), 200)
	emit(t, f, first, "run_started", 1)
	emit(t, f, first, "run_completed", 2)
	emit(t, f, first, "run_released", 3)
	if len(f.runtime.requests) != 2 {
		t.Fatal("archiving discarded queued task")
	}
	request := f.runtime.requests[1]
	if request["agent_id"] != a.ID || request["instructions"] != "original finance role" {
		t.Fatal("queued role snapshot changed")
	}
	connection := request["model"].(map[string]any)
	if connection["name"] != "role-model" || connection["api_key"] != "role-secret-original" {
		t.Fatal("queued model snapshot changed")
	}
	caps := request["capabilities"].(*Capabilities)
	if len(caps.Skills) != 1 || caps.Skills[0].ID != skill.ID {
		t.Fatal("queued capabilities changed")
	}
	ids := request["knowledge_ids"].([]string)
	if len(ids) != 1 || ids[0] != base.ID {
		t.Fatal("queued knowledge authorization changed")
	}
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+conversation+"/runs", map[string]string{"input": "new question"}, true, nil), 409)
	requireStatus(t, f.request(t, "POST", "/api/conversations", map[string]string{"agent_id": a.ID}, true, nil), 409)
	requireStatus(t, f.request(t, "GET", "/api/conversations/"+conversation, nil, true, nil), 200)
	var config string
	if err := f.store.db.QueryRow("SELECT config FROM runs WHERE id=?", second).Scan(&config); err != nil || strings.Contains(config, "role-secret-original") {
		t.Fatal("configuration exposed credential")
	}
}
func TestAgentArchiveGuardsAllActiveBindings(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "default-model", "default-secret")
	m := createTestModel(t, f, "other", "other-key")
	a := saveTestAgent(t, f, Agent{Name: "Other", Enabled: true, ModelID: m.ID})
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+a.ID, nil, true, nil), 409)
	requireStatus(t, f.request(t, "DELETE", "/api/models/"+strconv.FormatInt(m.ID, 10), nil, true, nil), 409)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/default", nil, true, nil), 409)
	skill, err := f.store.importSkill(skillDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+a.ID+"/capabilities", map[string]any{"skills": []string{skill.ID}, "confirm": true}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/skills/"+skill.ID, nil, true, nil), 200)
	a.Enabled = false
	saveTestAgent(t, f, a)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+a.ID, nil, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+a.ID+"/knowledge", map[string]any{"knowledge_ids": []string{}}, true, nil), 409)
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+a.ID+"/capabilities", map[string]any{"skills": []string{}}, true, nil), 409)
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+a.ID, map[string]any{"name": "Revive", "enabled": true, "model_id": 1}, true, nil), 409)
	requireStatus(t, f.request(t, "DELETE", "/api/models/"+strconv.FormatInt(m.ID, 10), nil, true, nil), 200)
	requireStatus(t, f.request(t, "POST", "/api/agents", map[string]any{"name": "Invalid model", "enabled": true, "model_id": m.ID}, true, nil), 400)
	requireStatus(t, f.request(t, "GET", "/api/models", nil, false, nil), 401)
	requireStatus(t, f.request(t, "POST", "/api/agents", map[string]any{"name": "Unauthorized"}, false, nil), 401)
}

func TestModelEditKeepsOnlyThatConnectionsKey(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "one", "one-secret")
	m := createTestModel(t, f, "two", "two-secret")
	requireStatus(t, f.request(t, "PUT", "/api/models/"+strconv.FormatInt(m.ID, 10), map[string]any{"base_url": "http://localhost:9999/v1", "name": "two-updated", "api_key": ""}, true, nil), 200)
	two, err := f.store.modelByID(m.ID)
	if err != nil || two.APIKey != "two-secret" || two.Version != 2 {
		t.Fatal("edited connection lost its own key")
	}
	one, err := f.store.model()
	if err != nil || one.APIKey != "one-secret" || one.Version != 1 {
		t.Fatal("editing another connection changed default model")
	}
	response := f.request(t, "GET", "/api/models", nil, true, nil)
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "one-secret") || strings.Contains(response.Body.String(), "two-secret") || strings.Contains(response.Body.String(), "api_key") {
		t.Fatal("catalog returned stored credentials")
	}
	conversation := agentConversation(t, f, "default")
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+conversation+"/runs", map[string]any{"input": "attempt rebind", "agent_id": "other"}, true, nil), 400)
}

func TestAgentFallbackModelsAreOrderedAndSnapshotted(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "primary", "primary-key")
	first := createTestModel(t, f, "backup-one", "backup-one-key")
	second := createTestModel(t, f, "backup-two", "backup-two-key")
	a := saveTestAgent(t, f, Agent{Name: "Fallback", Enabled: true, ModelID: 1, FallbackModelIDs: []int64{second.ID, first.ID}})
	if len(a.FallbackModelIDs) != 2 || a.FallbackModelIDs[0] != second.ID || a.FallbackModelIDs[1] != first.ID {
		t.Fatalf("fallback order lost: %+v", a.FallbackModelIDs)
	}
	conversation := agentConversation(t, f, a.ID)
	if _, err := f.server.enqueueRun(context.Background(), conversation, "use fallback", 0, nil); err != nil {
		t.Fatal(err)
	}
	request := f.runtime.requests[len(f.runtime.requests)-1]
	model := request["model"].(map[string]any)
	fallbacks, ok := model["fallbacks"].([]map[string]any)
	if !ok || len(fallbacks) != 2 {
		t.Fatalf("runtime fallback snapshot missing: %#v", model)
	}
	if fallbacks[0]["name"] != "backup-two" || fallbacks[1]["name"] != "backup-one" {
		t.Fatalf("runtime fallback order changed: %#v", fallbacks)
	}
	if fallbacks[0]["api_key"] != "backup-two-key" || fallbacks[1]["api_key"] != "backup-one-key" {
		t.Fatalf("runtime fallback credentials missing")
	}
}
