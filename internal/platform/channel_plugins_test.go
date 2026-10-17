package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"siagent/internal/channels"
)

// This plugin deliberately uses a different callback namespace and protocol.
// It has no access to the store, scheduler, or any vendor-specific core handler.
type fixtureWebhookPlugin struct {
	beforeVerify func()
	sent         []channels.Delivery
}

func (*fixtureWebhookPlugin) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "fixture-hook", Name: "Fixture webhook", Transport: "webhook", MaxTextBytes: 1024, IdentityFields: []string{"account"}, Fields: []channels.Field{
		{Key: "account", Label: "Account", Required: true},
		{Key: "token", Label: "Token", Secret: true, Required: true},
	}}
}
func (*fixtureWebhookPlugin) Validate(channels.Config) error { return nil }
func (*fixtureWebhookPlugin) CallbackPath(id string) string  { return "/callbacks/fixture/" + id }
func (p *fixtureWebhookPlugin) Open(context.Context, channels.Config, channels.Receive) (channels.Connection, error) {
	return p, nil
}
func (p *fixtureWebhookPlugin) Send(_ context.Context, d channels.Delivery) channels.Result {
	p.sent = append(p.sent, d)
	return channels.Result{State: "sent"}
}
func (*fixtureWebhookPlugin) Status() channels.Status { return channels.Status{State: "webhook"} }
func (*fixtureWebhookPlugin) Close()                  {}
func (p *fixtureWebhookPlugin) ServeCallback(w http.ResponseWriter, r *http.Request, c channels.Config, receive channels.Receive, verified func(context.Context) error) {
	if r.Header.Get("Authorization") != c.Secrets["token"] {
		http.Error(w, "unauthorized", 403)
		return
	}
	if r.Method == "GET" {
		if p.beforeVerify != nil {
			p.beforeVerify()
		}
		if err := verified(r.Context()); err != nil {
			http.Error(w, "verification failed", 503)
			return
		}
		w.WriteHeader(200)
		return
	}
	if err := receive(r.Context(), channels.Incoming{ID: "hook-message", Session: c.Values["account"], Kind: "text", Text: "plugin question"}); err != nil {
		http.Error(w, "admission failed", 503)
		return
	}
	w.WriteHeader(200)
}

func TestWebhookPluginUsesGenericCoreAndVersionedVerification(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	plugin := &fixtureWebhookPlugin{}
	if err := f.server.ConfigureChannelPlugins("node", "unused", []channels.Plugin{
		func(channels.PluginOptions) (channels.Provider, error) { return plugin, nil },
	}); err != nil {
		t.Fatal(err)
	}
	defer f.server.closeChannelConnections()
	response := f.request(t, "POST", "/api/channels", map[string]any{
		"provider": "fixture-hook", "config": map[string]string{"account": "member"},
		"secrets": map[string]string{"token": "hook-secret"}, "agent_id": "default", "enabled": true, "confirm": true,
	}, true, nil)
	requireStatus(t, response, 201)
	var c Channel
	if err := json.Unmarshal(response.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("Authorization", token)
		response := httptest.NewRecorder()
		f.server.CallbackHandler().ServeHTTP(response, request)
		return response
	}
	requireStatus(t, call("GET", c.CallbackPath, "wrong"), 403)
	loaded, _, err := f.server.loadChannel(context.Background(), c.ID)
	if err != nil || loaded.Verified {
		t.Fatal("unauthenticated verification persisted", err)
	}
	requireStatus(t, call("GET", "/callbacks/wrong/"+c.ID, "hook-secret"), 404)
	requireStatus(t, call("GET", c.CallbackPath, "hook-secret"), 200)
	loaded, _, err = f.server.loadChannel(context.Background(), c.ID)
	if err != nil || !loaded.Verified {
		t.Fatal("plugin verification not persisted", err)
	}
	requireStatus(t, call("POST", c.CallbackPath, "hook-secret"), 200)
	requireStatus(t, call("POST", c.CallbackPath, "hook-secret"), 200)
	tickChannel(t, f, time.Now())
	if len(f.runtime.requests) != 1 {
		t.Fatal("plugin input was not deduplicated and scheduled")
	}
	completeChannelRun(t, f, f.runtime.requests[0]["run_id"].(string), "plugin answer")
	tickChannel(t, f, time.Now())
	if len(plugin.sent) != 1 || plugin.sent[0].Text != "plugin answer" || plugin.sent[0].Target != "member" {
		t.Fatal("plugin output did not pass through the generic delivery queue", plugin.sent)
	}
	plugin.beforeVerify = func() {
		if _, err := f.store.db.Exec("UPDATE channels SET version=version+1,verified=0 WHERE id=?", c.ID); err != nil {
			t.Fatal(err)
		}
	}
	requireStatus(t, call("GET", c.CallbackPath, "hook-secret"), 503)
	loaded, _, err = f.server.loadChannel(context.Background(), c.ID)
	if err != nil || loaded.Verified {
		t.Fatal("stale plugin verification persisted", err)
	}
}

func TestBundledChannelPluginsCanBeOmittedAndRestored(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	c := createChannel(t, f, true)
	requireStatus(t, callback(t, f, c, "901", "member", "retained question"), 200)
	if err := f.server.ConfigureChannelPlugins("node", "unused", nil); err != nil {
		t.Fatal(err)
	}
	response := f.request(t, "GET", "/api/channel-providers", nil, true, nil)
	requireStatus(t, response, 200)
	if strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatal("bundled vendors hardwired in core", response.Body.String())
	}
	response = f.request(t, "GET", "/api/channels", nil, true, nil)
	requireStatus(t, response, 200)
	var values []Channel
	if err := json.Unmarshal(response.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Available || values[0].Status.State != "unavailable" {
		t.Fatal("missing plugin configuration not retained", values)
	}
	request := httptest.NewRequest("GET", "/callbacks/wecom/"+c.ID, nil)
	recorder := httptest.NewRecorder()
	f.server.CallbackHandler().ServeHTTP(recorder, request)
	requireStatus(t, recorder, 404)
	if err := f.server.ConfigureChannelAdapters("node", "runtime/dist/channel-wecom.js"); err != nil {
		t.Fatal(err)
	}
	restored, secrets, err := f.server.loadChannel(context.Background(), c.ID)
	if err != nil || !restored.Available || secrets["secret"] != "channel-secret" {
		t.Fatal("restored plugin lost configuration", err)
	}
	requireStatus(t, callback(t, f, c, "901", "member", "retained question"), 200)
	var count int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_inbox WHERE channel_id=?", c.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("restored plugin lost deduplication history", err)
	}
}
