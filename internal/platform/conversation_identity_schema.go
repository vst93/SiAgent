package platform

// Version 24 records the chat type and participants on a channel conversation,
// so history can name the sender instead of showing a generic channel user.
// Columns are added only when missing: fixtures that simulate an older schema
// keep the newer columns, and must not fail on a duplicate ALTER TABLE.
func (s *Store) migrateConversationIdentity() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, column := range []string{"channel_kind", "channel_sender", "channel_group"} {
		exists, err := hasColumn(tx, "conversations", column)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := tx.Exec("ALTER TABLE conversations ADD COLUMN " + column + " TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	// Backfill from the first inbox message per conversation, then from the
	// session key for conversations whose inbox rows were already cleaned.
	identities := map[string]participants{}
	rows, err := tx.Query("SELECT conversation_id,member FROM channel_inbox WHERE conversation_id<>'' ORDER BY created,rowid")
	if err != nil {
		return err
	}
	for rows.Next() {
		var conversation, member string
		if err := rows.Scan(&conversation, &member); err != nil {
			rows.Close()
			return err
		}
		if _, ok := identities[conversation]; !ok {
			identities[conversation] = sessionParticipants(member)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query("SELECT conversation_id,member FROM channel_sessions")
	if err != nil {
		return err
	}
	for rows.Next() {
		var conversation, member string
		if err := rows.Scan(&conversation, &member); err != nil {
			rows.Close()
			return err
		}
		if _, ok := identities[conversation]; !ok {
			identities[conversation] = sessionParticipants(member)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for conversation, who := range identities {
		if _, err := tx.Exec("UPDATE conversations SET channel_kind=?,channel_sender=?,channel_group=? WHERE id=? AND channel_id<>''", who.kind, who.sender, who.group, conversation); err != nil {
			return err
		}
	}
	// Answer feedback was removed from the product: keep no unused rating table.
	if _, err := tx.Exec("DROP TABLE IF EXISTS answer_feedback; PRAGMA user_version=24;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateKnowledgeSettings()
}
