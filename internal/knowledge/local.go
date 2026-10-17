package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type LocalProvider struct {
	db   *sql.DB
	root string
}

func (p *LocalProvider) Search(ctx context.Context, scope Scope, request SearchRequest) (SearchResult, error) {
	started := time.Now()
	tokens, err := tokenize(request.Query)
	if err != nil {
		return SearchResult{}, err
	}
	terms := []string{}
	for _, term := range strings.Fields(tokens) {
		if !slices.Contains(terms, term) {
			terms = append(terms, term)
		}
	}
	if len(terms) == 0 {
		return SearchResult{}, invalid("请输入可检索的文字或数字，查询不能只有标点或停用词。")
	}
	if len(terms) > 32 {
		return SearchResult{}, invalid("检索词过多，请缩短问题后重试。")
	}
	trace := &Trace{Query: request.Query, Terms: terms, KnowledgeID: append([]string{}, scope.KnowledgeIDs...)}
	if request.Trace {
		trace.Stages = append(trace.Stages, TraceStage{Name: "tokenize", Detail: strconv.Itoa(len(terms)) + " terms", Hits: len(terms), Duration: time.Since(started)})
	}
	excerptLimit := 600
	if request.Compact {
		excerptLimit = 140
		if len(terms) > 2 {
			excerptLimit = 220
		}
	}
	// Rule-based expansion adds synonyms without calling a model. The original
	// query always ranks first, so a weak variant cannot outrank a direct hit.
	queries := expandQueries(request.Query)
	if request.Trace {
		trace.Stages = append(trace.Stages, TraceStage{Name: "expand", Detail: strings.Join(queries, " | "), Hits: len(queries), Duration: time.Since(started)})
	}
	lists := [][]Hit{}
	sections := map[string]string{}
	matchMode := "all_terms"
	for index, query := range queries {
		stageStart := time.Now()
		hits, mode, secs, err := p.ftsQuery(ctx, scope, query, request.Limit, terms, excerptLimit)
		if err != nil {
			if index == 0 {
				return SearchResult{}, err
			}
			if request.Trace {
				trace.Stages = append(trace.Stages, TraceStage{Name: "fts_failed", Detail: query, Duration: time.Since(stageStart)})
			}
			continue
		}
		if index == 0 {
			matchMode = mode
		}
		for id, section := range secs {
			sections[id] = section
		}
		if request.Trace {
			trace.Stages = append(trace.Stages, TraceStage{Name: "fts_" + mode, Detail: query, Hits: len(hits), Duration: time.Since(stageStart)})
			for _, hit := range hits {
				if len(trace.Candidates) >= 50 {
					break
				}
				entry := TraceHit{SourceID: hit.SourceID, DocumentID: hit.DocumentID, Title: hit.Title, MatchedTerm: matchedTerms(hit.Text, terms)}
				if hit.Score != nil {
					entry.Score = *hit.Score
				}
				trace.Candidates = append(trace.Candidates, entry)
			}
		}
		if len(hits) > 0 {
			lists = append(lists, hits)
		}
	}
	fused := fuseResults(lists)
	if len(fused) > 0 {
		boostStart := time.Now()
		structureBoost(request.Query, fused, sections)
		if request.Trace {
			trace.Stages = append(trace.Stages, TraceStage{Name: "rrf_and_boost", Detail: strconv.Itoa(len(lists)) + " ranked lists", Hits: len(fused), Duration: time.Since(boostStart)})
		}
	}
	if len(fused) > request.Limit {
		fused = fused[:request.Limit]
	}
	if fused == nil {
		fused = []Hit{}
	}
	result := SearchResult{Hits: fused, MatchMode: matchMode}
	if request.Trace {
		trace.MatchMode = matchMode
		trace.Total = len(fused)
		trace.Selected = make([]TraceHit, 0, len(fused))
		for index, hit := range fused {
			entry := TraceHit{SourceID: hit.SourceID, DocumentID: hit.DocumentID, Title: hit.Title, Rank: index + 1, MatchedTerm: matchedTerms(hit.Text, terms)}
			if hit.Score != nil {
				entry.Score = *hit.Score
			}
			trace.Selected = append(trace.Selected, entry)
		}
		trace.Duration = time.Since(started)
		trace.Stages = append(trace.Stages, TraceStage{Name: "total", Hits: len(fused), Duration: trace.Duration})
		result.Trace = trace
	}
	return result, nil
}

// ftsQuery runs one FTS5 query, trying all terms first and falling back to any
// term. It returns the hits, the match mode actually used and the section path
// of each returned chunk.
func (p *LocalProvider) ftsQuery(ctx context.Context, scope Scope, query string, limit int, excerptTerms []string, excerptLimit int) ([]Hit, string, map[string]string, error) {
	tokens, err := tokenize(query)
	if err != nil {
		return nil, "", nil, err
	}
	terms := []string{}
	for _, term := range strings.Fields(tokens) {
		if !slices.Contains(terms, term) {
			terms = append(terms, term)
		}
	}
	if len(terms) == 0 {
		return nil, "", nil, nil
	}
	if len(terms) > 32 {
		terms = terms[:32]
	}
	quoted := make([]string, len(terms))
	for index, term := range terms {
		quoted[index] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	budget := limit * 4
	if budget < 10 {
		budget = 10
	}
	if budget > 40 {
		budget = 40
	}
	sections := map[string]string{}
	for _, mode := range []struct{ name, operator string }{{"all_terms", " AND "}, {"any_terms", " OR "}} {
		hits := []Hit{}
		placeholders := make([]string, len(scope.KnowledgeIDs))
		args := []any{strings.Join(quoted, mode.operator)}
		for index, id := range scope.KnowledgeIDs {
			placeholders[index] = "?"
			args = append(args, id)
		}
		args = append(args, budget)
		rows, err := p.db.QueryContext(ctx, `SELECT c.id,c.knowledge_id,c.document_id,c.version,c.position,d.title,c.text,c.location,c.section_path,bm25(knowledge_fts,0,3,1) FROM knowledge_fts JOIN knowledge_chunks c ON c.id=knowledge_fts.chunk_id JOIN knowledge_documents d ON d.id=c.document_id JOIN knowledge_bases k ON k.id=c.knowledge_id WHERE knowledge_fts MATCH ? AND d.deleted=0 AND k.deleted=0 AND c.version=d.active_version AND c.knowledge_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY bm25(knowledge_fts,0,3,1),c.id LIMIT ?`, args...)
		if err != nil {
			return nil, "", nil, err
		}
		for rows.Next() {
			var hit Hit
			var raw, section string
			var score float64
			var position int
			if err := rows.Scan(&hit.SourceID, &hit.KnowledgeID, &hit.DocumentID, &hit.Version, &position, &hit.Title, &hit.Text, &raw, &section, &score); err != nil {
				rows.Close()
				return nil, "", nil, err
			}
			if err := json.Unmarshal([]byte(raw), &hit.Location); err != nil {
				rows.Close()
				return nil, "", nil, err
			}
			hit.SourceURL = sourceURL(hit.SourceID)
			hit.Score = &score
			sections[hit.SourceID] = section
			hit.Text = excerpt(hit.Text, excerptTerms, excerptLimit)
			hits = append(hits, hit)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, "", nil, err
		}
		if len(hits) > 0 || mode.name == "any_terms" {
			return hits, mode.name, sections, nil
		}
	}
	return nil, "any_terms", sections, nil
}

// matchedTerms reports which query terms appear in the returned excerpt. It is
// used only for diagnostics and never changes ranking.
func matchedTerms(text string, terms []string) []string {
	lower := strings.ToLower(text)
	matched := []string{}
	for _, term := range terms {
		if strings.Contains(lower, strings.ToLower(term)) {
			matched = append(matched, term)
		}
	}
	return matched
}
func excerpt(text string, terms []string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	lower := strings.ToLower(text)
	lowerRunes := []rune(lower)
	start, best := 0, -1
	// Candidate windows at each term preserve nearby context and favor coverage of
	// distinct query terms instead of always showing only the earliest keyword.
	candidates := []int{0}
	for _, term := range terms {
		if index := strings.Index(lower, term); index >= 0 {
			position := utf8.RuneCountInString(lower[:index])
			candidates = append(candidates, position-limit/3, position, position-limit+utf8.RuneCountInString(term))
		}
	}
	for _, candidate := range candidates {
		candidate = max(0, min(candidate, len(runes)-limit))
		window := string(lowerRunes[candidate : candidate+limit])
		score := 0
		for _, term := range terms {
			if strings.Contains(window, term) {
				score++
			}
		}
		if score > best {
			start, best = candidate, score
		}
	}
	end := start + limit
	fragment := string(runes[start:end])
	if start > 0 {
		fragment = "…" + fragment
	}
	if end < len(runes) {
		fragment += "…"
	}
	return fragment
}
func parseSource(id string) (string, int, int, error) {
	parts := strings.Split(id, ".")
	if len(parts) != 3 || !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`).MatchString(parts[0]) {
		return "", 0, 0, invalid("来源标识无效。")
	}
	version, err := strconv.Atoi(parts[1])
	if err != nil || version < 1 {
		return "", 0, 0, invalid("来源标识无效。")
	}
	position, err := strconv.Atoi(parts[2])
	if err != nil || position < 0 {
		return "", 0, 0, invalid("来源标识无效。")
	}
	return parts[0], version, position, nil
}
func (p *LocalProvider) Read(ctx context.Context, scope Scope, request ReadRequest) (Source, error) {
	id, version, position, err := parseSource(request.SourceID)
	if err != nil {
		return Source{}, err
	}
	var source Source
	var total, active int
	err = p.db.QueryRowContext(ctx, `SELECT d.knowledge_id,d.title,d.deleted OR k.deleted,d.active_version,v.chunks FROM knowledge_documents d JOIN knowledge_bases k ON k.id=d.knowledge_id JOIN knowledge_versions v ON v.document_id=d.id WHERE d.id=? AND v.version=?`, id, version).Scan(&source.KnowledgeID, &source.Title, &source.Deleted, &active, &total)
	if err == sql.ErrNoRows {
		return source, missing()
	}
	if err != nil {
		return source, err
	}
	if !slices.Contains(scope.KnowledgeIDs, source.KnowledgeID) {
		return source, &Fault{"forbidden", "不能读取未授权知识库中的文档。"}
	}
	if position >= total {
		return source, missing()
	}
	source.SourceID = request.SourceID
	source.SourceURL = sourceURL(request.SourceID)
	source.DocumentID = id
	source.Version = version
	source.Historical = active != version
	if source.Deleted {
		return source, nil
	}
	var raw string
	err = p.db.QueryRowContext(ctx, "SELECT text,location FROM knowledge_chunks WHERE id=? AND document_id=? AND version=?", request.SourceID, id, version).Scan(&source.Text, &raw)
	if err == sql.ErrNoRows {
		return source, unavailable("来源片段暂不可读取，请检查文档处理状态。")
	}
	if err != nil {
		return source, err
	}
	if err := json.Unmarshal([]byte(raw), &source.Location); err != nil {
		return source, err
	}
	if request.Offset < 0 || request.Limit < 0 || request.Limit > 12000 {
		return source, invalid("原文读取范围无效，单次最多 12,000 个字符。")
	}
	if request.Limit == 0 {
		request.Limit = 4000
	}
	data, err := os.ReadFile(filepath.Join(p.root, id, strconv.Itoa(version), "text.json"))
	if err != nil {
		return source, unavailable("来源原文不可读取，请检查文件或从完整备份恢复。")
	}
	if len(data) > 8<<20 {
		return source, unavailable("来源原文超过读取上限。")
	}
	var pages []page
	if json.Unmarshal(data, &pages) != nil {
		return source, unavailable("来源原文格式无效，请重新索引或恢复完整备份。")
	}
	texts := make([]string, len(pages))
	for index, page := range pages {
		texts[index] = page.Text
	}
	runes := []rune(strings.Join(texts, "\n"))
	source.TotalCharacters = len(runes)
	if request.Offset > len(runes) {
		return source, invalid("原文读取起点超出范围。")
	}
	end := min(request.Offset+request.Limit, len(runes))
	source.DocumentText = string(runes[request.Offset:end])
	source.Offset = request.Offset
	if end < len(runes) {
		source.NextOffset = &end
	}
	return source, nil
}

var _ Provider = (*LocalProvider)(nil)
