package platform

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"siagent/internal/channelplugins/wecomapp"
	wecom "siagent/internal/channelplugins/wecomapp/protocol"
	agentruntime "siagent/internal/runtime"
)

const callbackKey = "BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc"

func createChannel(t *testing.T, f *fixture, enabled bool) wecomChannel {
	t.Helper()
	response := f.request(t, "POST", "/api/wecom/channels", map[string]any{"corp_id": "wx-local", "app_id": 7, "agent_id": "default", "enabled": enabled, "secret": "channel-secret", "token": "callback-token", "aes_key": callbackKey, "confirm": true}, true, nil)
	requireStatus(t, response, 201)
	var c wecomChannel
	json.Unmarshal(response.Body.Bytes(), &c)
	if strings.Contains(response.Body.String(), "channel-secret") || strings.Contains(response.Body.String(), callbackKey) || strings.Contains(response.Body.String(), "callback-token") {
		t.Fatal("channel response leaked credential")
	}
	return c
}
func callbackFrame(corp, text string) string {
	key, _ := base64.RawStdEncoding.DecodeString(callbackKey)
	data := append(bytes.Repeat([]byte{7}, 16), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(data[16:], uint32(len(text)))
	data = append(data, []byte(text+corp)...)
	padding := 32 - len(data)%32
	data = append(data, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(data, data)
	return base64.StdEncoding.EncodeToString(data)
}
func callback(t *testing.T, f *fixture, c wecomChannel, msgID, member, text string) *httptest.ResponseRecorder {
	t.Helper()
	message := wecom.Message{CorpID: c.CorpID, AppID: c.AppID, Member: member, ID: msgID, Type: "text", Content: text, Created: time.Now().Unix()}
	raw, _ := xml.Marshal(message)
	return callbackXML(f, c, string(raw), "callback-token")
}
func callbackXML(f *fixture, c wecomChannel, plain, token string) *httptest.ResponseRecorder {
	encrypted := callbackFrame(c.CorpID, plain)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	query := url.Values{"timestamp": {timestamp}, "nonce": {"nonce"}, "msg_signature": {wecom.Signature(token, timestamp, "nonce", encrypted)}}
	body, _ := xml.Marshal(wecom.Envelope{Encrypted: encrypted})
	request := httptest.NewRequest("POST", "http://localhost"+c.CallbackPath+"?"+query.Encode(), bytes.NewReader(body))
	response := httptest.NewRecorder()
	f.server.CallbackHandler().ServeHTTP(response, request)
	return response
}

type wecomTransport struct{ endpoint *url.URL }

func (t wecomTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = t.endpoint.Scheme
	clone.URL.Host = t.endpoint.Host
	return http.DefaultTransport.RoundTrip(clone)
}
func wecomAPI(t *testing.T, f *fixture) (chan string, chan map[string]any) {
	t.Helper()
	modes := make(chan string, 32)
	sent := make(chan map[string]any, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			w.Write([]byte(`{"errcode":0,"access_token":"access-canary","expires_in":7200}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		sent <- body
		mode := "ok"
		select {
		case mode = <-modes:
		default:
		}
		switch mode {
		case "ok":
			w.Write([]byte(`{"errcode":0}`))
		case "retry":
			w.Write([]byte(`{"errcode":45009}`))
		case "fail":
			w.Write([]byte(`{"errcode":81013}`))
		case "unknown":
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	applicationClient(t, f).HTTP.Transport = wecomTransport{endpoint}
	return modes, sent
}
func tickChannel(t *testing.T, f *fixture, now time.Time) {
	t.Helper()
	if err := f.server.ChannelTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}
func completeChannelRun(t *testing.T, f *fixture, run, answer string) {
	t.Helper()
	emit(t, f, run, "run_started", 1)
	raw, _ := json.Marshal(map[string]string{"answer": answer})
	if err := f.server.HandleEvent(agentruntime.Event{Type: "run_completed", RunID: run, Sequence: 2, Data: raw}); err != nil {
		t.Fatal(err)
	}
	emit(t, f, run, "run_released", 3)
}
func TestWecomSignedInboxDedupIsolationAndNewConversation(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	c := createChannel(t, f, true)
	_, sent := wecomAPI(t, f)
	requireStatus(t, f.request(t, "GET", "/api/wecom/channels", nil, false, nil), 401)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	encrypted := callbackFrame(c.CorpID, "verification-echo")
	q := url.Values{"timestamp": {timestamp}, "nonce": {"nonce"}, "echostr": {encrypted}, "msg_signature": {wecom.Signature("callback-token", timestamp, "nonce", encrypted)}}
	verify := httptest.NewRecorder()
	f.server.CallbackHandler().ServeHTTP(verify, httptest.NewRequest("GET", "http://localhost"+c.CallbackPath+"?"+q.Encode(), nil))
	requireStatus(t, verify, 200)
	if verify.Body.String() != "verification-echo" {
		t.Fatal("URL verification failed")
	}
	message := wecom.Message{CorpID: c.CorpID, AppID: c.AppID, Member: "member-a", ID: "123", Type: "text", Content: "invoice policy", Created: 10}
	raw, _ := xml.Marshal(message)
	var wg sync.WaitGroup
	responses := make(chan int, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); responses <- callbackXML(f, c, string(raw), "callback-token").Code }()
	}
	wg.Wait()
	close(responses)
	for status := range responses {
		if status != 200 {
			t.Fatal(status)
		}
	}
	if len(f.runtime.requests) != 0 {
		t.Fatal("callback executed model before durable acknowledgement")
	}
	tickChannel(t, f, time.Now())
	if len(f.runtime.requests) != 1 {
		t.Fatal("duplicate callbacks created more than one run")
	}
	requireStatus(t, callback(t, f, c, "124", "member-b", "other question"), 200)
	tickChannel(t, f, time.Now())
	if len(f.runtime.requests) != 2 || f.runtime.requests[0]["conversation_id"] == f.runtime.requests[1]["conversation_id"] {
		t.Fatal("members shared a conversation")
	}
	first := f.runtime.requests[0]["run_id"].(string)
	completeChannelRun(t, f, first, strings.Repeat("中文回复", 500))
	tickChannel(t, f, time.Now())
	tickChannel(t, f, time.Now())
	tickChannel(t, f, time.Now())
	if len(sent) < 2 {
		t.Fatal("long response was not split")
	}
	for len(sent) > 0 {
		body := <-sent
		content := body["text"].(map[string]any)["content"].(string)
		if len(content) > 2048 || body["touser"] != "member-a" {
			t.Fatal("outgoing bounds or recipient incorrect")
		}
	}
	before := f.runtime.requests[0]["conversation_id"].(string)
	requireStatus(t, f.request(t, "POST", "/api/conversations/"+before+"/runs", map[string]string{"input": "admin cannot inject"}, true, nil), 403)
	requireStatus(t, callback(t, f, c, "125", "member-a", "/new"), 200)
	tickChannel(t, f, time.Now())
	requireStatus(t, callback(t, f, c, "126", "member-a", "fresh context"), 200)
	tickChannel(t, f, time.Now())
	if len(f.runtime.requests) != 3 || f.runtime.requests[2]["conversation_id"] == before {
		t.Fatal("/new did not reset member session")
	}
	message.Content = "changed"
	raw, _ = xml.Marshal(message)
	requireStatus(t, callbackXML(f, c, string(raw), "callback-token"), 403)
	requireStatus(t, callbackXML(f, c, string(raw), "wrong-token"), 403)
	message.CorpID = "different"
	raw, _ = xml.Marshal(message)
	requireStatus(t, callbackXML(f, c, string(raw), "callback-token"), 403)
	response := f.request(t, "GET", "/api/wecom/channels", nil, true, nil)
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "channel-secret") || strings.Contains(response.Body.String(), callbackKey) {
		t.Fatal("catalog leaked credentials")
	}
}
func TestWecomHintRetryUnknownAndRecoveryNeverReplay(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	c := createChannel(t, f, true)
	modes, sent := wecomAPI(t, f)
	now := time.Now()
	requireStatus(t, callback(t, f, c, "100", "member", "slow question"), 200)
	tickChannel(t, f, now)
	tickChannel(t, f, now.Add(11*time.Second))
	tickChannel(t, f, now.Add(12*time.Second))
	if len(sent) != 1 {
		t.Fatal("processing hint was not sent exactly once")
	}
	<-sent
	run := f.runtime.requests[0]["run_id"].(string)
	completeChannelRun(t, f, run, strings.Repeat("answer ", 600))
	modes <- "retry"
	tickChannel(t, f, now.Add(13*time.Second))
	tickChannel(t, f, now.Add(14*time.Second))
	if len(sent) != 1 {
		t.Fatal("retry ignored backoff")
	}
	modes <- "unknown"
	tickChannel(t, f, now.Add(16*time.Second))
	tickChannel(t, f, now.Add(30*time.Second))
	if len(sent) != 2 {
		t.Fatal("unknown outcome retried or later segment was sent")
	}
	var unknown, blocked int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE state='unknown'").Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE state='blocked'").Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if unknown != 1 || blocked < 1 {
		t.Fatal("unknown outcome did not stop remaining parts")
	}
	var delivery string
	f.store.db.QueryRow("SELECT id FROM channel_outbox WHERE state='unknown'").Scan(&delivery)
	requireStatus(t, f.request(t, "POST", "/api/wecom/deliveries/"+delivery+"/retry", nil, true, nil), 409)
	if _, err := f.store.db.Exec("UPDATE channel_outbox SET state='sending' WHERE id=?", delivery); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, callback(t, f, c, "101", "member", "not admitted yet"), 200)
	restarted, err := New(f.store, f.runtime, func() bool { return true }, "")
	if err != nil {
		t.Fatal(err)
	}
	f.server = restarted
	endpointClient := wecom.NewClient()
	endpointClient.HTTP.Transport = applicationClient(t, f).HTTP.Transport
	_ = endpointClient
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE state='unknown'").Scan(&unknown); err != nil || unknown != 1 {
		t.Fatal("sending recovered as retryable")
	}
	var state string
	f.store.db.QueryRow("SELECT state FROM channel_inbox WHERE msg_id='101'").Scan(&state)
	if state != "rejected" || len(f.runtime.requests) != 1 {
		t.Fatal("restart replayed pending admission")
	}
}
func TestWecomChannelBindingAndArchiveGuards(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	a := saveTestAgent(t, f, Agent{Name: "Bound", ModelID: 1, Enabled: true})
	c := createChannel(t, f, false)
	update := map[string]any{"corp_id": c.CorpID, "app_id": c.AppID, "agent_id": a.ID, "enabled": false}
	requireStatus(t, f.request(t, "PUT", "/api/wecom/channels/"+c.ID, update, true, nil), 200)
	a.Enabled = false
	saveTestAgent(t, f, a)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+a.ID, nil, true, nil), 409)
	requireStatus(t, callback(t, f, c, "1", "member", "disabled"), 503)
	update["enabled"] = true
	update["confirm"] = true
	requireStatus(t, f.request(t, "PUT", "/api/wecom/channels/"+c.ID, update, true, nil), 409)
	requireStatus(t, f.request(t, "DELETE", "/api/wecom/channels/"+c.ID, nil, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+a.ID, nil, true, nil), 200)
	requireStatus(t, f.request(t, "PUT", "/api/wecom/channels/"+c.ID, update, true, nil), 409)
	_, secret, err := f.server.channel(c.ID)
	if err != nil || secret.Secret != "channel-secret" {
		t.Fatal("blank update did not preserve encrypted credential")
	}
	var stored string
	if err := f.store.db.QueryRow("SELECT secrets FROM channels WHERE id=?", c.ID).Scan(&stored); err != nil || strings.Contains(stored, "channel-secret") {
		t.Fatal("channel credential saved in plaintext")
	}
}
func TestWecomKnownFailureCanRetryDeliveryWithoutAnotherRun(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	c := createChannel(t, f, true)
	modes, sent := wecomAPI(t, f)
	requireStatus(t, callback(t, f, c, "900", "member", "question"), 200)
	tickChannel(t, f, time.Now())
	completeChannelRun(t, f, f.runtime.requests[0]["run_id"].(string), "reply")
	modes <- "fail"
	tickChannel(t, f, time.Now())
	var delivery string
	f.store.db.QueryRow("SELECT id FROM channel_outbox WHERE state='failed'").Scan(&delivery)
	if delivery == "" {
		t.Fatal("known rejection did not fail")
	}
	requireStatus(t, f.request(t, "POST", "/api/wecom/deliveries/"+delivery+"/retry", nil, true, nil), 200)
	tickChannel(t, f, time.Now())
	if len(sent) != 2 || len(f.runtime.requests) != 1 {
		t.Fatal("delivery retry reran model or failed to send")
	}
}

func TestWecomRetryLimitAndDisabledDelivery(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	c := createChannel(t, f, true)
	modes, sent := wecomAPI(t, f)
	now := time.Now()
	requireStatus(t, callback(t, f, c, "901", "member", "question"), 200)
	tickChannel(t, f, now)
	completeChannelRun(t, f, f.runtime.requests[0]["run_id"].(string), "reply")
	if _, err := f.store.db.Exec("UPDATE channels SET enabled=0 WHERE id=?", c.ID); err != nil {
		t.Fatal(err)
	}
	tickChannel(t, f, now)
	if len(sent) != 0 {
		t.Fatal("disabled channel sent a reply")
	}
	if _, err := f.store.db.Exec("UPDATE channels SET enabled=1 WHERE id=?", c.ID); err != nil {
		t.Fatal(err)
	}
	for _, delay := range []time.Duration{0, 3 * time.Second, 10 * time.Second} {
		modes <- "retry"
		tickChannel(t, f, now.Add(delay))
	}
	tickChannel(t, f, now.Add(time.Hour))
	if len(sent) != 3 {
		t.Fatal("retry limit did not bound attempts")
	}
	var failed int
	f.store.db.QueryRow("SELECT count(*) FROM channel_outbox WHERE state='failed' AND attempts=3").Scan(&failed)
	if failed != 1 {
		t.Fatal("retry exhaustion not marked failed")
	}
}

func applicationClient(t *testing.T, f *fixture) *wecom.Client {
	t.Helper()
	provider, ok := f.server.channelProviders.Get("wecom-app")
	if !ok {
		t.Fatal("application plugin missing")
	}
	return provider.(*wecomapp.Provider).Client
}
