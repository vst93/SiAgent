package platform

import (
	"encoding/json"
	"testing"
)

func TestAdvancedToolsDefaultOffAndAudited(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	agent, err := f.server.agentByID("default")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Policy.AdvancedTools {
		t.Fatal("advanced tools must default to off")
	}

	body := map[string]any{
		"name":         "默认助手",
		"instructions": agent.Instructions,
		"enabled":      true,
		"model_id":     agent.ModelID,
		"policy":       map[string]any{"advanced_tools": true},
	}
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", body, true, nil), 200)
	saved, err := f.server.agentByID("default")
	if err != nil || !saved.Policy.AdvancedTools {
		t.Fatalf("advanced tools not saved: %+v %v", saved.Policy, err)
	}
	var action string
	if err := f.store.db.QueryRow("SELECT action FROM audit_log WHERE resource='default' ORDER BY id DESC LIMIT 1").Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "agent.advanced_tools.enabled" {
		t.Fatalf("unexpected audit action: %s", action)
	}

	body["policy"] = map[string]any{"advanced_tools": false}
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", body, true, nil), 200)
	if err := f.store.db.QueryRow("SELECT action FROM audit_log WHERE resource='default' ORDER BY id DESC LIMIT 1").Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "agent.advanced_tools.disabled" {
		t.Fatalf("unexpected audit action: %s", action)
	}
}

func TestAdvancedToolsDispatchCarriesWorkspaceOnlyWhenEnabled(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	agent, _ := f.server.agentByID("default")
	body := map[string]any{"name": "默认助手", "instructions": agent.Instructions, "enabled": true, "model_id": agent.ModelID, "policy": map[string]any{"advanced_tools": true}}
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", body, true, nil), 200)
	f.start(t, f.conversation(t), "use tools")
	if f.runtime.requests[0]["advanced_tools"] != true {
		t.Fatal("advanced_tools not forwarded")
	}
	if workspace, _ := f.runtime.requests[0]["workspace"].(string); workspace == "" {
		t.Fatal("workspace missing for advanced tools")
	}

	body["policy"] = map[string]any{"advanced_tools": false}
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", body, true, nil), 200)
	f.start(t, f.conversation(t), "plain")
	if f.runtime.requests[1]["advanced_tools"] != false {
		t.Fatal("advanced_tools should be false after disabling")
	}
	if _, ok := f.runtime.requests[1]["workspace"]; ok {
		t.Fatal("workspace should be omitted when disabled")
	}
}

func TestDeploymentKillSwitchOverridesAgentSetting(t *testing.T) {
	t.Setenv("SIAGENT_DISABLE_ADVANCED_TOOLS", "true")
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	agent, _ := f.server.agentByID("default")
	body := map[string]any{"name": "默认助手", "instructions": agent.Instructions, "enabled": true, "model_id": agent.ModelID, "policy": map[string]any{"advanced_tools": true}}
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", body, true, nil), 409)

	response := f.request(t, "GET", "/api/agents", nil, true, nil)
	requireStatus(t, response, 200)
	var agents []Agent
	json.Unmarshal(response.Body.Bytes(), &agents)
	if len(agents) != 1 || agents[0].AdvancedToolsAvailable {
		t.Fatalf("availability not reported: %s", response.Body.String())
	}

	// A policy written before the switch was enabled must not reach the runtime.
	if _, err := f.store.db.Exec("UPDATE agents SET policy=? WHERE id='default'", `{"advanced_tools":true}`); err != nil {
		t.Fatal(err)
	}
	f.start(t, f.conversation(t), "should not get tools")
	if f.runtime.requests[0]["advanced_tools"] != false {
		t.Fatal("kill switch did not override the agent policy")
	}
}
