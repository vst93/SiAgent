package knowledge

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"
)

// VocabCandidate is a frequently occurring term that is not yet in the base
// vocabulary. Administrators use it to grow the domain dictionary.
type VocabCandidate struct {
	Term  string `json:"term"`
	Count int    `json:"count"`
}

// VocabCandidates extracts frequent terms from active chunks. It is a
// discovery aid only: nothing is added to the index or vocabulary automatically.
func (c *Core) VocabCandidates(ctx context.Context, baseID string) ([]VocabCandidate, error) {
	known := map[string]bool{}
	entries, err := c.Vocabulary(ctx, baseID)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		known[strings.ToLower(entry.Term)] = true
	}
	rows, err := c.db.QueryContext(ctx, `SELECT c.text FROM knowledge_chunks c JOIN knowledge_documents d ON d.id=c.document_id WHERE c.knowledge_id=? AND d.deleted=0 AND c.version=d.active_version LIMIT 400`, baseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		tokens, err := tokenize(text)
		if err != nil {
			return nil, err
		}
		for _, token := range strings.Fields(tokens) {
			length := utf8.RuneCountInString(token)
			if length < 2 || length > 8 || known[strings.ToLower(token)] {
				continue
			}
			counts[token]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := []VocabCandidate{}
	for term, count := range counts {
		if count >= 3 {
			values = append(values, VocabCandidate{Term: term, Count: count})
		}
	}
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Count != values[j].Count {
			return values[i].Count > values[j].Count
		}
		return values[i].Term < values[j].Term
	})
	if len(values) > 20 {
		values = values[:20]
	}
	return values, nil
}
