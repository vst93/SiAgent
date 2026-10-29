package platform

import "database/sql"

// usageSchema records one token-usage row per run. Rows are created with zeros
// when a run is admitted, then updated from the provider-reported usage on the
// terminal event. Existing runs are backfilled with zeros so every historical
// conversation has an explicit record rather than a missing one.
const usageSchema = `
CREATE TABLE IF NOT EXISTS run_usage(
  run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  conversation_id TEXT NOT NULL,
  agent_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  cost REAL NOT NULL DEFAULT 0,
  reported INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS run_usage_created ON run_usage(created);
CREATE INDEX IF NOT EXISTS run_usage_conversation ON run_usage(conversation_id,created);
CREATE INDEX IF NOT EXISTS run_usage_model ON run_usage(model,created);
INSERT OR IGNORE INTO run_usage(run_id,conversation_id,agent_id,model,reported,created)
  SELECT id,conversation_id,'','',0,created FROM runs;
PRAGMA user_version=32;
`

func (s *Store) migrateUsage() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(usageSchema); err != nil {
		return err
	}
	return tx.Commit()
}

// recordRunUsage stores the provider-reported usage for one run. The row exists
// from admission, so an UPDATE preserves the run's original conversation,
// agent and creation time. A fallback model may replace the model name.
func recordRunUsage(tx *sql.Tx, runID string, usage *runUsagePayload) error {
	if usage == nil {
		return nil
	}
	total := usage.Total
	if total == 0 && (usage.Input > 0 || usage.Output > 0) {
		total = usage.Input + usage.Output
	}
	reported := 0
	if usage.Reported || total > 0 || usage.Input > 0 || usage.Output > 0 {
		reported = 1
	}
	result, err := tx.Exec(`UPDATE run_usage SET
		input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=?,
		total_tokens=?,cost=?,reported=?,
		model=CASE WHEN ?<>'' THEN ? ELSE model END
		WHERE run_id=?`,
		usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite,
		total, usage.Cost, reported, usage.Model, usage.Model, runID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count > 0 {
		return nil
	}
	// Older tests and imported rows may not have a usage row. Create one from
	// the run without inventing an agent or model.
	_, err = tx.Exec(`INSERT INTO run_usage(run_id,conversation_id,agent_id,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,total_tokens,cost,reported,created)
		SELECT ?,conversation_id,'',?,?,?,?,?,?,?,?,created FROM runs WHERE id=?`,
		runID, usage.Model, usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, total, usage.Cost, reported, runID)
	return err
}
