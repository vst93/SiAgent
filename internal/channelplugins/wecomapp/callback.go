package wecomapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	wecom "siagent/internal/channelplugins/wecomapp/protocol"
	"siagent/internal/channels"
)

func (p *Provider) ServeCallback(w http.ResponseWriter, r *http.Request, c channels.Config, receive channels.Receive, verified func(context.Context) error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	ctx := r.Context()
	q := r.URL.Query()
	timestamp, nonce, signature := q.Get("timestamp"), q.Get("nonce"), q.Get("msg_signature")
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || ts < time.Now().Add(-5*time.Minute).Unix() || ts > time.Now().Add(5*time.Minute).Unix() || len(nonce) < 1 || len(nonce) > 128 {
		http.Error(w, "invalid callback", 403)
		return
	}
	cryptor, err := wecom.NewCryptor(c.Secrets["token"], c.Secrets["aes_key"], c.Values["corp_id"])
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
		if err := verified(ctx); err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Write(plain)
		return
	}
	var message wecom.Message
	if wecom.XML(plain, &message) != nil || message.CorpID != c.Values["corp_id"] || strconv.FormatInt(message.AppID, 10) != c.Values["app_id"] {
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
	err = receive(ctx, channels.Incoming{ID: message.ID, Session: message.Member, Kind: message.Type, Text: message.Content, Fingerprint: hex.EncodeToString(digest[:])})
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
