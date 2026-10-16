package knowledge

import (
	"context"
	"strings"
	"testing"
)

func benchmarkText() string {
	return strings.Repeat("发票申请需要提供订单编号和客户信息。财务复核通过后，在七个工作日内开具发票。退款申请需核实原始支付记录，具体进度可咨询客户服务。", 700)
}
func BenchmarkKnowledgeIndex(b *testing.B) {
	c := fixture(b)
	kb := base(b, c, "资料")
	text := benchmarkText()
	if _, err := tokenize("预热中文词典"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		upload(b, c, kb.ID, "", "rules.txt", text)
		process(b, c)
	}
}
func BenchmarkKnowledgeSearch(b *testing.B) {
	c := fixture(b)
	kb := base(b, c, "资料")
	upload(b, c, kb.ID, "", "rules.txt", benchmarkText())
	process(b, c)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Search(context.Background(), Scope{[]string{kb.ID}}, SearchRequest{Query: "发票", Limit: 5}); err != nil {
			b.Fatal(err)
		}
	}
}
