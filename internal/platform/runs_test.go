package platform

import (
	"encoding/json"
	"testing"

	agentruntime "siagent/internal/runtime"
)

func TestRunLimitsAndUncertainRuntimeExit(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "private-key")
	first := f.conversation(t)
	second := f.conversation(t)
	third := f.conversation(t)
	firstRun := f.start(t, first, "first")
	secondRun := f.start(t, second, "second")
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+third+"/runs", map[string]string{"input": "third"}, true, nil), 202)
	if err := f.server.HandleEvent(agentruntime.Event{Type: "message_delta", RunID: firstRun, Sequence: 1, Data: json.RawMessage(`{"text":"partial"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.HandleEvent(agentruntime.Event{Type: "runtime_unavailable"}); err != nil {
		t.Fatal(err)
	}
	// A late completion cannot turn an uncertain, interrupted platform run into success.
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: firstRun, Sequence: 2, Data: json.RawMessage(`{"answer":"late"}`)}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.server.snapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runs[0].Status != "interrupted" || snapshot.Messages[1].Content != "partial" {
		t.Fatalf("uncertain result was overwritten: %+v", snapshot)
	}
	var status string
	if err := f.store.db.QueryRow("SELECT status FROM runs WHERE id=?", secondRun).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" {
		t.Fatal("other active run was not interrupted")
	}
	if err := f.server.HandleEvent(agentruntime.Event{Type: "ready"}); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/agent", map[string]any{"name": "paused", "instructions": "instruction", "enabled": false}, true, nil), 200)
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+third+"/runs", map[string]string{"input": "blocked"}, true, nil), 409)
}
