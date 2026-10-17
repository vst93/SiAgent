package knowledge

import (
	"sort"
	"strings"
)

// maxExpandedQueries bounds rule-based expansion so latency stays predictable.
const maxExpandedQueries = 4

// synonymGroups is a small, domain-neutral business vocabulary. It is applied
// only as query expansion: the index is never rewritten, so an unrelated pair
// can at worst add a weak candidate that RRF ranks below the original query.
var synonymGroups = [][]string{
	{"报销", "费用核销", "报账"},
	{"出差", "差旅"},
	{"住宿", "住宿费", "酒店"},
	{"发票", "票据"},
	{"工资", "薪资", "薪酬"},
	{"假期", "休假", "请假"},
	{"加班", "延时工作"},
	{"转正", "试用期转正"},
	{"离职", "解除劳动合同"},
	{"采购", "购买"},
	{"合同", "协议"},
	{"客户", "顾客"},
	{"退款", "退费"},
	{"密码", "口令"},
	{"登录", "登入", "登陆"},
	{"限额", "上限", "额度"},
	{"流程", "办理流程", "步骤"},
	{"申请", "提交申请"},
	{"审批", "审核", "批准"},
	{"标准", "规定", "要求"},
	{"制度", "管理办法", "规范"},
	{"培训", "学习", "课程"},
	{"绩效", "考核"},
	{"晋升", "晋级"},
	{"社保", "五险一金"},
	{"体检", "健康检查"},
}

// expandQueries returns the original query first, then rule-based variants.
// It never calls a model, so a slow or unavailable provider cannot affect
// retrieval.
func expandQueries(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	values := []string{query}
	lower := strings.ToLower(query)
	seen := map[string]bool{strings.ToLower(query): true}
	for _, group := range synonymGroups {
		if len(values) >= maxExpandedQueries {
			break
		}
		for _, term := range group {
			if !strings.Contains(lower, strings.ToLower(term)) {
				continue
			}
			for _, replacement := range group {
				if replacement == term || len(values) >= maxExpandedQueries {
					continue
				}
				variant := replaceFold(query, term, replacement)
				key := strings.ToLower(variant)
				if seen[key] {
					continue
				}
				seen[key] = true
				values = append(values, variant)
			}
			break
		}
	}
	return values
}

// replaceFold replaces term case-insensitively while preserving the rest of the
// query verbatim.
func replaceFold(query, term, replacement string) string {
	lower := strings.ToLower(query)
	needle := strings.ToLower(term)
	index := strings.Index(lower, needle)
	if index < 0 {
		return query
	}
	runes := []rune(query)
	start := len([]rune(query[:index]))
	end := start + len([]rune(term))
	if end > len(runes) {
		return query
	}
	return string(runes[:start]) + replacement + string(runes[end:])
}

// rrfConstant is the standard reciprocal-rank-fusion constant.
const rrfConstant = 60.0

// fuseResults merges ranked lists with reciprocal rank fusion, keeping the best
// BM25 score seen for each chunk.
func fuseResults(lists [][]Hit) []Hit {
	type entry struct {
		hit   Hit
		score float64
	}
	merged := map[string]*entry{}
	order := []string{}
	for _, list := range lists {
		for rank, hit := range list {
			item, ok := merged[hit.SourceID]
			if !ok {
				copy := hit
				item = &entry{hit: copy}
				merged[hit.SourceID] = item
				order = append(order, hit.SourceID)
			}
			item.score += 1 / (rrfConstant + float64(rank+1))
			if hit.Score != nil && (item.hit.Score == nil || *hit.Score < *item.hit.Score) {
				item.hit.Score = hit.Score
			}
		}
	}
	fused := make([]Hit, 0, len(order))
	for _, id := range order {
		item := merged[id]
		score := item.score
		item.hit.Score = &score
		fused = append(fused, item.hit)
	}
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].Score == nil || fused[j].Score == nil {
			return fused[i].SourceID < fused[j].SourceID
		}
		return *fused[i].Score > *fused[j].Score
	})
	return fused
}

// structureBoost re-ranks fused candidates using signals the BM25 index cannot
// express: exact phrase, clause numbers and section headings.
func structureBoost(query string, hits []Hit, sections map[string]string) {
	phrase := strings.ToLower(strings.TrimSpace(query))
	for index := range hits {
		if hits[index].Score == nil {
			continue
		}
		score := *hits[index].Score
		text := strings.ToLower(hits[index].Text)
		if phrase != "" && strings.Contains(text, phrase) {
			score *= 1.35
		}
		section := strings.ToLower(sections[hits[index].SourceID])
		if section != "" && phrase != "" && strings.Contains(section, phrase) {
			score *= 1.25
		} else if section != "" {
			for _, term := range strings.Fields(phrase) {
				if len([]rune(term)) >= 2 && strings.Contains(section, term) {
					score *= 1.12
					break
				}
			}
		}
		hits[index].Score = &score
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == nil || hits[j].Score == nil {
			return hits[i].SourceID < hits[j].SourceID
		}
		return *hits[i].Score > *hits[j].Score
	})
}
