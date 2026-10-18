package platform

import "siagent/internal/knowledge"

// qualitySchema is the additive migration for the second knowledge-quality
// pass: per-base retrieval config, conflicts, vocabulary, evidence maps and
// plugin configuration/health columns.
const qualitySchema = knowledge.QualitySchema20 + `
 ALTER TABLE plugins ADD COLUMN config TEXT NOT NULL DEFAULT '{}';
 ALTER TABLE plugins ADD COLUMN secrets TEXT NOT NULL DEFAULT '';
 ALTER TABLE plugins ADD COLUMN health TEXT NOT NULL DEFAULT '';
 ALTER TABLE plugins ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
 PRAGMA user_version=20;
`

func (s *Store) migrateQuality() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(qualitySchema); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateAnswerEval()
}
