package platform

// Version 9 moves transport-neutral state into channel_* tables. IDs and all
// references stay unchanged; legacy configuration becomes an adapter payload.
func (s *Store) migrateChannelProviders() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
 CREATE TABLE channels(id TEXT PRIMARY KEY,name TEXT NOT NULL,provider TEXT NOT NULL,identity TEXT NOT NULL,config TEXT NOT NULL,agent_id TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 0,archived INTEGER NOT NULL DEFAULT 0,secrets TEXT NOT NULL,version INTEGER NOT NULL,verified INTEGER NOT NULL DEFAULT 0);
 CREATE UNIQUE INDEX active_channel_identity ON channels(provider,identity) WHERE archived=0;
 CREATE TABLE channel_sessions(channel_id TEXT NOT NULL REFERENCES channels(id),member TEXT NOT NULL,agent_id TEXT NOT NULL,conversation_id TEXT NOT NULL REFERENCES conversations(id),PRIMARY KEY(channel_id,member,agent_id));
 CREATE TABLE channel_inbox(id TEXT PRIMARY KEY,channel_id TEXT NOT NULL REFERENCES channels(id),msg_id TEXT NOT NULL,member TEXT NOT NULL,agent_id TEXT NOT NULL,content TEXT NOT NULL,fingerprint TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',error TEXT NOT NULL DEFAULT '',run_id TEXT NOT NULL DEFAULT '',conversation_id TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,finalized INTEGER NOT NULL DEFAULT 0,hinted INTEGER NOT NULL DEFAULT 0,reply_context TEXT NOT NULL DEFAULT '{}',UNIQUE(channel_id,msg_id));
 CREATE TABLE channel_outbox(id TEXT PRIMARY KEY,inbox_id TEXT NOT NULL REFERENCES channel_inbox(id),channel_id TEXT NOT NULL REFERENCES channels(id),member TEXT NOT NULL,kind TEXT NOT NULL,part INTEGER NOT NULL,total INTEGER NOT NULL,content TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,code INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,UNIQUE(inbox_id,kind,part));
 INSERT INTO channels SELECT id,'企微自建应用 '||corp_id||'/'||app_id,'wecom-app',corp_id||char(31)||app_id,json_object('corp_id',corp_id,'app_id',CAST(app_id AS TEXT)),agent_id,enabled,archived,secrets,version,verified FROM wecom_channels;
 INSERT INTO channel_sessions SELECT * FROM wecom_sessions;
 INSERT INTO channel_inbox(id,channel_id,msg_id,member,agent_id,content,fingerprint,state,error,run_id,conversation_id,created,finalized,hinted) SELECT id,channel_id,msg_id,member,agent_id,content,fingerprint,state,error,run_id,conversation_id,created,finalized,hinted FROM wecom_inbox;
 INSERT INTO channel_outbox SELECT * FROM wecom_outbox;
 DROP TABLE wecom_outbox; DROP TABLE wecom_inbox; DROP TABLE wecom_sessions; DROP TABLE wecom_channels;
 CREATE INDEX channel_pending_inbox ON channel_inbox(state,created);
 CREATE INDEX channel_pending_outbox ON channel_outbox(state,next_at,created);
 PRAGMA user_version=9;
 `)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateAgentPolicies()
}
