package knowledge

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSentenceChunksKeepCoverageAndSourceCoordinates(t *testing.T) {
	text := strings.Repeat("甲", 450) + "。" + strings.Repeat("乙", 450) + "。" + strings.Repeat("丙", 900)
	chunks, characters, err := split(context.Background(), "doc", 2, []page{{Number: 3, Text: text}})
	runes := []rune(text)
	covered := make([]bool, len(runes))
	if err != nil || characters != len(runes) || len(chunks) < 3 {
		t.Fatal("split failed", err)
	}
	if chunks[0].Location.End != 451 {
		t.Fatal("sentence boundary ignored")
	}
	for index, c := range chunks {
		location := c.Location
		if location.Page != 3 || location.Paragraph != 1 || location.End-location.Start > 600 || c.Text != string(runes[location.Start:location.End]) || c.ID != sourceID("doc", 2, index) {
			t.Fatal("invalid source coordinates", c)
		}
		if index > 0 && chunks[index-1].Location.End-location.Start != 80 {
			t.Fatal("overlap or progress lost")
		}
		for i := location.Start; i < location.End; i++ {
			covered[i] = true
		}
	}
	for _, present := range covered {
		if !present {
			t.Fatal("text dropped between chunks")
		}
	}
	english := strings.Repeat("A sentence with complete words and a price of 3.14. ", 30)
	chunks, _, err = split(context.Background(), "english", 1, []page{{Text: english}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(chunks[0].Text, ".") {
		t.Fatal("English sentence cut in the middle")
	}
	decimal := []rune(strings.Repeat("a", 400) + "." + strings.Repeat("x", 197) + "3.14" + strings.Repeat("y", 200))
	if end := chunkEnd(decimal, 0); end != 401 {
		t.Fatal("decimal point treated as a sentence boundary", end)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := split(ctx, "cancelled", 1, []page{{Text: text}}); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestCompactExcerptCoversQueryTermsAndPreservesFullSource(t *testing.T) {
	text := strings.Repeat("甲", 100) + "发票" + strings.Repeat("乙", 118) + "订单" + strings.Repeat("丙", 400)
	preview := excerpt(text, []string{"发票", "订单"}, 140)
	if !strings.Contains(preview, "发票") || !strings.Contains(preview, "订单") || utf8.RuneCountInString(preview) > 142 {
		t.Fatal("preview missed jointly relevant terms", preview)
	}
	c := fixture(t)
	kb := base(t, c, "摘要")
	upload(t, c, kb.ID, "", "rules.txt", strings.Repeat("背景资料说明。", 70)+"订单核销应在当日处理。"+strings.Repeat("其他规定。", 70))
	process(t, c)
	compact, err := c.Search(context.Background(), Scope{[]string{kb.ID}}, SearchRequest{Query: "核销", Compact: true})
	if err != nil || len(compact.Hits) == 0 {
		t.Fatal("missing preview", err)
	}
	hit := compact.Hits[0]
	if utf8.RuneCountInString(hit.Text) > 142 || !strings.Contains(hit.Text, "核销") {
		t.Fatal("single term preview too large or lost keyword")
	}
	full, err := c.Read(context.Background(), Scope{[]string{kb.ID}}, ReadRequest{SourceID: hit.SourceID})
	if err != nil || utf8.RuneCountInString(full.Text) <= utf8.RuneCountInString(hit.Text) || !strings.Contains(full.Text, "核销") {
		t.Fatal("preview truncated the actual source", err)
	}
	agent, err := c.Search(context.Background(), Scope{[]string{kb.ID}}, SearchRequest{Query: "核销"})
	if err != nil || len(agent.Hits) == 0 || len(agent.Hits[0].Text) <= len(hit.Text) {
		t.Fatal("management preview reduced Agent context", err)
	}
}
