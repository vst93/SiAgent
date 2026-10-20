package platform

import "time"

// Version 27 adds a short reveal window to administrator sessions. Sensitive
// values such as Agent environment variables stay encrypted and hidden by
// default; re-entering the password opens a bounded window in which the owner
// can read them. Every read is audited, and the window closes on its own even
// while the page stays open.
const sensitiveRevealSchema = `
 ALTER TABLE sessions ADD COLUMN reveal_until INTEGER NOT NULL DEFAULT 0;
 PRAGMA user_version=27;
`

// revealWindow bounds how long one password confirmation allows reading
// sensitive values. Short by design: the administrator re-confirms for the next
// look instead of leaving a permanently readable value on screen.
const revealWindow = 5 * time.Minute

func (s *Store) migrateSensitiveReveal() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exists, err := hasColumn(tx, "sessions", "reveal_until")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec("ALTER TABLE sessions ADD COLUMN reveal_until INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("PRAGMA user_version=27;"); err != nil {
		return err
	}
	return tx.Commit()
}
