package platform

// m5Schema adds webhook subscriptions and their delivery ledger.
const m5Schema = `
 CREATE TABLE webhook_subscriptions(id TEXT PRIMARY KEY,url TEXT NOT NULL,secret TEXT NOT NULL,events TEXT NOT NULL,agent_id TEXT NOT NULL DEFAULT '',enabled INTEGER NOT NULL DEFAULT 1,created INTEGER NOT NULL,updated INTEGER NOT NULL);
 CREATE TABLE webhook_deliveries(id TEXT PRIMARY KEY,subscription_id TEXT NOT NULL,event TEXT NOT NULL,event_id TEXT NOT NULL,run_id TEXT NOT NULL,payload TEXT NOT NULL,status TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL);
 CREATE UNIQUE INDEX webhook_delivery_event ON webhook_deliveries(subscription_id,event_id);
 PRAGMA user_version=19;
`

func (s *Store) migrateWebhooks() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(m5Schema); err != nil {
		return err
	}
	return tx.Commit()
}
