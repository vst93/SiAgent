package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Conflict is a pair of active chunks with the same wording but different
// numbers, which usually means two documents disagree.
type Conflict struct {
	ID          int64  `json:"id"`
	KnowledgeID string `json:"knowledge_id"`
	DocumentA   string `json:"document_a"`
	DocumentB   string `json:"document_b"`
	Detail      string `json:"detail"`
	Status      string `json:"status"`
	Created     int64  `json:"created"`
}

// OutdatedSource is a citation whose document has a newer published version.
type OutdatedSource struct {
	RunID         string `json:"run_id"`
	SourceID      string `json:"source_id"`
	DocumentID    string `json:"document_id"`
	Title         string `json:"title"`
	CitedVersion  int    `json:"cited_version"`
	ActiveVersion int    `json:"active_version"`
}

var digits = regexp.MustCompile(`[0-9０-９]+`)

// DetectConflicts compares active chunks that share the same wording skeleton
// but different numbers. It is bounded and idempotent: an existing pair is not
// recorded twice.
func (c *Core) DetectConflicts(ctx context.Context, baseID string) (int, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT c.document_id,c.text,d.title FROM knowledge_chunks c JOIN knowledge_documents d ON d.id=c.document_id WHERE c.knowledge_id=? AND d.deleted=0 AND c.version=d.active_version ORDER BY c.document_id,c.position LIMIT 4000`, baseID)
	if err != nil {
		return 0, err
	}
	type entry struct {
		document, title, text, skeleton, numbers string
	}
	entries := []entry{}
	for rows.Next() {
		var value entry
		if err := rows.Scan(&value.document, &value.text, &value.title); err != nil {
			rows.Close()
			return 0, err
		}
		value.numbers = strings.Join(uniqueStrings(digits.FindAllString(value.text, -1)), ",")
		value.skeleton = strings.TrimSpace(digits.ReplaceAllString(value.text, "#"))
		if value.numbers == "" {
			continue
		}
		entries = append(entries, value)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].skeleton != entries[j].skeleton {
			return entries[i].skeleton < entries[j].skeleton
		}
		return entries[i].document < entries[j].document
	})
	found := 0
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].skeleton != entries[i].skeleton {
				break
			}
			if entries[j].document == entries[i].document || entries[j].numbers == entries[i].numbers {
				continue
			}
			detail := truncateRunes(entries[i].text, 120) + " ↔ " + truncateRunes(entries[j].text, 120)
			var exists int
			if err := c.db.QueryRowContext(ctx, "SELECT count(*) FROM knowledge_conflicts WHERE knowledge_id=? AND document_a=? AND document_b=? AND detail=?", baseID, entries[i].document, entries[j].document, detail).Scan(&exists); err != nil {
				return found, err
			}
			if exists > 0 {
				continue
			}
			if _, err := c.db.ExecContext(ctx, "INSERT INTO knowledge_conflicts(knowledge_id,document_a,document_b,detail,created) VALUES(?,?,?,?,?)", baseID, entries[i].document, entries[j].document, detail, time.Now().UnixMilli()); err != nil {
				return found, err
			}
			found++
		}
	}
	return found, nil
}

func (c *Core) Conflicts(ctx context.Context, baseID, status string) ([]Conflict, error) {
	query := "SELECT id,knowledge_id,document_a,document_b,detail,status,created FROM knowledge_conflicts WHERE knowledge_id=?"
	args := []any{baseID}
	if status == "open" || status == "resolved" || status == "ignored" {
		query += " AND status=?"
		args = append(args, status)
	}
	query += " ORDER BY created DESC,id DESC LIMIT 200"
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Conflict{}
	for rows.Next() {
		var value Conflict
		if err := rows.Scan(&value.ID, &value.KnowledgeID, &value.DocumentA, &value.DocumentB, &value.Detail, &value.Status, &value.Created); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (c *Core) SetConflictStatus(ctx context.Context, id int64, status string) error {
	if status != "open" && status != "resolved" && status != "ignored" {
		return invalid("冲突状态无效。")
	}
	result, err := c.db.ExecContext(ctx, "UPDATE knowledge_conflicts SET status=? WHERE id=?", status, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return missing()
	}
	return nil
}

// OutdatedSources lists citations that point at an older published version.
func (c *Core) OutdatedSources(ctx context.Context, baseID string, limit int) ([]OutdatedSource, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := `SELECT s.run_id,s.reference,d.id,d.title,d.active_version FROM run_sources s
  JOIN knowledge_documents d ON d.id=json_extract(s.reference,'$.document_id')
  WHERE d.deleted=0 AND json_extract(s.reference,'$.version') < d.active_version`
	args := []any{}
	if baseID != "" {
		query += " AND d.knowledge_id=?"
		args = append(args, baseID)
	}
	query += " ORDER BY s.rowid DESC LIMIT ?"
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []OutdatedSource{}
	for rows.Next() {
		var value OutdatedSource
		var raw string
		if err := rows.Scan(&value.RunID, &raw, &value.DocumentID, &value.Title, &value.ActiveVersion); err != nil {
			return nil, err
		}
		var hit Hit
		_ = json.Unmarshal([]byte(raw), &hit)
		value.SourceID = hit.SourceID
		value.CitedVersion = hit.Version
		values = append(values, value)
	}
	return values, rows.Err()
}

// LastEvalRegressed reports whether the newest retrieval evaluation regressed
// against the previous one.
func (c *Core) LastEvalRegressed(ctx context.Context, baseID string) (bool, error) {
	var raw string
	err := c.db.QueryRowContext(ctx, "SELECT metrics FROM knowledge_eval_runs WHERE knowledge_id=? AND kind='retrieval' ORDER BY created DESC,rowid DESC LIMIT 1", baseID).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var metrics EvalMetrics
	if err := json.Unmarshal([]byte(raw), &metrics); err != nil {
		return false, nil
	}
	return metrics.Regressed, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func truncateRunes(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
