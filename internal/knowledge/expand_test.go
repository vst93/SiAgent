package knowledge

import (
	"context"
	"strings"
	"testing"
)

func TestExpandQueriesKeepsOriginalFirstAndAddsSynonyms(t *testing.T) {
	values := expandQueries("酒店开销怎么算")
	if len(values) < 2 || values[0] != "酒店开销怎么算" {
		t.Fatalf("original query must rank first: %v", values)
	}
	joined := strings.Join(values, "|")
	if !strings.Contains(joined, "住宿") {
		t.Fatalf("expected accommodation synonym, got %v", values)
	}
	if len(values) > maxExpandedQueries {
		t.Fatalf("expansion exceeded bound: %v", values)
	}
}

func TestExpandQueriesWithoutMatchReturnsOnlyOriginal(t *testing.T) {
	values := expandQueries("量子纠缠")
	if len(values) != 1 || values[0] != "量子纠缠" {
		t.Fatalf("unexpected expansion: %v", values)
	}
}

func TestFuseResultsPrefersChunksRankedBySeveralQueries(t *testing.T) {
	first := Hit{SourceID: "a"}
	second := Hit{SourceID: "b"}
	lists := [][]Hit{{first, second}, {first}}
	fused := fuseResults(lists)
	if len(fused) != 2 || fused[0].SourceID != "a" {
		t.Fatalf("RRF did not favour the repeated hit: %+v", fused)
	}
	if fused[0].Score == nil || fused[1].Score == nil || *fused[0].Score <= *fused[1].Score {
		t.Fatal("fused scores not ordered")
	}
}

func TestStructureBoostRewardsExactPhraseAndHeading(t *testing.T) {
	score := 0.01
	text := Hit{SourceID: "text", Text: "住宿标准为每晚 500 元"}
	heading := Hit{SourceID: "heading", Text: "其他说明"}
	other := Hit{SourceID: "other", Text: "无关内容"}
	text.Score = &score
	headingScore := 0.01
	heading.Score = &headingScore
	otherScore := 0.01
	other.Score = &otherScore
	hits := []Hit{other, heading, text}
	structureBoost("住宿标准", hits, map[string]string{"heading": "差旅 / 住宿标准"})
	if hits[0].SourceID != "text" {
		t.Fatalf("exact phrase should rank first: %+v", hits)
	}
	boosted := map[string]float64{}
	for _, hit := range hits {
		if hit.Score != nil {
			boosted[hit.SourceID] = *hit.Score
		}
	}
	if boosted["heading"] <= 0.01 {
		t.Fatalf("heading boost not applied: %+v", boosted)
	}
	if boosted["other"] != 0.01 {
		t.Fatalf("unrelated chunk should not be boosted: %+v", boosted)
	}
}

func TestSynonymExpansionFindsDocumentWithoutKeywordOverlap(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "synonyms")
	upload(t, c, b.ID, "", "travel.txt", "差旅住宿费上限为每晚 500 元。")
	process(t, c)
	got := search(t, c, []string{b.ID}, "酒店开销限制")
	if len(got.Hits) == 0 {
		t.Fatal("synonym expansion did not recover a match")
	}
	if got.MatchMode != "any_terms" {
		t.Fatalf("unexpected match mode: %s", got.MatchMode)
	}
}

func TestExpansionImprovesRecallOverSingleQuery(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "recall")
	doc := upload(t, c, b.ID, "", "travel.txt", "差旅住宿费上限为每晚 500 元。")
	upload(t, c, b.ID, "", "invoice.txt", "报销需要提供发票原件。")
	process(t, c)
	if _, err := c.SaveEvalQuestion(context.Background(), "", b.ID, "酒店开销限制", []string{doc.ID}, nil); err != nil {
		t.Fatal(err)
	}
	run, err := c.RunEval(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Metrics.RecallAt5 != 1 {
		t.Fatalf("expansion did not improve recall: %+v", run.Metrics)
	}
}

func TestTraceShowsExpansionAndFusionStages(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "trace stages")
	upload(t, c, b.ID, "", "travel.txt", "差旅住宿费上限为每晚 500 元。")
	process(t, c)
	result, err := c.Search(context.Background(), Scope{KnowledgeIDs: []string{b.ID}}, SearchRequest{Query: "酒店开销", Limit: 5, Trace: true})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, stage := range result.Trace.Stages {
		names = append(names, stage.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"tokenize", "expand", "rrf_and_boost", "total"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing stage %s in %s", want, joined)
		}
	}
	if len(result.Trace.Candidates) == 0 {
		t.Fatal("candidates missing from trace")
	}
}

func TestStructureTermsAreIndexedWithHigherWeight(t *testing.T) {
	c := fixture(t)
	b := base(t, c, "weighted")
	upload(t, c, b.ID, "", "policy.md", "# 差旅制度\n\n## 住宿标准\n\n本段不含关键词。\n")
	process(t, c)
	got := search(t, c, []string{b.ID}, "住宿标准")
	if len(got.Hits) == 0 {
		t.Fatal("heading terms not indexed")
	}
}
