package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFetchOfficialRetriesThenMirrorsAndBounds(t *testing.T) {
	attempts := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts = append(attempts, r.URL.Path)
		if r.URL.Path == "/official" {
			http.Error(w, "unavailable", 503)
			return
		}
		fmt.Fprint(w, "verified asset")
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "asset")
	err := Fetch(context.Background(), server.Client(), server.URL+"/official", destination, 100, time.Second, []string{server.URL + "/mirror/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 4 || attempts[0] != "/official" || !strings.HasPrefix(attempts[3], "/mirror/") {
		t.Fatal("wrong fallback order", attempts)
	}
	content, _ := os.ReadFile(destination)
	if string(content) != "verified asset" {
		t.Fatal("failed attempt content retained")
	}
	if err := Fetch(context.Background(), server.Client(), server.URL+"/large", destination, 2, time.Second, nil, nil); err == nil {
		t.Fatal("oversize response accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("failed output was not removed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Fetch(ctx, server.Client(), server.URL, destination, 100, time.Second, nil, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestVersionComparisonAndModes(t *testing.T) {
	for _, tt := range []struct {
		latest, current string
		want            bool
	}{{"v1.10.0", "v1.9.9", true}, {"v1.2.3", "v1.2.3", false}, {"v1.2.2", "v1.2.3", false}, {"v1.2.3", "v1.2.3-rc.1", true}} {
		if newer(tt.latest, tt.current) != tt.want {
			t.Fatal(tt)
		}
	}
	t.Setenv("SIAGENT_DEPLOYMENT", "docker")
	m := New(Config{Version: "v1.2.3"})
	if m.Status().Supported || m.Status().Mode != "docker" {
		t.Fatal(m.Status())
	}
	if err := m.Prepare(); err == nil {
		t.Fatal("container update accepted")
	}
	t.Setenv("SIAGENT_GITHUB_MIRRORS", "")
	if len(Mirrors()) != 0 {
		t.Fatal("mirror disable ignored")
	}
	t.Setenv("SIAGENT_GITHUB_MIRRORS", "http://bad/,https://mirror.example/,https://user:pass@bad/")
	if got := Mirrors(); len(got) != 1 || got[0] != "https://mirror.example/" {
		t.Fatal(got)
	}
}

// The subprocess mimics a new application boot, including a failed migration.
func TestUpdateChild(t *testing.T) {
	mode := os.Getenv("SIAGENT_TEST_UPDATE_CHILD")
	if mode == "" {
		return
	}
	data := os.Getenv("SIAGENT_TEST_UPDATE_DATA")
	if mode == "fail" {
		os.WriteFile(filepath.Join(data, "state"), []byte("failed migration"), 0600)
		os.Exit(1)
	}
	listener, err := net.Listen("tcp", os.Getenv("SIAGENT_TEST_UPDATE_LISTEN"))
	if err != nil {
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"update_boot": os.Getenv("SIAGENT_UPDATE_BOOT"), "version": "v1.0.1"})
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	time.Sleep(2 * time.Second)
	server.Close()
	os.Exit(0)
}
func TestWorkerSuccessfulBootAndMigrationRollback(t *testing.T) {
	for _, mode := range []string{"success", "fail"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "data")
			work := filepath.Join(root, ".update-work")
			old := filepath.Join(root, "versions", "old")
			target := filepath.Join(work, "stage", "versions", "new")
			for _, directory := range []string{data, old, target, filepath.Join(root, ".update-lock")} {
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			os.WriteFile(filepath.Join(data, "siagent.lock"), nil, 0600)
			os.WriteFile(filepath.Join(data, "state"), []byte("original state"), 0600)
			os.Symlink("state", filepath.Join(data, "state-link"))
			os.Symlink(old, filepath.Join(root, "current"))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			listener.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("SIAGENT_TEST_UPDATE_DATA", data)
			t.Setenv("SIAGENT_TEST_UPDATE_LISTEN", address)
			for path, childMode := range map[string]string{old: "success", target: mode} {
				script := fmt.Sprintf("#!/bin/sh\nexport SIAGENT_TEST_UPDATE_CHILD=%s\nexec %s -test.run=TestUpdateChild\n", childMode, shellQuote(executable))
				if err := os.WriteFile(filepath.Join(path, "siagent"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			plan := Plan{Root: root, Work: work, Data: data, Old: old, Target: target, Version: "v1.0.1", Listen: address, Parent: 99999999}
			raw, _ := json.Marshal(plan)
			path := filepath.Join(work, "plan.json")
			os.WriteFile(path, raw, 0600)
			if err := RunWorker(path); err != nil {
				t.Fatal(err)
			}
			raw, err = os.ReadFile(filepath.Join(root, ".update-result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var result Status
			json.Unmarshal(raw, &result)
			state, _ := os.ReadFile(filepath.Join(data, "state"))
			if string(state) != "original state" {
				t.Fatal("migration data was not restored", string(state))
			}
			wantPhase := "completed"
			wantPath := filepath.Join(root, "versions", "new")
			if mode == "fail" {
				wantPhase = "rolled_back"
				wantPath = old
			}
			current, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
			if err != nil || current != wantPath || result.Phase != wantPhase {
				t.Fatal(current, result, err)
			}
			backupState, _ := os.ReadFile(filepath.Join(result.Backup, "state"))
			if string(backupState) != "original state" {
				t.Fatal("offline backup missing")
			}
			if link, err := os.Readlink(filepath.Join(data, "state-link")); err != nil || link != "state" {
				t.Fatal("backup lost symlink", err)
			}
			if info, err := os.Stat(result.Backup); err != nil || info.Mode().Perm() != 0700 {
				t.Fatal("backup permissions", err)
			}
			// Allow the short-lived fixture server to exit before the temp directory is removed.
			time.Sleep(2100 * time.Millisecond)
		})
	}
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func TestFetchTimeoutFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		io.WriteString(w, "ready")
	}))
	defer server.Close()
	err := Fetch(context.Background(), server.Client(), server.URL+"/slow", filepath.Join(t.TempDir(), "asset"), 100, 20*time.Millisecond, []string{server.URL + "/fallback/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagerChecksStagesCancelsAndRecoversPreparedUpdate(t *testing.T) {
	t.Setenv("SIAGENT_GITHUB_MIRRORS", "")
	root := t.TempDir()
	bundle := filepath.Join(t.TempDir(), "siagent-"+runtime.GOOS+"-"+runtime.GOARCH)
	for name, content := range map[string]string{"siagent": "#!/bin/sh\nexit 0\n", "libexec/node": "#!/bin/sh\nexit 0\n", "runtime/dist/index.js": "// fixture\n", "VERSION": "v1.0.1\n"} {
		path := filepath.Join(bundle, name)
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte(content), 0700)
	}
	archive := filepath.Join(t.TempDir(), filepath.Base(bundle)+".tar.xz")
	if output, err := exec.Command("tar", "-cJf", archive, "-C", filepath.Dir(bundle), filepath.Base(bundle)).CombinedOutput(); err != nil {
		t.Fatal(string(output), err)
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(data))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/VERSION"):
			fmt.Fprintln(w, "v1.0.1")
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			fmt.Fprintf(w, "%s  %s\n", checksum, filepath.Base(archive))
		case strings.HasSuffix(r.URL.Path, ".tar.xz"):
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := &Manager{root: root, releaseBase: server.URL, client: server.Client(), config: Config{Version: "v1.0.0"}, status: Status{Current: "v1.0.0", Supported: true, Phase: "idle"}}
	if err := manager.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := manager.Status(); !status.Available || status.Latest != "v1.0.1" {
		t.Fatal(status)
	}
	if err := manager.Prepare(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for manager.Status().Phase == "downloading" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if status := manager.Status(); status.Phase != "ready" {
		t.Fatal(status)
	}
	// Recover after a server restart, with an owner PID that no longer exists.
	lockOwner(root, 99999999)
	recovered := &Manager{root: root, config: Config{Version: "v1.0.0"}}
	recovered.recoverPrepared()
	if recovered.Status().Phase != "ready" || recovered.work != manager.work {
		t.Fatal("prepared update not recovered", recovered.Status())
	}
	old := filepath.Join(root, "versions", "old")
	os.MkdirAll(old, 0700)
	os.Symlink(old, filepath.Join(root, "current"))
	recovered.config.Executable = "/bin/true"
	if err := recovered.StartWorker(); err == nil || !strings.Contains(err.Error(), "未就绪") {
		t.Fatal("exited update helper was accepted", err)
	}
	if recovered.Status().Phase != "ready" {
		t.Fatal("failed helper changed active phase")
	}
	if err := recovered.Cancel(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".update-lock")); !os.IsNotExist(err) {
		t.Fatal("cancel retained update lock")
	}
	if _, err := os.Stat(manager.work); !os.IsNotExist(err) {
		t.Fatal("cancel retained stage")
	}
}
