package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
  "listen": "127.0.0.1:9000",
  "base_path": "/siagent",
  "attachments": {"max_image_bytes": 2097152, "max_message_count": 4},
  "limits": {"concurrent": 2, "conversation_queue": 3, "global_queue": 10, "run_timeout_seconds": 60, "tool_timeout_seconds": 20, "tool_rounds": 4}
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
}

func TestLoadRejectsUnknownAndOutOfRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{
		`{"unknown": true}`,
		`{"attachments": {"max_file_bytes": -1}}`,
		`{"limits": {"concurrent": 99, "conversation_queue": 1, "global_queue": 1, "run_timeout_seconds": 60, "tool_timeout_seconds": 20, "tool_rounds": 4}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "config") && !strings.Contains(err.Error(), "limits") && !strings.Contains(err.Error(), "attachments") {
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
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	if _, resolved, err := Load(""); err != nil || resolved != filepath.Join(base, projectDir, "config.json") {
		t.Fatalf("default path: %q %v", resolved, err)
	}
}

func TestDefaultsAndConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/config-home")
	t.Setenv("SIAGENT_CONFIG", "")
	if got := DefaultPath(); got != filepath.Join("/tmp/config-home", "SiAgent", "config.json") {
		t.Fatalf("unexpected default path: %s", got)
	}
}
