package platform

// Version 31 adds the indexes the conversation list needs. That query derives
// three values per conversation — newest message time, question count and the
// last channel member — and without these indexes each one scanned the whole
// conversation, so listing 2000 conversations over 400k messages took ~220ms on
// every sidebar poll. With the indexes it is a handful of index seeks.
//
// messages(conversation_id, created) serves max(created).
// messages(conversation_id, role, id) serves the role-filtered count.
// channel_inbox(conversation_id, created) serves the last-member lookup: the most
// recent inbound row of a conversation is a single index seek, where joining the
// inbox to messages for every conversation cost ~0.9ms each.
const conversationIndexSchema = `
 CREATE INDEX IF NOT EXISTS messages_conversation_created ON messages(conversation_id, created);
 CREATE INDEX IF NOT EXISTS messages_conversation_role ON messages(conversation_id, role, id);
 CREATE INDEX IF NOT EXISTS messages_run ON messages(run_id);
 CREATE INDEX IF NOT EXISTS channel_inbox_conversation_created ON channel_inbox(conversation_id, created);
 PRAGMA user_version=31;
`

func (s *Store) migrateConversationIndexes() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(conversationIndexSchema); err != nil {
		return err
	}
	return tx.Commit()
}
