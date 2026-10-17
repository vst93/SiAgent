package knowledge

import (
	"context"
	"testing"
)

func TestSearchTraceReportsTermsScoresAndSelection(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "trace")
	d := upload(t, c, b.ID, "", "policy.txt", "出差住宿标准为每晚 500 元。\n\n报销需要发票。")
	process(t, c)
	result, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "住宿标准", Limit: 3, Trace: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace == nil {
		t.Fatal("trace not returned")
	}
	if result.Trace.Query != "住宿标准" || len(result.Trace.Terms) == 0 {
		t.Fatalf("trace query or terms missing: %+v", result.Trace)
	}
	if result.Trace.MatchMode != result.MatchMode {
		t.Fatalf("trace match mode %q != %q", result.Trace.MatchMode, result.MatchMode)
	}
	if len(result.Trace.Stages) < 2 {
		t.Fatalf("expected staged timings, got %+v", result.Trace.Stages)
	}
	if len(result.Trace.Selected) != len(result.Hits) {
		t.Fatalf("selected %d trace hits for %d results", len(result.Trace.Selected), len(result.Hits))
	}
	if len(result.Trace.Selected) > 0 {
		first := result.Trace.Selected[0]
		if first.Rank != 1 || first.DocumentID != d.ID || first.Title != "policy.txt" {
			t.Fatalf("unexpected first trace hit: %+v", first)
		}
		if first.SourceID != result.Hits[0].SourceID {
			t.Fatalf("trace hit does not match returned source")
		}
	}
	if result.Trace.Duration <= 0 {
		t.Fatal("trace duration missing")
	}
}

func TestSearchWithoutTraceKeepsPayloadSmall(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "plain")
	upload(t, c, b.ID, "", "policy.txt", "出差住宿标准为每晚 500 元。")
	process(t, c)
	result, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "住宿", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace != nil {
		t.Fatal("trace should be omitted unless requested")
	}
}

func TestEvalScoresRecallAndPersistsRun(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "eval")
	policy := upload(t, c, b.ID, "", "travel.txt", "出差住宿标准为每晚 500 元，超标部分自理。")
	upload(t, c, b.ID, "", "invoice.txt", "报销需要提供发票原件。")
	process(t, c)
	if _, err := c.SaveEvalQuestion(context.Background(), "", b.ID, "住宿标准是多少", []string{policy.ID}, []string{"差旅"}); err != nil {
		t.Fatal(err)
	}
	run, err := c.RunEval(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Metrics.Questions != 1 || run.Metrics.RecallAt5 != 1 {
		t.Fatalf("expected a hit, got %+v", run.Metrics)
	}
	if run.Metrics.MRR != 1 {
		t.Fatalf("expected rank 1, got MRR %v", run.Metrics.MRR)
	}
	if len(run.Cases) != 1 || !run.Cases[0].Pass || run.Cases[0].Ranked[0] != policy.ID {
		t.Fatalf("unexpected case result: %+v", run.Cases)
	}
	stored, err := c.EvalRun(context.Background(), run.ID)
	if err != nil || stored.Metrics.Questions != 1 || len(stored.Cases) != 1 {
		t.Fatalf("stored run missing: %+v %v", stored, err)
	}
	runs, err := c.EvalRuns(context.Background(), b.ID, 5)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("run history missing: %+v %v", runs, err)
	}
}

func TestEvalReportsMissWhenExpectedDocumentNotRanked(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "miss")
	upload(t, c, b.ID, "", "travel.txt", "出差住宿标准为每晚 500 元。")
	other := upload(t, c, b.ID, "", "invoice.txt", "报销需要提供发票原件。")
	process(t, c)
	if _, err := c.SaveEvalQuestion(context.Background(), "", b.ID, "住宿标准是多少", []string{other.ID}, nil); err != nil {
		t.Fatal(err)
	}
	run, err := c.RunEval(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Metrics.RecallAt5 != 0 || run.Metrics.MRR != 0 {
		t.Fatalf("expected a miss, got %+v", run.Metrics)
	}
	if run.Cases[0].Pass {
		t.Fatal("miss should not pass")
	}
}

func TestEvalRejectsForeignExpectedDocument(t *testing.T) {
	c := fixture(t)
	first := base(t, c, "first")
	second := base(t, c, "second")
	doc := upload(t, c, second.ID, "", "other.txt", "无关内容。")
	process(t, c)
	if _, err := c.SaveEvalQuestion(context.Background(), "", first.ID, "问题", []string{doc.ID}, nil); err == nil {
		t.Fatal("expected cross-base document to be rejected")
	}
}

func TestEvalRequiresQuestions(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "empty")
	if _, err := c.RunEval(context.Background(), b.ID); err == nil {
		t.Fatal("expected an error without questions")
	}
}

func TestRecordTraceKeepsBoundedDiagnostics(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "traces")
	upload(t, c, b.ID, "", "policy.txt", "出差住宿标准为每晚 500 元。")
	process(t, c)
	result, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "住宿", Trace: true})
	if err != nil {
		t.Fatal(err)
	}
	c.RecordTrace(context.Background(), b.ID, result.Trace)
	var count int
	if err := c.db.QueryRow("SELECT count(*) FROM retrieval_traces").Scan(&count); err != nil || count != 1 {
		t.Fatalf("trace not recorded: %d %v", count, err)
	}
	c.PruneTraces(context.Background(), 0)
	if err := c.db.QueryRow("SELECT count(*) FROM retrieval_traces").Scan(&count); err != nil || count != 0 {
		t.Fatalf("trace not pruned: %d %v", count, err)
	}
}
