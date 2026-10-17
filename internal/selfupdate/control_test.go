package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutomaticUpdateWithNoNewVersionDoesNotDownloadOrLock(t *testing.T) {
	for _, current := range []string{"v1.2.3", "v1.2.4"} {
		t.Run(current, func(t *testing.T) {
			t.Setenv("SIAGENT_GITHUB_MIRRORS", "")
			calls := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls <- r.URL.Path
				fmt.Fprintln(w, "v1.2.3")
			}))
			defer server.Close()
			manager := &Manager{root: t.TempDir(), releaseBase: server.URL, client: server.Client(), config: Config{Version: current}, status: Status{Current: current, Supported: true, Phase: "idle"}}
			if err := manager.BeginUpdate(); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(3 * time.Second); manager.Status().Phase == "checking" && time.Now().Before(deadline); {
				time.Sleep(10 * time.Millisecond)
			}
			status := manager.Status()
			if status.Phase != "idle" || status.Automatic || status.Available {
				t.Fatal("no update should have been scheduled", status)
			}
			if len(calls) != 1 {
				t.Fatal("unexpected release downloads", len(calls))
			}
			if _, err := os.Stat(filepath.Join(manager.root, ".update-lock")); !os.IsNotExist(err) {
				t.Fatal("no-op update acquired install lock", err)
			}
		})
	}
}

func TestPrivateControlSocketCanStartUpdateAndRejectDuplicates(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, ".update-work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{root: root, work: work, status: Status{Supported: true, Phase: "ready", Latest: "v1.0.1"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeControl, err := ListenControl(ctx, root, manager)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControl()
	if info, err := os.Stat(controlPath(root)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("control socket permissions", err)
	}
	status, err := ControlRequest(ctx, root, http.MethodGet, "/status")
	if err != nil || status.Phase != "ready" {
		t.Fatal(status, err)
	}
	status, err = ControlRequest(ctx, root, http.MethodPost, "/update")
	if err != nil || !status.Automatic {
		t.Fatal(status, err)
	}
	if _, err := ControlRequest(ctx, root, http.MethodPost, "/update"); err == nil {
		t.Fatal("duplicate update accepted")
	}
	raw, err := os.ReadFile(filepath.Join(root, ".update-prepared.json"))
	if err != nil || !strings.Contains(string(raw), `"Automatic":true`) {
		t.Fatal("automatic intent not persisted", string(raw), err)
	}
}

func TestVersionRetryRequiresSameVerifiedArchive(t *testing.T) {
	root := t.TempDir()
	staged, destination := filepath.Join(root, "stage"), filepath.Join(root, "version")
	for _, directory := range []string{staged, destination} {
		os.Mkdir(directory, 0700)
		os.WriteFile(filepath.Join(directory, ".archive-sha256"), []byte(strings.Repeat("a", 64)), 0600)
	}
	if err := installVersion(staged, destination); err != nil {
		t.Fatal("identical retry failed", err)
	}
	os.WriteFile(filepath.Join(destination, ".archive-sha256"), []byte(strings.Repeat("b", 64)), 0600)
	if err := installVersion(staged, destination); err == nil {
		t.Fatal("different target overwritten")
	}
}

func TestPreparedOlderVersionIsCleanedInsteadOfDowngraded(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, ".update-old")
	os.Mkdir(work, 0700)
	os.Mkdir(filepath.Join(root, ".update-lock"), 0700)
	if err := writePrepared(root, work, "v1.0.0", true, true); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{root: root, config: Config{Version: "v1.0.1"}}
	manager.recoverPrepared()
	if manager.Status().Automatic || manager.Status().Phase == "ready" {
		t.Fatal("stale update resumed")
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatal("stale stage retained", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".update-lock")); !os.IsNotExist(err) {
		t.Fatal("stale lock retained", err)
	}
}

func TestInstallerDataPathsAllowFirstUpdateAndRejectBackupRecursion(t *testing.T) {
	for _, key := range []string{"SIAGENT_DEPLOYMENT", "INVOCATION_ID", "NOTIFY_SOCKET", "SUPERVISOR_ENABLED", "container"} {
		t.Setenv(key, "")
	}
	if containerEnvironment() {
		t.Skip("installer updates unavailable in container")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(root, "versions", "v1.0.0")
	os.MkdirAll(version, 0700)
	executable := filepath.Join(version, "siagent")
	os.WriteFile(executable, nil, 0700)
	os.WriteFile(filepath.Join(root, ".siagent-install"), nil, 0600)
	os.Symlink(version, filepath.Join(root, "current"))
	config := Config{Executable: executable, Version: "v1.0.0", Listen: "127.0.0.1:8080", Data: filepath.Join(root, "new", "data")}
	if manager := New(config); !manager.Status().Supported {
		t.Fatal("first update rejected", manager.Status())
	}
	for _, data := range []string{root, filepath.Join(root, "versions"), filepath.Join(root, "versions", "other", "data"), filepath.Join(root, "backups"), filepath.Join(root, "backups", "data")} {
		config.Data = data
		if manager := New(config); manager.Status().Supported {
			t.Fatal("unsafe data path accepted", data)
		}
	}
}
