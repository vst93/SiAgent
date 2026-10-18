package platform

// apiKeySecretSchema adds an encrypted copy of the key for administrator
// reveal. Authentication still uses the SHA-256 hash; the encrypted copy is
// never returned by list endpoints and is only decrypted on explicit reveal.
const apiKeySecretSchema = `
 ALTER TABLE api_keys ADD COLUMN secret TEXT NOT NULL DEFAULT '';
 PRAGMA user_version=22;
`

func (s *Store) migrateAPIKeySecret() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(apiKeySecretSchema); err != nil {
		return err
	}
	return tx.Commit()
}
