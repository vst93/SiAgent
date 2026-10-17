package platform

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
)

const wecomSchema = `
 ALTER TABLE conversations ADD COLUMN channel_id TEXT NOT NULL DEFAULT '';
 CREATE TABLE wecom_channels(id TEXT PRIMARY KEY,corp_id TEXT NOT NULL,app_id INTEGER NOT NULL,agent_id TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 0,archived INTEGER NOT NULL DEFAULT 0,secrets TEXT NOT NULL,version INTEGER NOT NULL,verified INTEGER NOT NULL DEFAULT 0,UNIQUE(corp_id,app_id));
 CREATE TABLE wecom_sessions(channel_id TEXT NOT NULL REFERENCES wecom_channels(id),member TEXT NOT NULL,agent_id TEXT NOT NULL,conversation_id TEXT NOT NULL REFERENCES conversations(id),PRIMARY KEY(channel_id,member,agent_id));
 CREATE TABLE wecom_inbox(id TEXT PRIMARY KEY,channel_id TEXT NOT NULL REFERENCES wecom_channels(id),msg_id TEXT NOT NULL,member TEXT NOT NULL,agent_id TEXT NOT NULL,content TEXT NOT NULL,fingerprint TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',error TEXT NOT NULL DEFAULT '',run_id TEXT NOT NULL DEFAULT '',conversation_id TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,finalized INTEGER NOT NULL DEFAULT 0,hinted INTEGER NOT NULL DEFAULT 0,UNIQUE(channel_id,msg_id));
 CREATE TABLE wecom_outbox(id TEXT PRIMARY KEY,inbox_id TEXT NOT NULL REFERENCES wecom_inbox(id),channel_id TEXT NOT NULL REFERENCES wecom_channels(id),member TEXT NOT NULL,kind TEXT NOT NULL,part INTEGER NOT NULL,total INTEGER NOT NULL,content TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,code INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,UNIQUE(inbox_id,kind,part));
 CREATE INDEX wecom_pending_inbox ON wecom_inbox(state,created);
 CREATE INDEX wecom_pending_outbox ON wecom_outbox(state,next_at,created);`

// Legacy API DTOs translate old requests to the generic channel API. Transport
// and callback behavior belong to the channel plugin.
type legacyChannelCredentials struct {
	Secret string
	Token  string
	AESKey string
}

type wecomChannel struct {
	ID           string `json:"id"`
	CorpID       string `json:"corp_id"`
	AppID        int64  `json:"app_id"`
	AgentID      string `json:"agent_id"`
	Enabled      bool   `json:"enabled"`
	Archived     bool   `json:"archived"`
	Version      int    `json:"version"`
	Verified     bool   `json:"verified"`
	HasSecrets   bool   `json:"has_secrets"`
	CallbackPath string `json:"callback_path"`
}

func (s *Server) channel(id string) (wecomChannel, legacyChannelCredentials, error) {
	return s.channelContext(context.Background(), id)
}
func (s *Server) channelContext(ctx context.Context, id string) (wecomChannel, legacyChannelCredentials, error) {
	c, secrets, err := s.loadChannel(ctx, id)
	if err != nil {
		return wecomChannel{}, legacyChannelCredentials{}, err
	}
	if c.Provider != "wecom-app" {
		return wecomChannel{}, legacyChannelCredentials{}, sql.ErrNoRows
	}
	app, _ := strconv.ParseInt(c.Config["app_id"], 10, 64)
	return wecomChannel{ID: c.ID, CorpID: c.Config["corp_id"], AppID: app, AgentID: c.AgentID, Enabled: c.Enabled, Archived: c.Archived, Version: c.Version, Verified: c.Verified, HasSecrets: c.HasSecrets, CallbackPath: c.CallbackPath}, legacyChannelCredentials{Secret: secrets["secret"], Token: secrets["token"], AESKey: secrets["aes_key"]}, nil
}
func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id FROM channels WHERE provider='wecom-app' ORDER BY rowid")
	if err != nil {
		internal(w)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			internal(w)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w)
		return
	}
	out := []wecomChannel{}
	for _, id := range ids {
		c, _, err := s.channel(id)
		if err != nil {
			internal(w)
			return
		}
		out = append(out, c)
	}
	respond(w, 200, out)
}
func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CorpID  string `json:"corp_id"`
		AppID   int64  `json:"app_id"`
		AgentID string `json:"agent_id"`
		Enabled bool   `json:"enabled"`
		Secret  string `json:"secret"`
		Token   string `json:"token"`
		AESKey  string `json:"aes_key"`
		Confirm bool   `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	c, err := s.persistChannel(r.Context(), r.PathValue("channel"), channelInput{Name: "企微自建应用 " + input.CorpID + "/" + strconv.FormatInt(input.AppID, 10), Provider: "wecom-app", Config: map[string]string{"corp_id": input.CorpID, "app_id": strconv.FormatInt(input.AppID, 10)}, Secrets: map[string]string{"secret": input.Secret, "token": input.Token, "aes_key": input.AESKey}, AgentID: input.AgentID, Enabled: input.Enabled, Confirm: input.Confirm})
	if err != nil {
		writeAdmissionError(w, err)
		return
	}
	legacy, _, err := s.channel(c.ID)
	if err != nil {
		internal(w)
		return
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	respond(w, status, legacy)
}
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	c, _, err := s.channel(r.PathValue("channel"))
	if err != nil || c.Archived {
		fail(w, 404, "渠道不存在或已归档。")
		return
	}
	s.testGenericChannel(w, r)
}
