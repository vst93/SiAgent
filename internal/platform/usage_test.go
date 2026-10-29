package platform

import (
	"encoding/json"
	"testing"

	agentruntime "siagent/internal/runtime"
)

func TestUsageSummaryAndConversationTotals(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "usage-model", "usage-secret")
	conversation := f.conversation(t)
	if _, err := f.store.db.Exec(`INSERT INTO runs(id,conversation_id,status,config,created) VALUES
		('usage-run-1',?,'completed','{}',1000),
		('usage-run-2',?,'completed','{}',2000)`, conversation, conversation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`INSERT INTO run_usage(run_id,conversation_id,agent_id,model,input_tokens,output_tokens,total_tokens,cost,reported,created) VALUES
		('usage-run-1',?,'default','model-a',10,5,15,0.02,1,1000),
		('usage-run-2',?,'default','',0,0,0,0,0,2000)`, conversation, conversation); err != nil {
		t.Fatal(err)
	}
	response := f.request(t, "GET", "/api/usage?from=1&to=10000", nil, true, nil)
	requireStatus(t, response, 200)
	var summary usageSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Totals.Input != 10 || summary.Totals.Output != 5 || summary.Totals.Total != 15 {
		t.Fatalf("unexpected totals: %+v", summary.Totals)
	}
	if summary.Totals.Calls != 2 || summary.Totals.ReportedCalls != 1 {
		t.Fatalf("unexpected calls: %+v", summary.Totals)
	}
	if len(summary.Series) == 0 {
		t.Fatal("expected a series bucket")
	}
	if len(summary.Models) != 2 || summary.Models[0].Model != "model-a" || summary.Models[0].Total != 15 {
		t.Fatalf("unexpected models: %+v", summary.Models)
	}
	if len(summary.Conversations) != 1 || summary.Conversations[0].Total != 15 || summary.Conversations[0].Title != "新对话" {
		t.Fatalf("unexpected conversations: %+v", summary.Conversations)
	}

	list := f.request(t, "GET", "/api/conversations", nil, true, nil)
	requireStatus(t, list, 200)
	var conversations []Conversation
	if err := json.Unmarshal(list.Body.Bytes(), &conversations); err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 1 || conversations[0].UsageTotal != 15 || conversations[0].UsageInput != 10 || conversations[0].UsageOutput != 5 {
		t.Fatalf("conversation usage missing: %+v", conversations)
	}

	all := f.request(t, "GET", "/api/usage?from=0&to=10000", nil, true, nil)
	requireStatus(t, all, 200)
	requireStatus(t, f.request(t, "GET", "/api/usage?from=20000&to=10000", nil, true, nil), 400)
	requireStatus(t, f.request(t, "GET", "/api/usage?from=bad", nil, true, nil), 400)
}

func TestRunCompletedStoresProviderUsage(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "usage-model", "usage-secret")
	conversation := f.conversation(t)
	if _, err := f.store.db.Exec(`INSERT INTO runs(id,conversation_id,status,config,created,released) VALUES('usage-run',?,'running','{}',1000,0)`, conversation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`INSERT INTO run_usage(run_id,conversation_id,agent_id,model,reported,created) VALUES('usage-run',?,'default','primary',0,1000)`, conversation); err != nil {
		t.Fatal(err)
	}
	data := json.RawMessage(`{"usage":{"input":7,"output":3,"cache_read":2,"cache_write":1,"total":10,"cost":0.01,"reported":true,"model":"fallback-model"}}`)
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: "usage-run", Sequence: 1, Data: data}); err != nil {
		t.Fatal(err)
	}
	var input, output, total int64
	var model string
	var reported int
	if err := f.store.db.QueryRow("SELECT input_tokens,output_tokens,total_tokens,model,reported FROM run_usage WHERE run_id='usage-run'").Scan(&input, &output, &total, &model, &reported); err != nil {
		t.Fatal(err)
	}
	if input != 7 || output != 3 || total != 10 || model != "fallback-model" || reported != 1 {
		t.Fatalf("unexpected stored usage: input=%d output=%d total=%d model=%q reported=%d", input, output, total, model, reported)
	}
}
