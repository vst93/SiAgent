package platform

import "database/sql"

const chatAssetsSchema = `
 CREATE TABLE IF NOT EXISTS attachments(
   id TEXT PRIMARY KEY,
   conversation_id TEXT NOT NULL,
   run_id TEXT NOT NULL DEFAULT '',
   name TEXT NOT NULL,
   mime TEXT NOT NULL,
   kind TEXT NOT NULL,
   size INTEGER NOT NULL,
   path TEXT NOT NULL,
   extracted TEXT NOT NULL DEFAULT '',
   linked INTEGER NOT NULL DEFAULT 0,
   created INTEGER NOT NULL
 );
 CREATE INDEX IF NOT EXISTS attachments_conversation ON attachments(conversation_id, created);
 CREATE INDEX IF NOT EXISTS attachments_cleanup ON attachments(linked, created);
 PRAGMA user_version=23;
`

func hasColumn(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// migrateChatAssets is intentionally idempotent for old migration test fixtures
// that downgrade user_version without removing newer columns.
func (s *Store) migrateChatAssets() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if ok, err := hasColumn(tx, "agents", "fallback_model_ids"); err != nil {
		return err
	} else if !ok {
		if _, err := tx.Exec("ALTER TABLE agents ADD COLUMN fallback_model_ids TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return err
		}
	}
	if ok, err := hasColumn(tx, "messages", "attachments"); err != nil {
		return err
	} else if !ok {
		if _, err := tx.Exec("ALTER TABLE messages ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(chatAssetsSchema); err != nil {
		// The additive column statements above are already handled. Run only
		// the table/index/version portion here; this also supports retrying a
		// partially applied migration.
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateConversationIdentity()
}
