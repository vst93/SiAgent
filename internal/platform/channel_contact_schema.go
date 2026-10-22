package platform

// Version 29 adds administrator-set display names for channel contacts. The
// WeCom long-connection protocol only returns opaque member and chat IDs, so a
// readable group or member name has to be labelled by a human. The label is
// keyed by channel and vendor ID, so it survives new conversations with the same
// person or group.
const channelContactSchema = `
 CREATE TABLE IF NOT EXISTS channel_contacts(
   channel_id TEXT NOT NULL,
   kind TEXT NOT NULL,
   target TEXT NOT NULL,
   name TEXT NOT NULL,
   updated INTEGER NOT NULL,
   PRIMARY KEY(channel_id, kind, target)
 );
 PRAGMA user_version=29;
`

// ChannelContact labels one group or member of a channel.
type ChannelContact struct {
	ChannelID string `json:"channel_id"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Name      string `json:"name"`
	Updated   int64  `json:"updated"`
}

func (s *Store) migrateChannelContacts() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(channelContactSchema); err != nil {
		return err
	}
	return tx.Commit()
}
