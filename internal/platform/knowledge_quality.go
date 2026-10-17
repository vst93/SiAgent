package platform

import "siagent/internal/knowledge"

// migrateKnowledgeQuality adds evaluation and trace tables. It only adds
// tables, so existing documents, indexes and history stay untouched.
func (s *Store) migrateKnowledgeQuality() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(knowledge.EvalSchema + "\n PRAGMA user_version=11;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateKnowledgeStructure()
}

// migrateKnowledgeStructure adds structural chunk metadata used by
// structure-aware parsing. Existing chunks keep working with defaults.
func (s *Store) migrateKnowledgeStructure() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(knowledge.ChunkStructureSchema + "\n PRAGMA user_version=12;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateAnswerEvidence()
}

// migrateAnswerEvidence records whether an answer was backed by retrieved
// sources and whether its citation markers pointed at real sources.
func (s *Store) migrateAnswerEvidence() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("ALTER TABLE runs ADD COLUMN citation_valid INTEGER NOT NULL DEFAULT -1; ALTER TABLE runs ADD COLUMN evidence_state TEXT NOT NULL DEFAULT ''; PRAGMA user_version=13;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateFeedback()
}

// migrateFeedback adds answer ratings and content-gap tracking.
func (s *Store) migrateFeedback() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(FeedbackSchema + knowledge.GapSchema + "\n PRAGMA user_version=14;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateModelProviders()
}

// migrateModelProviders records a provider for every model connection and adds
// the optional provider-pack table. Existing connections map to the provider
// that matches their protocol.
func (s *Store) migrateModelProviders() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE models ADD COLUMN provider TEXT NOT NULL DEFAULT '';
 CREATE TABLE provider_packs(id TEXT PRIMARY KEY,installed_at INTEGER NOT NULL);
 UPDATE models SET provider=CASE WHEN json_extract(options,'$.api')='anthropic-messages' THEN 'anthropic' ELSE 'openai' END WHERE provider='';
 PRAGMA user_version=15;`); err != nil {
		return err
	}
	return tx.Commit()
}
