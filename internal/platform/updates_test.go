package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"siagent/internal/selfupdate"
	"testing"
)

func TestUpdateAuthenticationModesAndShutdownBarrier(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	for _, path := range []string{"/api/updates", "/api/updates/check", "/api/updates/start", "/api/updates/prepare", "/api/updates/apply"} {
		method := "POST"
		if path == "/api/updates" {
			method = "GET"
		}
		requireStatus(t, f.request(t, method, path, map[string]bool{"confirm": true}, false, nil), 401)
	}
	requireStatus(t, f.request(t, "GET", "/api/updates", nil, true, nil), 200)
	f.server.ConfigureUpdates(selfupdate.New(selfupdate.Config{Version: "v1.0.0"}), func() { t.Error("unsupported update stopped application") })
	requireStatus(t, f.request(t, "POST", "/api/updates/start", map[string]bool{"confirm": false}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/updates/start", map[string]bool{"confirm": true}, true, nil), 409)
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

func TestAutomaticUpdateWaitsForExistingWorkAndCancellationReopensAdmission(t *testing.T) {
	for _, key := range []string{"SIAGENT_DEPLOYMENT", "INVOCATION_ID", "NOTIFY_SOCKET", "SUPERVISOR_ENABLED", "container"} {
		t.Setenv(key, "")
	}
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	run := f.start(t, f.conversation(t), "existing work")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, work := filepath.Join(root, "versions", "old"), filepath.Join(root, ".update-work")
	for _, directory := range []string{old, work} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, ".siagent-install"), nil, 0600)
	executable := filepath.Join(old, "siagent")
	os.WriteFile(executable, nil, 0700)
	os.Symlink(old, filepath.Join(root, "current"))
	raw, _ := json.Marshal(map[string]any{"Work": work, "Version": "v1.0.1", "Ready": true, "Automatic": true})
	os.WriteFile(filepath.Join(root, ".update-prepared.json"), raw, 0600)
	manager := selfupdate.New(selfupdate.Config{Executable: executable, Data: f.directory, Listen: "127.0.0.1:8080", Version: "v1.0.0"})
	if !manager.Status().Supported {
		t.Skip("installer updates unavailable in container")
	}
	f.server.ConfigureUpdates(manager, func() { t.Error("busy service stopped") })
	if err := f.server.UpdateTick(); err != nil {
		t.Fatal(err)
	}
	if manager.Status().Phase != "waiting" || !f.server.updateDraining.Load() || f.server.stopping {
		t.Fatal("active work did not drain", manager.Status())
	}
	requireStatus(t, f.request(t, "POST", "/api/conversations", nil, true, nil), 503)
	requireStatus(t, f.request(t, "GET", "/api/updates", nil, true, nil), 200)
	requireStatus(t, f.request(t, "POST", "/api/runs/"+run+"/cancel", nil, true, nil), 202)
	requireStatus(t, f.request(t, "POST", "/api/updates/cancel", nil, true, nil), 200)
	if f.server.updateDraining.Load() || manager.Status().Automatic {
		t.Fatal("cancel left admission blocked")
	}
	requireStatus(t, f.request(t, "POST", "/api/conversations", nil, true, nil), 201)
}
