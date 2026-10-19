//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

const unitName = "siagent.service"

func detect() string {
	if _, err := exec.LookPath("systemctl"); err == nil {
		return "systemd"
	}
	return ""
}

func unitPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", unitName)
}

func quote(value string) string {
	value = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "\n", " ").Replace(value)
	return `"` + value + `"`
}

func unitText(options Options) string {
	var b strings.Builder
	b.WriteString("# Written by `siagent service install`; `siagent service uninstall` removes it.\n")
	b.WriteString("[Unit]\nDescription=SiAgent\nWants=network-online.target\nAfter=network-online.target\n\n")
	b.WriteString("[Service]\nType=simple\nExecStart=" + quote(options.Launcher) + " serve")
	if options.Listen != "" {
		b.WriteString(" -listen " + quote(options.Listen))
	}
	if options.BasePath != "" {
		b.WriteString(" -base-path " + quote(options.BasePath))
	}
	b.WriteString("\nEnvironment=" + quote("SIAGENT_SERVICE=systemd") + "\n")
	b.WriteString("Environment=" + quote("SIAGENT_DATA_DIR="+options.DataDir) + "\n")
	if options.ConfigPath != "" {
		b.WriteString("Environment=" + quote("SIAGENT_CONFIG="+options.ConfigPath) + "\n")
	}
	b.WriteString("KillMode=process\nRestart=on-failure\nRestartSec=5\nTimeoutStopSec=30\n\n")
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return b.String()
}

func install(ctx context.Context, options Options) (Status, error) {
	if err := reachable(); err != nil {
		return Status{}, err
	}
	path := unitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Status{}, err
	}
	if existing, err := os.ReadFile(path); err == nil && !strings.Contains(string(existing), "Written by `siagent service install`") {
		_ = os.WriteFile(path+".bak", existing, 0600)
	}
	if err := os.WriteFile(path, []byte(unitText(options)), 0600); err != nil {
		return Status{}, err
	}
	if _, err := systemctl("daemon-reload"); err != nil {
		return Status{}, err
	}
	if _, err := systemctl("enable", unitName); err != nil {
		return Status{}, err
	}
	if _, err := systemctl("restart", unitName); err != nil {
		return Status{}, err
	}
	return status(ctx)
}

func uninstall(ctx context.Context) (Status, error) {
	if _, err := os.Stat(unitPath()); err != nil {
		return Status{Manager: "systemd"}, nil
	}
	if err := reachable(); err != nil {
		return Status{}, err
	}
	_, _ = systemctl("disable", "--now", unitName)
	if err := os.Remove(unitPath()); err != nil && !os.IsNotExist(err) {
		return Status{}, err
	}
	_, err := systemctl("daemon-reload")
	return Status{Manager: "systemd"}, err
}

func start(ctx context.Context) (Status, error) {
	_, err := systemctl("start", unitName)
	return statusOrError(ctx, err)
}
func stop(ctx context.Context) (Status, error) {
	_, err := systemctl("stop", unitName)
	return statusOrError(ctx, err)
}
func restart(ctx context.Context) (Status, error) {
	_, err := systemctl("restart", unitName)
	return statusOrError(ctx, err)
}
func restartSystemd(ctx context.Context) error { _, err := systemctl("restart", unitName); return err }

func statusOrError(ctx context.Context, err error) (Status, error) {
	if err != nil {
		return Status{}, err
	}
	return status(ctx)
}

func status(ctx context.Context) (Status, error) {
	if err := reachable(); err != nil {
		return Status{Manager: "systemd"}, err
	}
	out, err := systemctl("show", unitName, "--property=ActiveState,SubState,MainPID,UnitFileState")
	if err != nil {
		return Status{}, err
	}
	value := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, item, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			value[key] = item
		}
	}
	result := Status{Manager: "systemd", Installed: value["UnitFileState"] != "", Enabled: value["UnitFileState"] == "enabled", Running: value["ActiveState"] == "active", Detail: value["ActiveState"] + " (" + value["SubState"] + ")"}
	if pid, err := strconv.Atoi(value["MainPID"]); err == nil && pid > 0 {
		result.PID = pid
	}
	if advice := lingerAdvice(); advice != "" && result.Enabled {
		result.Detail += "；" + advice
	}
	return result, nil
}

func reachable() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("未找到 systemctl；请使用系统自带的服务管理器，或运行 siagent serve")
	}
	if _, err := systemctl("is-system-running"); err != nil {
		message := err.Error()
		if strings.Contains(message, "bus") || strings.Contains(message, "XDG_RUNTIME_DIR") {
			return fmt.Errorf("systemctl --user 无法连接当前会话；请直接以该用户登录，或设置 XDG_RUNTIME_DIR=/run/user/%d", os.Getuid())
		}
	}
	return nil
}

func lingerAdvice() string {
	name := os.Getenv("USER")
	if current, err := user.Current(); err == nil && current.Username != "" {
		name = current.Username
	}
	if name == "" {
		return ""
	}
	out, err := exec.Command("loginctl", "show-user", name, "--property=Linger", "--value").Output()
	if err != nil || strings.TrimSpace(string(out)) != "no" {
		return ""
	}
	return "要让服务在未登录时也运行，请执行 sudo loginctl enable-linger " + name
}

func systemctl(args ...string) (string, error) {
	command := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("%s", message)
	}
	return string(output), nil
}

func restartLaunchd(context.Context) error { return fmt.Errorf("launchd is unavailable on linux") }
