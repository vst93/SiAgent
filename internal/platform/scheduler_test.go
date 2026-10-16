package platform

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentruntime "siagent/internal/runtime"
)

func requestsOf(f *fixture, kind string) []map[string]any {
	result := []map[string]any{}
	for _, r := range f.runtime.requests {
		if r["type"] == kind {
			result = append(result, r)
		}
	}
	return result
}
func emit(t *testing.T, f *fixture, runID, kind string, seq int64) {
	t.Helper()
	if err := f.server.HandleEvent(agentruntime.Event{Type: kind, RunID: runID, Sequence: seq, Data: json.RawMessage(`{"answer":"answer"}`)}); err != nil {
		t.Fatal(err)
	}
}
func TestFIFOReleaseAndQueuedConfigurationSnapshot(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "old-model", "old-key")
	conversation := f.conversation(t)
	first := f.start(t, conversation, "one")
	second := f.start(t, conversation, "two")
	third := f.start(t, conversation, "three")
	if len(requestsOf(f, "start")) != 1 {
		t.Fatal("same conversation ran concurrently")
	}
	f.model(t, "new-model", "new-key")
	emit(t, f, first, "run_started", 1)
	emit(t, f, first, "run_completed", 2)
	if len(requestsOf(f, "start")) != 1 {
		t.Fatal("next task started before storage release")
	}
	emit(t, f, first, "run_released", 3)
	starts := requestsOf(f, "start")
	if len(starts) != 2 || starts[1]["run_id"] != second {
		t.Fatal("FIFO violated")
	}
	if starts[1]["model"].(map[string]any)["api_key"] != "old-key" {
		t.Fatal("queued credential snapshot changed")
	}
	emit(t, f, second, "run_started", 1)
	emit(t, f, second, "run_completed", 2)
	emit(t, f, second, "run_released", 3)
	starts = requestsOf(f, "start")
	if len(starts) != 3 || starts[2]["run_id"] != third {
		t.Fatal("third task did not start")
	}
	var credential string
	if err := f.store.db.QueryRow("SELECT credential FROM runs WHERE id=?", first).Scan(&credential); err != nil {
		t.Fatal(err)
	}
	if credential != "" {
		t.Fatal("terminal task retained its credential snapshot")
	}
}
func TestBoundedQueueAndQueuedCancellation(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	l := defaultLimits()
	l.Concurrent = 1
	l.ConversationQueue = 1
	l.GlobalQueue = 2
	requireStatus(t, f.request(t, "PUT", "/api/limits", l, true, nil), 200)
	a := f.conversation(t)
	b := f.conversation(t)
	c := f.conversation(t)
	f.start(t, a, "running")
	queued := f.start(t, a, "queued")
	f.start(t, b, "other queued")
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+a+"/runs", map[string]string{"input": "overflow conversation"}, true, nil), 429)
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+c+"/runs", map[string]string{"input": "overflow global"}, true, nil), 429)
	requireStatus(t, f.request(t, "POST", "/api/runs/"+queued+"/cancel", nil, true, nil), 200)
	if len(requestsOf(f, "cancel")) != 0 {
		t.Fatal("cancelling queued work contacted Runtime")
	}
	f.start(t, c, "new queued")
	snapshot, err := f.server.snapshot(a)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runs[1].Status != "cancelled" {
		t.Fatal("queued cancellation not saved")
	}
}
func TestIndependentConversationsAndTimeoutWatchdog(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	f.server.limits.RunTimeout = 1
	a := f.conversation(t)
	b := f.conversation(t)
	c := f.conversation(t)
	first := f.start(t, a, "one")
	f.start(t, b, "two")
	f.start(t, c, "three")
	if len(requestsOf(f, "start")) != 2 {
		t.Fatal("global concurrency was not applied")
	}
	var started int64
	if err := f.store.db.QueryRow("SELECT started FROM runs WHERE id=?", first).Scan(&started); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(started).Add(2 * time.Second)
	if err := f.server.Tick(now); err != nil {
		t.Fatal(err)
	}
	if len(requestsOf(f, "cancel")) != 2 {
		t.Fatal("timed out work was not cancelled")
	}
	snapshot, _ := f.server.snapshot(a)
	if snapshot.Runs[0].Status != "cancelling" {
		t.Fatal("timeout assumed operation had stopped")
	}
	if err := f.server.Tick(now.Add(11 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if f.runtime.restarts != 1 {
		t.Fatal("unresponsive peer was not restarted")
	}
	snapshot, _ = f.server.snapshot(c)
	if snapshot.Runs[0].Status != "interrupted" {
		t.Fatal("uncertain peer restart silently resumed queued work")
	}
}
func TestMissingEventsAndDiagnosticsAreConservative(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "do-not-expose")
	conversation := f.conversation(t)
	run := f.start(t, conversation, "private message")
	if err := f.server.HandleEvent(agentruntime.Event{Type: "tool_start", RunID: run, Sequence: 1, Data: json.RawMessage(`{"toolName":"external_write","toolCallId":"call","args":{"api_key":"do-not-expose"}}`)}); err != nil {
		t.Fatal(err)
	}
	emit(t, f, run, "run_completed", 3)
	snapshot, _ := f.server.snapshot(conversation)
	if snapshot.Runs[0].Status != "interrupted" {
		t.Fatal("missing event was inferred as success")
	}
	if len(requestsOf(f, "cancel")) != 1 {
		t.Fatal("uncertain work was not stopped")
	}
	emit(t, f, run, "run_released", 4)
	requireStatus(t, f.request(t, "GET", "/api/runs/"+run, nil, false, nil), 401)
	w := f.request(t, "GET", "/api/runs/"+run, nil, true, nil)
	requireStatus(t, w, 200)
	body := w.Body.String()
	if strings.Contains(body, "do-not-expose") || strings.Contains(body, "private message") {
		t.Fatal("diagnostics leaked payload or credential")
	}
	if !strings.Contains(body, "external_write") || !strings.Contains(body, "interrupted") || !strings.Contains(body, "event_gap") {
		t.Fatal("missing uncertain tool evidence")
	}
}
func TestStatusReconciliationNeverInventsSuccess(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	conversation := f.conversation(t)
	run := f.start(t, conversation, "one")
	second := f.start(t, conversation, "two")
	data, _ := json.Marshal(map[string]any{"run_id": run, "status": "completed", "released": 1, "seq": 10})
	if err := f.server.HandleEvent(agentruntime.Event{Type: "status", RequestID: "status-" + run, Data: data}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := f.server.snapshot(conversation)
	if snapshot.Runs[0].Status != "interrupted" {
		t.Fatal("status query invented a successful result")
	}
	starts := requestsOf(f, "start")
	if len(starts) != 2 || starts[1]["run_id"] != second {
		t.Fatal("confirmed release did not free queue")
	}
}
