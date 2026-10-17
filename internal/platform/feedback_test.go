package platform

import (
	"encoding/json"
	"strconv"
	"testing"

	"siagent/internal/knowledge"
	agentruntime "siagent/internal/runtime"
)

func TestFeedbackRequiresFinishedRunAndValidReason(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	conversation := f.conversation(t)
	runID := f.start(t, conversation, "hello")

	requireStatus(t, f.request(t, "POST", "/api/runs/"+runID+"/feedback", map[string]string{"rating": "up"}, true, nil), 409)
	requireStatus(t, f.request(t, "POST", "/api/runs/missing/feedback", map[string]string{"rating": "up"}, true, nil), 404)

	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: runID, Sequence: 1, Data: json.RawMessage(`{"answer":"answer"}`)}); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "POST", "/api/runs/"+runID+"/feedback", map[string]string{"rating": "sideways"}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/runs/"+runID+"/feedback", map[string]string{"rating": "down", "reason": "bogus"}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/runs/"+runID+"/feedback", map[string]string{"rating": "down", "reason": "no_evidence", "comment": "缺少依据"}, true, nil), 200)

	snapshot, err := f.server.snapshot(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Runs[0].Feedback != "down" {
		t.Fatalf("feedback not exposed: %+v", snapshot.Runs[0])
	}

	response := f.request(t, "GET", "/api/feedback/stats", nil, true, nil)
	requireStatus(t, response, 200)
	var stats struct {
		Up      int            `json:"up"`
		Down    int            `json:"down"`
		Reasons map[string]int `json:"reasons"`
	}
	json.Unmarshal(response.Body.Bytes(), &stats)
	if stats.Down != 1 || stats.Reasons["no_evidence"] != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	// Re-rating replaces the previous value instead of duplicating it.
	requireStatus(t, f.request(t, "POST", "/api/runs/"+runID+"/feedback", map[string]string{"rating": "up"}, true, nil), 200)
	response = f.request(t, "GET", "/api/feedback/stats", nil, true, nil)
	json.Unmarshal(response.Body.Bytes(), &stats)
	if stats.Up != 1 || stats.Down != 0 {
		t.Fatalf("rating not replaced: %+v", stats)
	}
	requireStatus(t, f.request(t, "DELETE", "/api/runs/"+runID+"/feedback", nil, true, nil), 200)
	snapshot, _ = f.server.snapshot(conversation)
	if snapshot.Runs[0].Feedback != "" {
		t.Fatal("feedback not cleared")
	}
}

func TestEmptySearchRecordsContentGap(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	b, err := f.server.knowledge.SaveBase(t.Context(), "", "Gaps", "")
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.request(t, "POST", "/api/knowledge/search", map[string]any{"query": "不存在的主题", "knowledge_ids": []string{b.ID}}, true, nil), 200)
	response := f.request(t, "GET", "/api/knowledge/gaps?knowledge_id="+b.ID+"&status=open", nil, true, nil)
	requireStatus(t, response, 200)
	var gaps []knowledge.ContentGap
	json.Unmarshal(response.Body.Bytes(), &gaps)
	if len(gaps) != 1 || gaps[0].Query != "不存在的主题" || gaps[0].Count != 1 {
		t.Fatalf("gap not recorded: %s", response.Body.String())
	}
	// Repeating the same question increments instead of duplicating.
	requireStatus(t, f.request(t, "POST", "/api/knowledge/search", map[string]any{"query": "不存在的主题", "knowledge_ids": []string{b.ID}}, true, nil), 200)
	response = f.request(t, "GET", "/api/knowledge/gaps?knowledge_id="+b.ID, nil, true, nil)
	json.Unmarshal(response.Body.Bytes(), &gaps)
	if len(gaps) != 1 || gaps[0].Count != 2 {
		t.Fatalf("gap not aggregated: %+v", gaps)
	}
	gapID := itoa(gaps[0].ID)
	requireStatus(t, f.request(t, "POST", "/api/knowledge/gaps/"+gapID+"/status", map[string]string{"status": "resolved"}, true, nil), 200)
	response = f.request(t, "GET", "/api/knowledge/gaps?knowledge_id="+b.ID+"&status=open", nil, true, nil)
	json.Unmarshal(response.Body.Bytes(), &gaps)
	if len(gaps) != 0 {
		t.Fatal("resolved gap still listed as open")
	}
	requireStatus(t, f.request(t, "DELETE", "/api/knowledge/gaps/"+gapID, nil, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/knowledge/gaps/"+gapID, nil, true, nil), 404)
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
