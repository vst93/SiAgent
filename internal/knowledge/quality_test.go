package knowledge

import (
	"context"
	"strings"
	"testing"
)

func TestEvalCSVAndJSONRoundTrip(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "io")
	doc := upload(t, c, b.ID, "", "policy.txt", "住宿标准为每晚 500 元。")
	process(t, c)
	csv := "question,expected_document_ids,tags,answerable,expected_answer\n" +
		"住宿标准是多少," + doc.ID + ",差旅;财务,true,每晚 500 元\n" +
		"火星办公室地址, , ,false,\n"
	rows, err := ParseEvalCSV(strings.NewReader(csv))
	if err != nil || len(rows) != 2 {
		t.Fatalf("csv parse: %v %v", rows, err)
	}
	imported, failures := c.ImportEvalQuestions(context.Background(), b.ID, rows)
	if imported != 2 || len(failures) != 0 {
		t.Fatalf("import: %d %v", imported, failures)
	}
	questions, err := c.EvalQuestions(context.Background(), b.ID)
	if err != nil || len(questions) != 2 {
		t.Fatalf("questions: %v %v", questions, err)
	}
	if questions[1].Answerable {
		t.Fatal("unanswerable flag lost")
	}
	exported := FormatEvalCSV(questions)
	again, err := ParseEvalCSV(strings.NewReader(exported))
	if err != nil || len(again) != 2 || again[0].Question != "住宿标准是多少" {
		t.Fatalf("csv roundtrip: %v %v", again, err)
	}
	jsonRows, err := ParseEvalJSON([]byte(`[{"question":"q","expected_document_ids":["` + doc.ID + `"],"answerable":true}]`))
	if err != nil || len(jsonRows) != 1 {
		t.Fatalf("json parse: %v %v", jsonRows, err)
	}
}

func TestEvalScoresAbstentionForUnanswerableQuestions(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "abstain")
	upload(t, c, b.ID, "", "policy.txt", "住宿标准为每晚 500 元。")
	process(t, c)
	answerable := false
	if _, err := c.SaveEvalQuestionFull(context.Background(), "", b.ID, "火星办公室在哪里", nil, nil, answerable, ""); err != nil {
		t.Fatal(err)
	}
	run, err := c.RunEval(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Metrics.AbstentionAcc != 1 {
		t.Fatalf("expected abstention 1, got %+v", run.Metrics)
	}
	if !run.Cases[0].Pass {
		t.Fatal("unanswerable question should pass when retrieval is empty")
	}
}

func TestConflictDetectionFindsDifferentNumbersAndIsIdempotent(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "conflict")
	upload(t, c, b.ID, "", "a.txt", "住宿标准为每晚 500 元。")
	upload(t, c, b.ID, "", "b.txt", "住宿标准为每晚 600 元。")
	process(t, c)
	found, err := c.DetectConflicts(context.Background(), b.ID)
	if err != nil || found != 1 {
		t.Fatalf("detect: %d %v", found, err)
	}
	again, err := c.DetectConflicts(context.Background(), b.ID)
	if err != nil || again != 0 {
		t.Fatalf("idempotency: %d %v", again, err)
	}
	values, err := c.Conflicts(context.Background(), b.ID, "open")
	if err != nil || len(values) != 1 {
		t.Fatalf("list: %v %v", values, err)
	}
	if err := c.SetConflictStatus(context.Background(), values[0].ID, "resolved"); err != nil {
		t.Fatal(err)
	}
	open, _ := c.Conflicts(context.Background(), b.ID, "open")
	if len(open) != 0 {
		t.Fatal("resolved conflict still open")
	}
}

func TestOutdatedSourcesDetectNewerVersion(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "outdated")
	doc := upload(t, c, b.ID, "", "policy.txt", "住宿标准为每晚 500 元。")
	process(t, c)
	hit := search(t, c, []string{b.ID}, "住宿")
	if len(hit.Hits) == 0 {
		t.Fatal("no hit")
	}
	if _, err := c.db.Exec("CREATE TABLE IF NOT EXISTS runs(id TEXT PRIMARY KEY); INSERT INTO runs(id) VALUES('run-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.Exec("INSERT INTO run_sources(run_id,source_id,reference) VALUES('run-1',?,?)", hit.Hits[0].SourceID, `{"document_id":"`+doc.ID+`","version":1,"source_id":"`+hit.Hits[0].SourceID+`","title":"policy.txt"}`); err != nil {
		t.Fatal(err)
	}
	upload(t, c, b.ID, doc.ID, "policy.txt", "住宿标准为每晚 600 元。")
	process(t, c)
	values, err := c.OutdatedSources(context.Background(), b.ID, 10)
	if err != nil || len(values) != 1 || values[0].CitedVersion != 1 || values[0].ActiveVersion != 2 {
		t.Fatalf("outdated: %+v %v", values, err)
	}
}

func TestRegressionGateDetectsDrop(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "gate")
	doc := upload(t, c, b.ID, "", "policy.txt", "住宿标准为每晚 500 元。")
	process(t, c)
	if _, err := c.SaveEvalQuestion(context.Background(), "", b.ID, "住宿标准", []string{doc.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunEval(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	regressed, err := c.LastEvalRegressed(context.Background(), b.ID)
	if err != nil || regressed {
		t.Fatalf("first run must not regress: %v %v", regressed, err)
	}
	// Simulate a later run with a lower score.
	metrics := `{"questions":1,"recall_at_5":0.2,"mrr":0.2,"previous_recall_at_5":1,"regressed":true}`
	if _, err := c.db.Exec("INSERT INTO knowledge_eval_runs(id,knowledge_id,created,metrics,cases,kind) VALUES('regress',?,?,?, '[]','retrieval')", b.ID, 9999999999999, metrics); err != nil {
		t.Fatal(err)
	}
	regressed, err = c.LastEvalRegressed(context.Background(), b.ID)
	if err != nil || !regressed {
		t.Fatalf("regression not detected: %v %v", regressed, err)
	}
}

func TestCustomVocabularyExpandsQueries(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "vocab")
	upload(t, c, b.ID, "", "policy.txt", "差旅住宿费上限为每晚 500 元。")
	process(t, c)
	if err := c.SaveVocabulary(context.Background(), b.ID, "酒店开销", "住宿费"); err != nil {
		t.Fatal(err)
	}
	got := search(t, c, []string{b.ID}, "酒店开销")
	if len(got.Hits) == 0 {
		t.Fatal("custom vocabulary did not expand the query")
	}
	entries, err := c.Vocabulary(context.Background(), b.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("vocabulary: %v %v", entries, err)
	}
	if err := c.DeleteVocabulary(context.Background(), b.ID, "酒店开销"); err != nil {
		t.Fatal(err)
	}
}

func TestVocabularyCandidatesSurfaceFrequentTerms(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "candidates")
	upload(t, c, b.ID, "", "policy.txt", strings.Repeat("差旅住宿费上限为每晚 500 元。", 5))
	process(t, c)
	values, err := c.VocabCandidates(context.Background(), b.ID)
	if err != nil || len(values) == 0 {
		t.Fatalf("candidates: %v %v", values, err)
	}
	found := false
	for _, value := range values {
		if strings.Contains(value.Term, "住宿") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected accommodation term in candidates: %+v", values)
	}
}

func TestDisabledExpansionFallsBackToSingleQuery(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "noexpand")
	upload(t, c, b.ID, "", "policy.txt", "差旅住宿费上限为每晚 500 元。")
	process(t, c)
	disabled := false
	if _, err := c.SaveBaseConfig(context.Background(), b.ID, BaseConfig{Expand: disabled}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "酒店开销限制", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hits) != 0 {
		t.Fatal("expansion should be disabled by base config")
	}
	enabled := true
	if _, err := c.SaveBaseConfig(context.Background(), b.ID, BaseConfig{Expand: enabled}); err != nil {
		t.Fatal(err)
	}
	got, err = c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "酒店开销限制", Limit: 5})
	if err != nil || len(got.Hits) == 0 {
		t.Fatalf("expansion should recover the match: %+v %v", got, err)
	}
}
