package knowledge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// BaseConfig controls per-knowledge-base retrieval behaviour. Defaults keep the
// previous behaviour: rule-based expansion on, model expansion off.
type BaseConfig struct {
	Expand    bool `json:"expand"`
	LLMExpand bool `json:"llm_expand"`
	// Gate blocks document replacement and reindex while the latest retrieval
	// evaluation regressed.
	Gate bool `json:"gate"`
}

func DefaultBaseConfig() BaseConfig { return BaseConfig{Expand: true} }

// VocabEntry is one customer synonym: when Term appears in a query, Expansion
// is also searched.
type VocabEntry struct {
	Term      string `json:"term"`
	Expansion string `json:"expansion"`
}

func (c *Core) BaseConfig(ctx context.Context, baseID string) (BaseConfig, error) {
	var raw string
	err := c.db.QueryRowContext(ctx, "SELECT config FROM knowledge_bases WHERE id=? AND deleted=0", baseID).Scan(&raw)
	if err == sql.ErrNoRows {
		return BaseConfig{}, missing()
	}
	if err != nil {
		return BaseConfig{}, err
	}
	cfg := DefaultBaseConfig()
	if strings.TrimSpace(raw) != "" && raw != "{}" {
		decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return DefaultBaseConfig(), nil
		}
	}
	return cfg, nil
}

func (c *Core) SaveBaseConfig(ctx context.Context, baseID string, cfg BaseConfig) (BaseConfig, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	result, err := c.db.ExecContext(ctx, "UPDATE knowledge_bases SET config=? WHERE id=? AND deleted=0", string(raw), baseID)
	if err != nil {
		return cfg, err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return cfg, missing()
	}
	return cfg, nil
}

func (c *Core) Vocabulary(ctx context.Context, baseID string) ([]VocabEntry, error) {
	rows, err := c.db.QueryContext(ctx, "SELECT term,expansion FROM knowledge_vocabulary WHERE knowledge_id=? ORDER BY term", baseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []VocabEntry{}
	for rows.Next() {
		var value VocabEntry
		if err := rows.Scan(&value.Term, &value.Expansion); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (c *Core) SaveVocabulary(ctx context.Context, baseID, term, expansion string) error {
	term = strings.TrimSpace(term)
	expansion = strings.TrimSpace(expansion)
	if term == "" || expansion == "" || len(term) > 100 || len(expansion) > 200 {
		return invalid("请填写词条与扩展词，各不超过 100/200 字节。")
	}
	if strings.EqualFold(term, expansion) {
		return invalid("词条与扩展词不能相同。")
	}
	var exists int
	if err := c.db.QueryRowContext(ctx, "SELECT count(*) FROM knowledge_bases WHERE id=? AND deleted=0", baseID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return missing()
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO knowledge_vocabulary(knowledge_id,term,expansion,created) VALUES(?,?,?,strftime('%s','now')*1000)
  ON CONFLICT(knowledge_id,term) DO UPDATE SET expansion=excluded.expansion`, baseID, term, expansion)
	return err
}

func (c *Core) DeleteVocabulary(ctx context.Context, baseID, term string) error {
	result, err := c.db.ExecContext(ctx, "DELETE FROM knowledge_vocabulary WHERE knowledge_id=? AND term=?", baseID, term)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return missing()
	}
	return nil
}
