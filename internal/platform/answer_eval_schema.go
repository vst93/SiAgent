package platform

import "siagent/internal/knowledge"

// answerEvalSchema marks evaluation runs so retrieval and answer-quality runs
// share one history without changing existing rows.
const answerEvalSchema = knowledge.QualitySchema21 + `
 PRAGMA user_version=21;
`

func (s *Store) migrateAnswerEval() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(answerEvalSchema); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateAPIKeySecret()
}
