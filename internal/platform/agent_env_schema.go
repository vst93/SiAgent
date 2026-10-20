package platform

// Version 26 adds per-Agent environment variables. Skills sometimes expect a
// system constant or secret (for example an API token) that the Agent's shell
// tools read at run time; until now there was no place to configure it. Values
// are encrypted with the platform master key, named only in the API and audit
// output, and never written to run configuration or logs.
func (s *Store) migrateAgentEnvironment() error {
	// Version 26 adds agents.environment; the knowledge-settings migration calls
	// this too so a fresh install and an upgrade agree on the column set.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exists, err := hasColumn(tx, "agents", "environment")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec("ALTER TABLE agents ADD COLUMN environment TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("PRAGMA user_version=26;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateSensitiveReveal()
}
