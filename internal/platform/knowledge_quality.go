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
	return tx.Commit()
}
