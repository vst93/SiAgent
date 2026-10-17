package platform

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type webhook struct {
	ID      string   `json:"id"`
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	AgentID string   `json:"agent_id"`
	Enabled bool     `json:"enabled"`
	Created int64    `json:"created"`
	Updated int64    `json:"updated"`
	HasKey  bool     `json:"has_secret"`
}

type webhookDelivery struct {
	ID             string `json:"id"`
	SubscriptionID string `json:"subscription_id"`
	Event          string `json:"event"`
	EventID        string `json:"event_id"`
	RunID          string `json:"run_id"`
	Status         string `json:"status"`
	Attempts       int    `json:"attempts"`
	LastError      string `json:"last_error"`
	Created        int64  `json:"created"`
}

var webhookEvents = map[string]bool{"run.completed": true, "run.failed": true, "run.cancelled": true, "run.interrupted": true}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id,url,events,agent_id,enabled,created,updated FROM webhook_subscriptions ORDER BY created DESC")
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	values := []webhook{}
	for rows.Next() {
		var value webhook
		var events string
		var enabled int
		if err := rows.Scan(&value.ID, &value.URL, &events, &value.AgentID, &enabled, &value.Created, &value.Updated); err != nil {
			internal(w)
			return
		}
		_ = json.Unmarshal([]byte(events), &value.Events)
		value.Enabled = enabled == 1
		value.HasKey = true
		values = append(values, value)
	}
	respond(w, 200, values)
}

func (s *Server) saveWebhook(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL     string   `json:"url"`
		Secret  string   `json:"secret"`
		Events  []string `json:"events"`
		AgentID string   `json:"agent_id"`
		Enabled bool     `json:"enabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.URL = strings.TrimSpace(input.URL)
	parsed, err := url.Parse(input.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
		fail(w, 400, "请填写有效的 http(s) Webhook 地址。")
		return
	}
	if len(input.URL) > 2048 {
		fail(w, 400, "Webhook 地址过长。")
		return
	}
	if len(input.Events) == 0 || len(input.Events) > 8 {
		fail(w, 400, "请选择 1–8 个事件。")
		return
	}
	for _, event := range input.Events {
		if !webhookEvents[event] {
			fail(w, 400, "事件类型无效。")
			return
		}
	}
	id := r.PathValue("id")
	secret := input.Secret
	if secret == "" && id != "" {
		_ = s.store.db.QueryRow("SELECT secret FROM webhook_subscriptions WHERE id=?", id).Scan(&secret)
		if secret != "" {
			decrypted, err := s.store.decrypt(secret)
			if err == nil {
				secret = decrypted
			}
		}
	}
	if len(secret) < 16 || len(secret) > 200 {
		fail(w, 400, "签名密钥须为 16–200 字节。")
		return
	}
	encoded, _ := json.Marshal(input.Events)
	now := time.Now().UnixMilli()
	if id == "" {
		id = id2()
		_, err = s.store.db.Exec("INSERT INTO webhook_subscriptions(id,url,secret,events,agent_id,enabled,created,updated) VALUES(?,?,?,?,?,?,?,?)", id, input.URL, s.store.encrypt(secret), string(encoded), input.AgentID, boolInt(input.Enabled), now, now)
	} else {
		_, err = s.store.db.Exec("UPDATE webhook_subscriptions SET url=?,secret=?,events=?,agent_id=?,enabled=?,updated=? WHERE id=?", input.URL, s.store.encrypt(secret), string(encoded), input.AgentID, boolInt(input.Enabled), now, id)
	}
	if err != nil {
		internal(w)
		return
	}
	s.audit("webhook.save", id, input.URL)
	value := webhook{ID: id, URL: input.URL, Events: input.Events, AgentID: input.AgentID, Enabled: input.Enabled, Created: now, Updated: now, HasKey: true}
	respond(w, 200, value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// id2 generates a random identifier without colliding with the platform helper.
func id2() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return "wh_" + base64.RawURLEncoding.EncodeToString(raw[:])
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.db.Exec("DELETE FROM webhook_subscriptions WHERE id=?", r.PathValue("id")); err != nil {
		internal(w)
		return
	}
	s.audit("webhook.delete", r.PathValue("id"), "")
	respond(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id,subscription_id,event,event_id,run_id,status,attempts,last_error,created FROM webhook_deliveries ORDER BY created DESC LIMIT 100")
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	values := []webhookDelivery{}
	for rows.Next() {
		var value webhookDelivery
		if err := rows.Scan(&value.ID, &value.SubscriptionID, &value.Event, &value.EventID, &value.RunID, &value.Status, &value.Attempts, &value.LastError, &value.Created); err != nil {
			internal(w)
			return
		}
		values = append(values, value)
	}
	respond(w, 200, values)
}

// enqueueWebhooks records one delivery per matching subscription and attempts
// it in the background. Duplicate terminal events are ignored by the unique
// (subscription, event_id) index.
func (s *Server) enqueueWebhooks(runID, status string) {
	event := "run." + map[string]string{"completed": "completed", "failed": "failed", "cancelled": "cancelled", "interrupted": "interrupted"}[status]
	if event == "run." {
		return
	}
	view, err := s.loadRunView(runID)
	if err != nil {
		return
	}
	var agentID string
	if err := s.store.db.QueryRow("SELECT c.agent_id FROM runs r JOIN conversations c ON c.id=r.conversation_id WHERE r.id=?", runID).Scan(&agentID); err != nil {
		return
	}
	rows, err := s.store.db.Query("SELECT id,url,secret,events,agent_id FROM webhook_subscriptions WHERE enabled=1 AND (agent_id='' OR agent_id=?)", agentID)
	if err != nil {
		return
	}
	type target struct {
		id, url, secret string
	}
	targets := []target{}
	for rows.Next() {
		var id, rawURL, secret, events, subscribedAgent string
		if err := rows.Scan(&id, &rawURL, &secret, &events, &subscribedAgent); err != nil {
			rows.Close()
			return
		}
		var declared []string
		_ = json.Unmarshal([]byte(events), &declared)
		matched := false
		for _, item := range declared {
			if item == event {
				matched = true
			}
		}
		if !matched {
			continue
		}
		targets = append(targets, target{id: id, url: rawURL, secret: secret})
	}
	rows.Close()
	if len(targets) == 0 {
		return
	}
	payload, _ := json.Marshal(map[string]any{"event": event, "run": view, "created": time.Now().UnixMilli()})
	for _, item := range targets {
		deliveryID := "wd_" + strings.TrimPrefix(id2(), "wh_")
		eventID := runID + ":" + event
		if _, err := s.store.db.Exec("INSERT OR IGNORE INTO webhook_deliveries(id,subscription_id,event,event_id,run_id,payload,status,created) VALUES(?,?,?,?,?,?,'pending',?)", deliveryID, item.id, event, eventID, runID, string(payload), time.Now().UnixMilli()); err != nil {
			continue
		}
		secret, err := s.store.decrypt(item.secret)
		if err != nil {
			continue
		}
		go s.deliverWebhook(deliveryID, item.url, secret, payload, event, eventID)
	}
}

// deliverWebhook retries three times with a fixed backoff, then marks failed.
func (s *Server) deliverWebhook(deliveryID, target, secret string, payload []byte, event, eventID string) {
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	backoff := []time.Duration{0, 2 * time.Second, 5 * time.Second}
	attempts := 0
	var lastErr string
	for _, wait := range backoff {
		if wait > 0 {
			time.Sleep(wait)
		}
		attempts++
		timestamp := time.Now().Unix()
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(strings.Join([]string{eventID, event, strconvItoa(timestamp)}, ".")))
		mac.Write(payload)
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, bytes.NewReader(payload))
		if err != nil {
			lastErr = "request_invalid"
			break
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-SiAgent-Event", event)
		request.Header.Set("X-SiAgent-Event-Id", eventID)
		request.Header.Set("X-SiAgent-Timestamp", strconvItoa(timestamp))
		request.Header.Set("X-SiAgent-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		response, err := client.Do(request)
		if err != nil {
			lastErr = "network"
			continue
		}
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			_, _ = s.store.db.Exec("UPDATE webhook_deliveries SET status='sent',attempts=?,last_error='' WHERE id=?", attempts, deliveryID)
			return
		}
		lastErr = "status_" + strconvItoa(int64(response.StatusCode))
	}
	_, _ = s.store.db.Exec("UPDATE webhook_deliveries SET status='failed',attempts=?,last_error=? WHERE id=?", attempts, lastErr, deliveryID)
}

func strconvItoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

// sendTestWebhook posts a signed ping so an administrator can verify the
// endpoint before enabling real events.
func (s *Server) sendTestWebhook(w http.ResponseWriter, r *http.Request) {
	var target, secret string
	err := s.store.db.QueryRow("SELECT url,secret FROM webhook_subscriptions WHERE id=?", r.PathValue("id")).Scan(&target, &secret)
	if err == sql.ErrNoRows {
		fail(w, 404, "Webhook 不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	decrypted, err := s.store.decrypt(secret)
	if err != nil {
		internal(w)
		return
	}
	payload, _ := json.Marshal(map[string]any{"event": "ping", "created": time.Now().UnixMilli()})
	eventID := "ping_" + strconvItoa(time.Now().UnixMilli())
	go s.deliverWebhook("", target, decrypted, payload, "ping", eventID)
	respond(w, 202, map[string]bool{"queued": true})
}

var _ = errors.New
