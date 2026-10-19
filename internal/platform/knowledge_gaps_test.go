package platform

import (
	"encoding/json"
	"strconv"
	"testing"

	"siagent/internal/knowledge"
)

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
