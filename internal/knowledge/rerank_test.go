package knowledge

import (
	"context"
	"errors"
	"testing"
)

type stubReranker struct {
	order []string
	err   error
	calls int
	size  int
}

func (s *stubReranker) Rerank(_ context.Context, _ string, hits []Hit) ([]string, error) {
	s.calls++
	s.size = len(hits)
	return s.order, s.err
}

func TestClassifyQueryUsesStructuralIntent(t *testing.T) {
	cases := map[string]string{
		"住宿标准是多少":   QueryTable,
		"报销制度怎么规定的": QueryPolicy,
		"请假流程怎么走":   QueryProcedure,
		"这两个方案有什么区别": QueryComparison,
		"差旅和报销分别怎么处理": QueryMultiHop,
		"公司地址在哪里":   QueryFact,
	}
	for query, want := range cases {
		if got := classifyQuery(query); got != want {
			t.Fatalf("%q classified as %s, want %s", query, got, want)
		}
	}
}

func TestTableQueryBoostsTableChunk(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "tables")
	upload(t, c, b.ID, "", "policy.md", "# 住宿\n\n住宿需要提前申请。\n\n| 职级 | 标准 |\n| --- | --- |\n| P5 | 400 |\n")
	process(t, c)
	got := search(t, c, []string{b.ID}, "住宿标准是多少")
	if len(got.Hits) == 0 {
		t.Fatal("no hits")
	}
	var kind string
	if err := c.db.QueryRow("SELECT block_type FROM knowledge_chunks WHERE id=?", got.Hits[0].SourceID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != blockTable {
		t.Fatalf("table chunk should rank first, got %s", kind)
	}
}

func TestRerankReordersAndRecordsTrace(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "rerank")
	upload(t, c, b.ID, "", "standard.txt", "住宿标准为每晚 500 元。")
	upload(t, c, b.ID, "", "apply.txt", "住宿需要提前申请。")
	upload(t, c, b.ID, "", "monthly.txt", "住宿费按月结算。")
	process(t, c)
	baseline, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "住宿", Limit: 5, Trace: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Hits) < 2 {
		t.Fatal("need at least two hits")
	}
	reversed := []string{}
	for index := len(baseline.Hits) - 1; index >= 0; index-- {
		reversed = append(reversed, baseline.Hits[index].SourceID)
	}
	stub := &stubReranker{order: reversed}
	c.SetReranker(stub)
	result, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "住宿", Limit: 5, Trace: true, Rerank: true})
	if err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 || stub.size > rerankLimit {
		t.Fatalf("rerank call unexpected: calls=%d size=%d", stub.calls, stub.size)
	}
	if result.Hits[0].SourceID != reversed[0] {
		t.Fatal("model order not applied")
	}
	if result.Trace.Rerank == nil || result.Trace.Rerank.Mode != "llm" || len(result.Trace.Rerank.After) != len(result.Hits) {
		t.Fatalf("rerank trace missing: %+v", result.Trace.Rerank)
	}
	if result.Trace.Rerank.Before[0] == result.Trace.Rerank.After[0] {
		t.Fatal("rerank did not change order")
	}
}

func TestRerankFailureFallsBackToHeuristicOrder(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "rerank fail")
	upload(t, c, b.ID, "", "standard.txt", "住宿标准为每晚 500 元。")
	upload(t, c, b.ID, "", "apply.txt", "住宿需要提前申请。")
	process(t, c)
	baseline, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "住宿", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReranker(&stubReranker{err: errors.New("timeout")})
	result, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "住宿", Limit: 5, Trace: true, Rerank: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Hits[0].SourceID != baseline.Hits[0].SourceID {
		t.Fatal("failure should keep heuristic order")
	}
	if result.Trace.Rerank == nil || result.Trace.Rerank.Fallback != "heuristic" {
		t.Fatalf("fallback not traced: %+v", result.Trace.Rerank)
	}
}

func TestRerankWithoutModelIsTraced(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "no model")
	upload(t, c, b.ID, "", "standard.txt", "住宿标准为每晚 500 元。")
	upload(t, c, b.ID, "", "apply.txt", "住宿需要提前申请。")
	process(t, c)
	result, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "住宿", Limit: 5, Trace: true, Rerank: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.Rerank == nil || result.Trace.Rerank.Fallback != "unavailable" {
		t.Fatalf("missing reranker should be traced: %+v", result.Trace.Rerank)
	}
}

func TestReorderKeepsOmittedCandidates(t *testing.T) {
	hits := []Hit{{SourceID: "a"}, {SourceID: "b"}, {SourceID: "c"}}
	ordered := reorder(hits, []string{"c", "unknown", "a", "c"})
	if len(ordered) != 3 || ordered[0].SourceID != "c" || ordered[1].SourceID != "a" || ordered[2].SourceID != "b" {
		t.Fatalf("unexpected order: %+v", ordered)
	}
}
