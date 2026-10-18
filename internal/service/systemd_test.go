//go:build linux

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitTextStartsLauncherAndKeepsUpdateWorkerAlive(t *testing.T) {
	text := unitText(Options{Launcher: "/home/u/.local/bin/siagent", DataDir: "/home/u/.local/share/siagent/data", Listen: "127.0.0.1:8080"})
	for _, want := range []string{
		`ExecStart="/home/u/.local/bin/siagent" serve -listen "127.0.0.1:8080"`,
		`Environment="SIAGENT_SERVICE=systemd"`,
		`Environment="SIAGENT_DATA_DIR=/home/u/.local/share/siagent/data"`,
		"KillMode=process",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unit missing %q:\n%s", want, text)
		}
	}
}

func TestUnitTextKeepsBasePath(t *testing.T) {
	text := unitText(Options{Launcher: "/home/u/.local/bin/siagent", DataDir: "/home/u/.local/share/siagent/data", Listen: "127.0.0.1:8080", BasePath: "/siagent"})
	if !strings.Contains(text, `-base-path "/siagent"`) {
		t.Fatalf("unit missing base path:\n%s", text)
	}
}

func TestUnitQuoteEscapesSpecifiersAndQuotes(t *testing.T) {
	got := quote(`/tmp/a"b%c`)
	if got != `"/tmp/a\"b%%c"` {
		t.Fatalf("unexpected quote: %s", got)
	}
}

func TestValidateRejectsRelativeLauncher(t *testing.T) {
	if err := validate(Options{Launcher: "siagent", DataDir: "/tmp/data", Listen: "127.0.0.1:8080"}); err == nil {
		t.Fatal("relative launcher accepted")
	}
}

func TestInstallWritesUnitAndEnablesService(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	launcher := filepath.Join(root, "bin", "siagent")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(root, "fakebin")
	if err := os.MkdirAll(fakeBin, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$*" in
  *" show "*) printf 'ActiveState=active\nSubState=running\nMainPID=123\nUnitFileState=enabled\n' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(fakeBin, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	status, err := Install(t.Context(), Options{Launcher: launcher, DataDir: filepath.Join(root, "data"), Listen: "127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || !status.Enabled || status.PID != 123 {
		t.Fatalf("unexpected status: %+v", status)
	}
	raw, err := os.ReadFile(unitPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `SIAGENT_SERVICE=systemd`) || !strings.Contains(string(raw), "KillMode=process") {
		t.Fatalf("unit not written correctly:\n%s", raw)
	}
}
