package platform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"siagent/internal/channels"
	"siagent/internal/wecom"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const wecomSchema = `
 ALTER TABLE conversations ADD COLUMN channel_id TEXT NOT NULL DEFAULT '';
 CREATE TABLE wecom_channels(id TEXT PRIMARY KEY,corp_id TEXT NOT NULL,app_id INTEGER NOT NULL,agent_id TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 0,archived INTEGER NOT NULL DEFAULT 0,secrets TEXT NOT NULL,version INTEGER NOT NULL,verified INTEGER NOT NULL DEFAULT 0,UNIQUE(corp_id,app_id));
 CREATE TABLE wecom_sessions(channel_id TEXT NOT NULL REFERENCES wecom_channels(id),member TEXT NOT NULL,agent_id TEXT NOT NULL,conversation_id TEXT NOT NULL REFERENCES conversations(id),PRIMARY KEY(channel_id,member,agent_id));
 CREATE TABLE wecom_inbox(id TEXT PRIMARY KEY,channel_id TEXT NOT NULL REFERENCES wecom_channels(id),msg_id TEXT NOT NULL,member TEXT NOT NULL,agent_id TEXT NOT NULL,content TEXT NOT NULL,fingerprint TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',error TEXT NOT NULL DEFAULT '',run_id TEXT NOT NULL DEFAULT '',conversation_id TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,finalized INTEGER NOT NULL DEFAULT 0,hinted INTEGER NOT NULL DEFAULT 0,UNIQUE(channel_id,msg_id));
 CREATE TABLE wecom_outbox(id TEXT PRIMARY KEY,inbox_id TEXT NOT NULL REFERENCES wecom_inbox(id),channel_id TEXT NOT NULL REFERENCES wecom_channels(id),member TEXT NOT NULL,kind TEXT NOT NULL,part INTEGER NOT NULL,total INTEGER NOT NULL,content TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,code INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,UNIQUE(inbox_id,kind,part));
 CREATE INDEX wecom_pending_inbox ON wecom_inbox(state,created);
 CREATE INDEX wecom_pending_outbox ON wecom_outbox(state,next_at,created);`

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

func (s *Server) channel(id string) (wecomChannel, wecom.Credentials, error) {
	return s.channelContext(context.Background(), id)
}
func (s *Server) channelContext(ctx context.Context, id string) (wecomChannel, wecom.Credentials, error) {
	c, secrets, err := s.loadChannel(ctx, id)
	if err != nil {
		return wecomChannel{}, wecom.Credentials{}, err
	}
	if c.Provider != "wecom-app" {
		return wecomChannel{}, wecom.Credentials{}, sql.ErrNoRows
	}
	app, _ := strconv.ParseInt(c.Config["app_id"], 10, 64)
	return wecomChannel{ID: c.ID, CorpID: c.Config["corp_id"], AppID: app, AgentID: c.AgentID, Enabled: c.Enabled, Archived: c.Archived, Version: c.Version, Verified: c.Verified, HasSecrets: c.HasSecrets, CallbackPath: c.CallbackPath}, wecom.Credentials{Secret: secrets["secret"], Token: secrets["token"], AESKey: secrets["aes_key"]}, nil
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
	c, secret, err := s.channel(r.PathValue("channel"))
	if err != nil || c.Archived {
		fail(w, 404, "渠道不存在或已归档。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	result := s.wecomClient.Test(ctx, wecom.Application{ID: c.ID, CorpID: c.CorpID, AppID: c.AppID, Secret: secret.Secret})
	if result.State != "sent" {
		fail(w, 502, result.Message)
		return
	}
	respond(w, 200, map[string]string{"message": result.Message})
}

// CallbackHandler is the sole public channel endpoint; it uses signature/AES authentication, not administrator cookies.
func (s *Server) CallbackHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callbacks/wecom/{channel}", s.wecomCallback)
	mux.HandleFunc("POST /callbacks/wecom/{channel}", s.wecomCallback)
	return mux
}
func (s *Server) wecomCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	c, secret, err := s.channelContext(ctx, r.PathValue("channel"))
	if err != nil || c.Archived {
		http.Error(w, "not found", 404)
		return
	}
	q := r.URL.Query()
	timestamp, nonce, signature := q.Get("timestamp"), q.Get("nonce"), q.Get("msg_signature")
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || ts < time.Now().Add(-5*time.Minute).Unix() || ts > time.Now().Add(5*time.Minute).Unix() || len(nonce) < 1 || len(nonce) > 128 {
		http.Error(w, "invalid callback", 403)
		return
	}
	cryptor, err := wecom.NewCryptor(secret.Token, secret.AESKey, c.CorpID)
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	encrypted := q.Get("echostr")
	if r.Method == "POST" {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
		var envelope wecom.Envelope
		if err != nil || wecom.XML(data, &envelope) != nil {
			http.Error(w, "invalid callback", 400)
			return
		}
		encrypted = envelope.Encrypted
	}
	plain, err := cryptor.Decrypt(signature, timestamp, nonce, encrypted)
	if err != nil {
		http.Error(w, "invalid callback", 403)
		return
	}
	if r.Method == "GET" {
		result, err := s.store.db.ExecContext(ctx, "UPDATE channels SET verified=1 WHERE id=? AND version=? AND archived=0", c.ID, c.Version)
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		if count, _ := result.RowsAffected(); count != 1 {
			http.Error(w, "configuration changed", 503)
			return
		}
		w.Write(plain)
		return
	}
	var message wecom.Message
	if wecom.XML(plain, &message) != nil || message.CorpID != c.CorpID || message.AppID != c.AppID {
		http.Error(w, "invalid callback", 403)
		return
	}
	if message.Type == "event" || message.ChatID != "" {
		io.WriteString(w, "success")
		return
	}
	if !wecom.ValidMember(message.Member) || len(message.ID) < 1 || len(message.ID) > 128 || strings.Trim(message.ID, "0123456789") != "" || !utf8.ValidString(message.Content) || len(message.Content) > 100000 {
		http.Error(w, "invalid callback", 400)
		return
	}
	original, _ := json.Marshal(message)
	digest := sha256.Sum256(original)
	err = s.receiveChannel(ctx, c.ID, c.Version, channels.Incoming{ID: message.ID, Session: message.Member, Kind: message.Type, Text: message.Content, Fingerprint: hex.EncodeToString(digest[:])})
	if err != nil {
		if err.Error() == "conflicting channel message" {
			http.Error(w, "conflicting callback", 403)
		} else {
			http.Error(w, "channel unavailable", 503)
		}
		return
	}
	io.WriteString(w, "success")
}
