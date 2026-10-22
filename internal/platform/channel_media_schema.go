package platform

// Version 28 records inbound channel media. Adapters download and decrypt
// pictures themselves, because only they hold the vendor key, and stage the
// bytes in a per-channel directory: the process protocol caps one frame at
// 256 KiB, while one picture may be several MiB. The inbox row stores the staged
// file names; admission promotes them to ordinary chat attachments.
const channelMediaSchema = `
 ALTER TABLE channel_inbox ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]';
 PRAGMA user_version=28;
`

func (s *Store) migrateChannelMedia() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exists, err := hasColumn(tx, "channel_inbox", "attachments")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec("ALTER TABLE channel_inbox ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]'"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("PRAGMA user_version=28;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateChannelContacts()
}
