package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ilinkGateway stands in for the Tencent personal-WeChat gateway: long polls
// return queued messages, sends are recorded. The real gateway holds the poll
// until a message arrives; the fixture holds it briefly so the loop behaves the
// same without slowing the test down.
type ilinkGateway struct {
	mu      sync.Mutex
	queue   []map[string]any
	sent    []map[string]any
	updates int
}

func (g *ilinkGateway) push(message map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queue = append(g.queue, message)
}

func (g *ilinkGateway) sentMessages() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.sent...)
}

func (g *ilinkGateway) handler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ilink/bot/getupdates":
		var body struct {
			GetUpdatesBuf string `json:"get_updates_buf"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// A single long poll waits briefly for a message, then answers empty.
		deadline := time.After(200 * time.Millisecond)
		for {
			g.mu.Lock()
			if len(g.queue) > 0 {
				batch := g.queue
				g.queue = nil
				g.updates++
				g.mu.Unlock()
				json.NewEncoder(w).Encode(map[string]any{
					"ret":                    0,
					"get_updates_buf":        "cursor-" + time.Now().Format("150405.000"),
					"longpolling_timeout_ms": 2000,
					"msgs":                   batch,
				})
				return
			}
			g.mu.Unlock()
			select {
			case <-r.Context().Done():
				return
			case <-deadline:
				json.NewEncoder(w).Encode(map[string]any{
					"ret":             0,
					"get_updates_buf": body.GetUpdatesBuf,
				})
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	case "/ilink/bot/sendmessage":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t := r.Context()
			_ = t
		}
		g.mu.Lock()
		g.sent = append(g.sent, body)
		g.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ret": 0})
	default:
		http.NotFound(w, r)
	}
}

func TestPersonalWechatLongPollDrivesTheGenericChannelCore(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	gateway := &ilinkGateway{}
	server := httptest.NewServer(http.HandlerFunc(gateway.handler))
	t.Cleanup(server.Close)
	response := f.request(t, "POST", "/api/channels", map[string]any{
		"provider": "weixin",
		"config":   map[string]string{"bot_id": "bot-id", "base_url": server.URL},
		"secrets":  map[string]string{"token": "private-token"},
		"agent_id": "default",
		"enabled":  true,
		"confirm":  true,
	}, true, nil)
	requireStatus(t, response, 201)
	var c Channel
	if err := json.Unmarshal(response.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Body.String(), "private-token") {
		t.Fatal("channel response leaked the bot token")
	}
	defer f.server.closeChannelConnections()

	gateway.push(map[string]any{
		"message_id":    9001,
		"from_user_id":  "member@im.wechat",
		"message_type":  1,
		"context_token": "ctx-1",
		"item_list": []map[string]any{
			{"type": 1, "text_item": map[string]string{"text": "发票政策是什么"}},
		},
	})
	// The connection is opened by the channel tick, then the long poll returns
	// the queued message and the inbox row is admitted on a later tick.
	deadline := time.Now().Add(5 * time.Second)
	for len(f.runtime.requests) == 0 && time.Now().Before(deadline) {
		tickChannel(t, f, time.Now())
		time.Sleep(20 * time.Millisecond)
	}
	if len(f.runtime.requests) != 1 {
		t.Fatal("long-poll message did not create exactly one run")
	}
	completeChannelRun(t, f, f.runtime.requests[0]["run_id"].(string), "发票政策见知识库")
	for len(gateway.sentMessages()) == 0 && time.Now().Before(deadline) {
		tickChannel(t, f, time.Now())
		time.Sleep(20 * time.Millisecond)
	}
	sent := gateway.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("reply was not sent over the ilink gateway: %d", len(sent))
	}
	message, _ := sent[0]["msg"].(map[string]any)
	if message["to_user_id"] != "member@im.wechat" || message["context_token"] != "ctx-1" || message["message_type"] != float64(2) {
		t.Fatalf("unexpected outbound scope: %+v", message)
	}
	items, _ := message["item_list"].([]any)
	if len(items) != 1 {
		t.Fatalf("unexpected outbound items: %+v", items)
	}
	item, _ := items[0].(map[string]any)
	text, _ := item["text_item"].(map[string]any)
	if text["text"] != "发票政策见知识库" {
		t.Fatalf("unexpected outbound text: %+v", text)
	}
	var state string
	if err := f.store.db.QueryRow("SELECT state FROM channel_outbox WHERE channel_id=? AND kind='final'", c.ID).Scan(&state); err != nil || state != "sent" {
		t.Fatal("delivery was not confirmed", state, err)
	}
	// The same vendor message ID must never create a second run.
	gateway.push(map[string]any{
		"message_id":    9001,
		"from_user_id":  "member@im.wechat",
		"message_type":  1,
		"context_token": "ctx-1",
		"item_list": []map[string]any{
			{"type": 1, "text_item": map[string]string{"text": "发票政策是什么"}},
		},
	})
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tickChannel(t, f, time.Now())
		time.Sleep(20 * time.Millisecond)
	}
	if len(f.runtime.requests) != 1 {
		t.Fatal("duplicate long-poll delivery created another run")
	}
}

func TestPersonalWechatConnectionStatusAndCredentialTest(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	gateway := &ilinkGateway{}
	server := httptest.NewServer(http.HandlerFunc(gateway.handler))
	t.Cleanup(server.Close)
	response := f.request(t, "POST", "/api/channels", map[string]any{
		"provider": "weixin",
		"config":   map[string]string{"bot_id": "bot-id", "base_url": server.URL},
		"secrets":  map[string]string{"token": "private-token"},
		"agent_id": "default",
		"enabled":  true,
		"confirm":  true,
	}, true, nil)
	requireStatus(t, response, 201)
	var c Channel
	json.Unmarshal(response.Body.Bytes(), &c)
	defer f.server.closeChannelConnections()
	if err := f.server.ChannelTick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if status := f.server.channelConnectionStatus(c.ID); status.State == "connected" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status := f.server.channelConnectionStatus(c.ID); status.State != "connected" {
		t.Fatalf("long poll was not reported as connected: %+v", status)
	}
	requireStatus(t, f.request(t, "POST", "/api/channels/"+c.ID+"/test", nil, true, nil), 200)
}
