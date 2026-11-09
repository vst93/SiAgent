package platform

import (
	"encoding/json"
	"strconv"
	"testing"
)

// Purging a channel is irreversible, so it is gated on the archived state and on
// typing the name back. It removes channel bookkeeping only; conversations stay
// with their Agent.
func TestChannelPurgeRequiresArchiveAndName(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	agent := f.agent(t, "客服 A")
	response := f.request(t, "POST", "/api/channels", map[string]any{
		"name":     "客服群",
		"provider": "wecom-bot",
		"config":   map[string]string{"bot_id": "support-bot"},
		"secrets":  map[string]string{"secret": "private-key"},
		"agent_id": agent,
		"enabled":  false,
	}, true, nil)
	requireStatus(t, response, 201)
	var channel Channel
	json.Unmarshal(response.Body.Bytes(), &channel)

	// Not archived yet.
	requireStatus(t, f.request(t, "DELETE", "/api/channels/"+channel.ID+"/purge", map[string]string{"confirm": "客服群"}, true, nil), 409)
	requireStatus(t, f.request(t, "DELETE", "/api/channels/"+channel.ID, nil, true, nil), 200)
	// Wrong name.
	requireStatus(t, f.request(t, "DELETE", "/api/channels/"+channel.ID+"/purge", map[string]string{"confirm": "错名字"}, true, nil), 400)
	requireStatus(t, f.request(t, "DELETE", "/api/channels/"+channel.ID+"/purge", map[string]string{"confirm": "客服群"}, true, nil), 200)

	var remaining int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channels WHERE id=?", channel.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("the purged channel row must be gone")
	}
	// Channel-scoped bookkeeping goes with it.
	for _, table := range []string{"channel_inbox", "channel_outbox", "channel_sessions"} {
		if err := f.store.db.QueryRow("SELECT count(*) FROM "+table+" WHERE channel_id=?", channel.ID).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("%s rows must be removed with the channel", table)
		}
	}
	// The Agent that owned it survives.
	if err := f.store.db.QueryRow("SELECT count(*) FROM agents WHERE id=?", agent).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("purging a channel must not touch its Agent: %d %v", remaining, err)
	}
}

// A model connection can be purged only after archiving, and only once no Agent
// still points at it.
func TestModelPurgeRequiresArchiveAndNoReferences(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	response := f.request(t, "POST", "/api/models", map[string]any{
		"base_url": "http://localhost:9998/v1",
		"name":     "备用模型",
		"api_key":  "key",
	}, true, nil)
	requireStatus(t, response, 201)
	var model Model
	json.Unmarshal(response.Body.Bytes(), &model)

	// A live Agent still points at it, so archiving is refused first.
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", map[string]any{"name": "默认助手", "enabled": true, "model_id": model.ID, "instructions": "x"}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/models/"+strconv.FormatInt(model.ID, 10), nil, true, nil), 409)
	// Point the Agent back and archive.
	requireStatus(t, f.request(t, "PUT", "/api/agents/default", map[string]any{"name": "默认助手", "enabled": true, "model_id": 1, "instructions": "x"}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/models/"+strconv.FormatInt(model.ID, 10), nil, true, nil), 200)
	// Purge needs the exact name.
	requireStatus(t, f.request(t, "DELETE", "/api/models/"+strconv.FormatInt(model.ID, 10)+"/purge", map[string]string{"confirm": "错"}, true, nil), 400)
	requireStatus(t, f.request(t, "DELETE", "/api/models/"+strconv.FormatInt(model.ID, 10)+"/purge", map[string]string{"confirm": "备用模型"}, true, nil), 200)
	var remaining int
	if err := f.store.db.QueryRow("SELECT count(*) FROM models WHERE id=?", model.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("the purged model row must be gone")
	}
}
