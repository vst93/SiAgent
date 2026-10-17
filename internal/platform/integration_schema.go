package platform

// integrationSchema adds API keys, idempotency, external conversation mapping
// and external document mapping. All tables are additive.
const integrationSchema = `
 CREATE TABLE api_keys(id TEXT PRIMARY KEY,prefix TEXT NOT NULL,hash TEXT NOT NULL UNIQUE,name TEXT NOT NULL,scopes TEXT NOT NULL,agent_id TEXT NOT NULL DEFAULT '',knowledge_id TEXT NOT NULL DEFAULT '',rate_limit INTEGER NOT NULL DEFAULT 60,expires INTEGER NOT NULL DEFAULT 0,revoked INTEGER NOT NULL DEFAULT 0,created INTEGER NOT NULL,last_used INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE api_idempotency(api_key_id TEXT NOT NULL,key TEXT NOT NULL,run_id TEXT NOT NULL,request_hash TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(api_key_id,key));
 CREATE TABLE api_conversations(api_key_id TEXT NOT NULL,external_id TEXT NOT NULL,conversation_id TEXT NOT NULL,agent_id TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(api_key_id,external_id));
 CREATE TABLE knowledge_external(knowledge_id TEXT NOT NULL,external_id TEXT NOT NULL,document_id TEXT NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(knowledge_id,external_id));
 ALTER TABLE agents ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
 ALTER TABLE runs ADD COLUMN agent_revision INTEGER NOT NULL DEFAULT 0;
 ALTER TABLE runs ADD COLUMN source TEXT NOT NULL DEFAULT '';
 PRAGMA user_version=18;
`

// migrateIntegration is the schema step for the /v1 integration API.
func (s *Store) migrateIntegration() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(integrationSchema); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateWebhooks()
}
