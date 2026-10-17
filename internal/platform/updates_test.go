package platform

import (
	"siagent/internal/selfupdate"
	"testing"
)

func TestUpdateAuthenticationModesAndShutdownBarrier(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	for _, path := range []string{"/api/updates", "/api/updates/check", "/api/updates/prepare", "/api/updates/apply"} {
		method := "POST"
		if path == "/api/updates" {
			method = "GET"
		}
		requireStatus(t, f.request(t, method, path, map[string]bool{"confirm": true}, false, nil), 401)
	}
	requireStatus(t, f.request(t, "GET", "/api/updates", nil, true, nil), 200)
	f.server.ConfigureUpdates(selfupdate.New(selfupdate.Config{Version: "v1.0.0"}), func() { t.Error("unsupported update stopped application") })
	requireStatus(t, f.request(t, "POST", "/api/updates/prepare", nil, true, nil), 409)
	requireStatus(t, f.request(t, "POST", "/api/updates/apply", map[string]bool{"confirm": false}, true, nil), 400)
	f.start(t, f.conversation(t), "busy task")
	requireStatus(t, f.request(t, "POST", "/api/updates/apply", map[string]bool{"confirm": true}, true, nil), 409)
	if f.server.stopping {
		t.Fatal("busy update stopped scheduler")
	}
	f.server.updating.Store(true)
	requireStatus(t, f.request(t, "POST", "/api/conversations", nil, true, nil), 503)
	requireStatus(t, f.request(t, "PUT", "/api/agent", map[string]any{"name": "blocked", "enabled": true}, true, nil), 503)
	requireStatus(t, f.request(t, "GET", "/api/agents", nil, true, nil), 200)
}
