package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateInstallation(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "siagent")
	if err := os.WriteFile(executable, nil, 0700); err != nil {
		t.Fatal(err)
	}
	development := locateInstallation(executable)
	if development.Bundled || development.Node != "node" || development.DataDir != "data" {
		t.Fatalf("unexpected source defaults: %+v", development)
	}
	// A damaged package must still select its private runtime, never system Node.
	if err := os.Mkdir(filepath.Join(root, "libexec"), 0700); err != nil {
		t.Fatal(err)
	}
	bundled := locateInstallation(executable)
	if !bundled.Bundled || bundled.Node != filepath.Join(root, "libexec", "node") ||
		bundled.Runtime != filepath.Join(root, "runtime", "dist", "index.js") ||
		bundled.ChannelWorker != filepath.Join(root, "runtime", "dist", "channel-wecom.js") ||
		bundled.DataDir != filepath.Join(root, "data") {
		t.Fatalf("unexpected package defaults: %+v", bundled)
	}
	if err := os.WriteFile(filepath.Join(root, "NODE_VERSION"), []byte("24.22.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "libexec")); err != nil {
		t.Fatal(err)
	}
	if got := locateInstallation(executable); got != bundled {
		t.Fatalf("lost libexec directory should not fall back to system Node: %+v", got)
	}
	link := filepath.Join(t.TempDir(), "agent")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	if got := locateInstallation(link); got != bundled {
		t.Fatalf("symlink did not resolve installation: %+v", got)
	}
}

func TestCheckBundledRuntimeMissingEntrypoint(t *testing.T) {
	if err := checkBundledRuntime("unused-node", filepath.Join(t.TempDir(), "missing.js")); err == nil {
		t.Fatal("missing Runtime should fail before starting Node")
	}
}
