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

// Version 30 records who supplied a label. Members may name themselves and their
// group with /user and /group; the administrator can always override, so the
// console shows which labels came from members.
const channelContactSourceSchema = `
 ALTER TABLE channel_contacts ADD COLUMN source TEXT NOT NULL DEFAULT 'admin';
 ALTER TABLE channel_contacts ADD COLUMN set_by TEXT NOT NULL DEFAULT '';
 PRAGMA user_version=30;
`

// ChannelContact labels one group or member of a channel.
type ChannelContact struct {
	ChannelID string `json:"channel_id"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Name      string `json:"name"`
	Updated   int64  `json:"updated"`
	// Source is "admin" or "member"; SetBy is the member ID for member labels.
	Source string `json:"source,omitempty"`
	SetBy  string `json:"set_by,omitempty"`
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
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateChannelContactSource()
}

func (s *Store) migrateChannelContactSource() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Idempotent for fixtures that downgrade user_version without dropping columns.
	exists, err := hasColumn(tx, "channel_contacts", "source")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec("ALTER TABLE channel_contacts ADD COLUMN source TEXT NOT NULL DEFAULT 'admin'"); err != nil {
			return err
		}
	}
	exists, err = hasColumn(tx, "channel_contacts", "set_by")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec("ALTER TABLE channel_contacts ADD COLUMN set_by TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("PRAGMA user_version=30;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Each step chains to the next one, so an older database still reaches the
	// newest schema in a single Open.
	return s.migrateConversationIndexes()
}
