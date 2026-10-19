package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
  "listen": "127.0.0.1:9000",
  "base_path": "/siagent",
  "attachments": {"max_image_bytes": 2097152, "max_message_count": 4},
  "limits": {"concurrent": 2, "conversation_queue": 3, "global_queue": 10, "run_timeout_seconds": 60, "tool_timeout_seconds": 20, "tool_rounds": 4},
  "knowledge": {"max_file_bytes": 52428800, "allowed_extensions": ["txt", ".PDF"]}
}`), 0600); err != nil {
		t.Fatal(err)
	}
	value, resolved, err := Load(path)
	if err != nil || resolved != path {
		t.Fatal(err)
	}
	if value.Listen != "127.0.0.1:9000" || value.BasePath != "/siagent" || value.Attachments.MaxImageBytes != 2097152 || value.Attachments.MaxMessageCount != 4 {
		t.Fatalf("unexpected config: %+v", value)
	}
	if value.Limits == nil || value.Limits.Concurrent != 2 {
		t.Fatalf("limits missing: %+v", value.Limits)
	}
	limits := value.Knowledge.Limits().Normalize()
	if limits.MaxUploadBytes != 52428800 || len(limits.Extensions) != 2 || limits.Extensions[0] != "pdf" || limits.Extensions[1] != "txt" {
		t.Fatalf("knowledge limits not normalized: %+v", limits)
	}
	// Unset fields keep the defaults instead of becoming zero.
	if limits.MaxCharacters != 1000000 || limits.MaxChunks != 5000 || limits.MaxPDFPages != 1000 {
		t.Fatalf("knowledge defaults not applied: %+v", limits)
	}
}

func TestLoadRejectsUnknownAndOutOfRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{
		`{"unknown": true}`,
		`{"attachments": {"max_file_bytes": -1}}`,
		`{"limits": {"concurrent": 99, "conversation_queue": 1, "global_queue": 1, "run_timeout_seconds": 60, "tool_timeout_seconds": 20, "tool_rounds": 4}}`,
		`{"knowledge": {"max_file_bytes": 1024}}`,
		`{"knowledge": {"allowed_extensions": ["../txt"]}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "config") && !strings.Contains(err.Error(), "limits") && !strings.Contains(err.Error(), "attachments") && !strings.Contains(err.Error(), "knowledge") {
			t.Fatalf("accepted invalid config %s: %v", body, err)
		}
	}
}

func TestMissingDefaultIsOptionalButExplicitPathFails(t *testing.T) {
	t.Setenv("SIAGENT_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	if _, _, err := Load(""); err == nil {
		t.Fatal("explicit missing config should fail")
	}
	t.Setenv("SIAGENT_CONFIG", "")
	if _, resolved, err := Load(""); err != nil || !strings.HasSuffix(resolved, filepath.Join(projectDir, "config.json")) {
		t.Fatalf("default path: %q %v", resolved, err)
	}
}

func TestDefaultsAndConfigDir(t *testing.T) {
	t.Setenv("SIAGENT_CONFIG", "")
	if runtime.GOOS == "linux" {
		t.Setenv("XDG_CONFIG_HOME", "/tmp/config-home")
		if got := DefaultPath(); got != filepath.Join("/tmp/config-home", "SiAgent", "config.json") {
			t.Fatalf("unexpected default path: %s", got)
		}
		return
	}
	if got := DefaultPath(); !strings.HasSuffix(got, filepath.Join("SiAgent", "config.json")) {
		t.Fatalf("unexpected default path: %s", got)
	}
}
