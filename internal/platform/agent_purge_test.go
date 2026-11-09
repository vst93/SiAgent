package platform

import (
	"encoding/json"
	"testing"
)

// The default Agent is a bootstrap convenience, not an identity, and "no live
// Agent" is a valid state: the UI then shows no Agent and disables new chats.
func TestDefaultAgentCanBeArchivedEvenWhenLast(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", map[string]any{"name": "默认助手", "enabled": false, "model_id": 1, "instructions": "x"}, true, nil), 200)
	// Archiving the only Agent is allowed.
	requireStatus(t, f.request(t, "DELETE", "/api/agents/default", nil, true, nil), 200)
	var archived bool
	if err := f.store.db.QueryRow("SELECT archived FROM agents WHERE id='default'").Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if !archived {
		t.Fatal("the default Agent should be archived")
	}
	var live int
	if err := f.store.db.QueryRow("SELECT count(*) FROM agents WHERE archived=0").Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("expected no live Agents, got %d", live)
	}
	// A model save must not resurrect an archived Agent.
	requireStatus(t, f.request(t, "POST", "/api/models", map[string]any{"base_url": "http://localhost:9999/v1", "name": "second", "api_key": "k"}, true, nil), 201)
	if err := f.store.db.QueryRow("SELECT count(*) FROM agents WHERE archived=0").Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("saving a model must not create a live Agent again: %d", live)
	}
}

// With every Agent archived, the list still returns them so history is reachable.
func TestArchivedAgentsRemainListed(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", map[string]any{"name": "默认助手", "enabled": false, "model_id": 1, "instructions": "x"}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/default", nil, true, nil), 200)
	w := f.request(t, "GET", "/api/agents", nil, true, nil)
	requireStatus(t, w, 200)
	var agents []Agent
	json.Unmarshal(w.Body.Bytes(), &agents)
	if len(agents) != 1 || !agents[0].Archived {
		t.Fatalf("the archived Agent must stay listed: %+v", agents)
	}
}

// Archived Agents sort after live ones so history does not sit among peers.
func TestAgentListSortsArchivedLast(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	first := f.agent(t, "活跃 A")
	second := f.agent(t, "活跃 B")
	// Archive the first one; it must move behind the live one.
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+first, map[string]any{"name": "活跃 A", "enabled": false, "model_id": 1, "instructions": "x"}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+first, nil, true, nil), 200)
	w := f.request(t, "GET", "/api/agents", nil, true, nil)
	requireStatus(t, w, 200)
	var agents []Agent
	json.Unmarshal(w.Body.Bytes(), &agents)
	seenArchived := false
	for _, agent := range agents {
		if agent.Archived {
			seenArchived = true
		} else if seenArchived {
			t.Fatalf("a live Agent appeared after an archived one: %+v", agents)
		}
	}
	if !seenArchived {
		t.Fatal("the archived Agent must still be listed")
	}
	_ = second
}

// Purging needs an archived Agent, the exact name, and leaves other Agents alone.
func TestAgentPurgeRequiresArchiveAndName(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	keep := f.agent(t, "保留助手")
	target := f.agent(t, "待删除助手")
	// Not archived yet.
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+target+"/purge", map[string]string{"confirm": "待删除助手"}, true, nil), 409)
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+target, map[string]any{"name": "待删除助手", "enabled": false, "model_id": 1, "instructions": "x"}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+target, nil, true, nil), 200)
	// Wrong name.
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+target+"/purge", map[string]string{"confirm": "错名字"}, true, nil), 400)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+target+"/purge", map[string]string{"confirm": "待删除助手"}, true, nil), 200)
	var remaining int
	if err := f.store.db.QueryRow("SELECT count(*) FROM agents WHERE id=?", target).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("the purged Agent row must be gone")
	}
	// The other Agent is untouched.
	if err := f.store.db.QueryRow("SELECT count(*) FROM agents WHERE id=?", keep).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("other Agents must survive a purge: %d %v", remaining, err)
	}
}

// A purged Agent takes its authorization and knowledge bindings with it.
func TestAgentPurgeRemovesBindings(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	f.agent(t, "保留助手")
	target := f.agent(t, "待删除助手")
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+target+"/capabilities", map[string]any{"skills": []string{}, "mcps": []MCPBinding{}, "confirm": true}, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+target, map[string]any{"name": "待删除助手", "enabled": false, "model_id": 1, "instructions": "x"}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+target, nil, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+target+"/purge", map[string]string{"confirm": "待删除助手"}, true, nil), 200)
	var count int
	if err := f.store.db.QueryRow("SELECT count(*) FROM agent_capabilities WHERE agent_id=?", target).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("capability bindings must be removed with the Agent")
	}
	if err := f.store.db.QueryRow("SELECT count(*) FROM knowledge_bindings WHERE agent_id=?", target).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("knowledge bindings must be removed with the Agent")
	}
}
