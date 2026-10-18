package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceModeSurvivesInvocationID(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	version := filepath.Join(root, "versions", "v1.0.0-linux-amd64")
	if err := os.MkdirAll(version, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(version, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(version, "siagent")
	if err := os.WriteFile(executable, []byte("x"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".siagent-install"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INVOCATION_ID", "systemd-invocation")
	t.Setenv("SIAGENT_SERVICE", "systemd")
	manager := New(Config{Context: context.Background(), Executable: executable, Data: data, Listen: "127.0.0.1:8080", Version: "v1.0.0"})
	status := manager.Status()
	if status.Mode != "service" || !status.Supported {
		t.Fatalf("service mode not detected: %+v", status)
	}
	if status.Message == "" || status.Message[:2] == "此实例" {
		t.Fatalf("unexpected message: %s", status.Message)
	}
}

func TestServiceModeWithoutFlagStaysManaged(t *testing.T) {
	t.Setenv("INVOCATION_ID", "systemd-invocation")
	t.Setenv("SIAGENT_SERVICE", "")
	manager := New(Config{Context: context.Background(), Executable: "/tmp/siagent", Data: "/tmp/data", Listen: "127.0.0.1:8080", Version: "v1.0.0"})
	if manager.Status().Mode != "managed" {
		t.Fatalf("expected managed: %+v", manager.Status())
	}
}
