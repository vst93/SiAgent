package knowledge

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"
)

// GapSchema stores repeated unanswered questions so content owners can see what
// the knowledge base is missing. Only the question text is kept, never answers,
// sources or personal identifiers.
const GapSchema = `
 CREATE TABLE knowledge_gaps(id INTEGER PRIMARY KEY AUTOINCREMENT,knowledge_id TEXT NOT NULL DEFAULT '',query TEXT NOT NULL,count INTEGER NOT NULL DEFAULT 1,status TEXT NOT NULL DEFAULT 'open',created INTEGER NOT NULL,last_seen INTEGER NOT NULL);
 CREATE UNIQUE INDEX knowledge_gaps_query ON knowledge_gaps(knowledge_id,query);
`

// ContentGap is one aggregated unanswered question.
type ContentGap struct {
	ID          int64  `json:"id"`
	KnowledgeID string `json:"knowledge_id"`
	Query       string `json:"query"`
	Count       int    `json:"count"`
	Status      string `json:"status"`
	Created     int64  `json:"created"`
	LastSeen    int64  `json:"last_seen"`
}

const maxGapQueryRunes = 300

// RecordGap increments the counter for a normalized question. Empty or
// whitespace-only queries are ignored so diagnostics do not pollute the list.
func (c *Core) RecordGap(ctx context.Context, knowledgeID, query string) {
	query = normalizeGapQuery(query)
	if query == "" {
		return
	}
	now := time.Now().UnixMilli()
	_, _ = c.db.ExecContext(ctx, `INSERT INTO knowledge_gaps(knowledge_id,query,count,status,created,last_seen) VALUES(?,?,1,'open',?,?)
  ON CONFLICT(knowledge_id,query) DO UPDATE SET count=knowledge_gaps.count+1,last_seen=excluded.last_seen`, knowledgeID, query, now, now)
}

func normalizeGapQuery(query string) string {
	query = strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
	if utf8.RuneCountInString(query) > maxGapQueryRunes {
		query = string([]rune(query)[:maxGapQueryRunes])
	}
	return query
}

// Gaps lists content gaps, newest activity first. An empty status lists all.
func (c *Core) Gaps(ctx context.Context, knowledgeID, status string, limit int) ([]ContentGap, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	query := "SELECT id,knowledge_id,query,count,status,created,last_seen FROM knowledge_gaps WHERE 1=1"
	args := []any{}
	if knowledgeID != "" {
		query += " AND knowledge_id=?"
		args = append(args, knowledgeID)
	}
	if status == "open" || status == "resolved" || status == "ignored" {
		query += " AND status=?"
		args = append(args, status)
	}
	query += " ORDER BY last_seen DESC,id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ContentGap{}
	for rows.Next() {
		var gap ContentGap
		if err := rows.Scan(&gap.ID, &gap.KnowledgeID, &gap.Query, &gap.Count, &gap.Status, &gap.Created, &gap.LastSeen); err != nil {
			return nil, err
		}
		values = append(values, gap)
	}
	return values, rows.Err()
}

// SetGapStatus marks a gap resolved or ignored. Reopening is allowed.
func (c *Core) SetGapStatus(ctx context.Context, id int64, status string) (ContentGap, error) {
	if status != "open" && status != "resolved" && status != "ignored" {
		return ContentGap{}, invalid("缺口状态无效。")
	}
	result, err := c.db.ExecContext(ctx, "UPDATE knowledge_gaps SET status=? WHERE id=?", status, id)
	if err != nil {
		return ContentGap{}, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ContentGap{}, missing()
	}
	var gap ContentGap
	err = c.db.QueryRowContext(ctx, "SELECT id,knowledge_id,query,count,status,created,last_seen FROM knowledge_gaps WHERE id=?", id).Scan(&gap.ID, &gap.KnowledgeID, &gap.Query, &gap.Count, &gap.Status, &gap.Created, &gap.LastSeen)
	if err == sql.ErrNoRows {
		return gap, missing()
	}
	return gap, err
}

func (c *Core) DeleteGap(ctx context.Context, id int64) error {
	result, err := c.db.ExecContext(ctx, "DELETE FROM knowledge_gaps WHERE id=?", id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return missing()
	}
	return nil
}
