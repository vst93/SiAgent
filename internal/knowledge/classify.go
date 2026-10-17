package knowledge

import (
	"strings"
	"unicode/utf8"
)

// Query types drive small, explainable ranking adjustments. They are inferred
// from the question text with fixed rules, never from a model.
const (
	QueryFact        = "fact"
	QueryPolicy      = "policy"
	QueryProcedure   = "procedure"
	QueryTable       = "table"
	QueryComparison  = "comparison"
	QueryMultiHop    = "multi_hop"
	QueryUnspecified = "unspecified"
)

var (
	tableHints      = []string{"多少", "表格", "标准是", "限额", "上限", "价格", "费用", "比例", "天数", "金额"}
	policyHints     = []string{"规定", "制度", "条款", "是否允许", "可不可以", "能不能", "依据", "要求", "标准"}
	procedureHints  = []string{"流程", "步骤", "怎么办理", "如何申请", "先", "然后", "手续"}
	comparisonHints = []string{"区别", "对比", "哪个", "相比", "不同", "差异"}
	multiHopHints   = []string{"以及", "同时", "分别", "另外", "还需要", "并且"}
)

// classifyQuery returns one query type. Order matters: structural intent beats
// generic wording, so "报销标准是多少" is a table question, not a policy one.
func classifyQuery(query string) string {
	lower := strings.ToLower(strings.TrimSpace(query))
	if lower == "" {
		return QueryUnspecified
	}
	if containsAny(lower, comparisonHints) {
		return QueryComparison
	}
	if containsAny(lower, multiHopHints) {
		return QueryMultiHop
	}
	if containsAny(lower, procedureHints) {
		return QueryProcedure
	}
	if containsAny(lower, tableHints) {
		return QueryTable
	}
	if containsAny(lower, policyHints) {
		return QueryPolicy
	}
	if utf8.RuneCountInString(lower) <= 6 {
		return QueryUnspecified
	}
	return QueryFact
}

func containsAny(value string, hints []string) bool {
	for _, hint := range hints {
		if strings.Contains(value, hint) {
			return true
		}
	}
	return false
}

// typeBoost returns the multiplier applied to a chunk whose block type matches
// the inferred query type.
func typeBoost(queryType, blockType string) float64 {
	switch queryType {
	case QueryTable:
		if blockType == blockTable {
			return 1.25
		}
	case QueryPolicy:
		if blockType == blockClause {
			return 1.2
		}
	case QueryProcedure:
		if blockType == blockList {
			return 1.15
		}
	}
	return 1
}
