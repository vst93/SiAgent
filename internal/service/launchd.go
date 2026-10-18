//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const label = "com.vst93.siagent"

func detect() string { return "launchd" }

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func logPath(options Options) string {
	if options.LogPath != "" {
		return options.LogPath
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "SiAgent", "siagent.log")
}

func plistText(options Options) string {
	escape := func(value string) string { return html.EscapeString(value) }
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by siagent service install; siagent service uninstall removes it. -->
<plist version="1.0">
<dict>
`)
	b.WriteString("\t<key>Label</key><string>" + label + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array><string>" + escape(options.Launcher) + "</string><string>serve</string>")
	if options.Listen != "" {
		b.WriteString("<string>-listen</string><string>" + escape(options.Listen) + "</string>")
	}
	if options.BasePath != "" {
		b.WriteString("<string>-base-path</string><string>" + escape(options.BasePath) + "</string>")
	}
	b.WriteString("</array>\n")
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	b.WriteString("\t\t<key>SIAGENT_SERVICE</key><string>launchd</string>\n")
	b.WriteString("\t\t<key>SIAGENT_DATA_DIR</key><string>" + escape(options.DataDir) + "</string>\n")
	b.WriteString("\t</dict>\n")
	b.WriteString("\t<key>RunAtLoad</key><true/>\n\t<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n")
	b.WriteString("\t<key>ThrottleInterval</key><integer>5</integer>\n\t<key>ProcessType</key><string>Background</string>\n")
	b.WriteString("\t<key>AbandonProcessGroup</key><true/>\n")
	b.WriteString("\t<key>StandardOutPath</key><string>" + escape(logPath(options)) + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key><string>" + escape(logPath(options)) + "</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func domain() string      { return "gui/" + strconv.Itoa(os.Getuid()) }
func serviceName() string { return domain() + "/" + label }

func install(ctx context.Context, options Options) (Status, error) {
	path := plistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Status{}, err
	}
	if existing, err := os.ReadFile(path); err == nil && !strings.Contains(string(existing), "Written by siagent service install") {
		_ = os.WriteFile(path+".bak", existing, 0600)
	}
	if err := os.WriteFile(path, []byte(plistText(options)), 0600); err != nil {
		return Status{}, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath(options)), 0700); err != nil {
		return Status{}, err
	}
	_, _ = launchctl("bootout", serviceName())
	if _, err := launchctl("bootstrap", domain(), path); err != nil {
		return Status{}, err
	}
	if _, err := launchctl("kickstart", "-k", serviceName()); err != nil {
		return Status{}, err
	}
	return status(ctx)
}

func uninstall(ctx context.Context) (Status, error) {
	_, _ = launchctl("bootout", serviceName())
	if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
		return Status{}, err
	}
	return Status{Manager: "launchd"}, nil
}

func start(ctx context.Context) (Status, error) {
	if _, err := launchctl("print", serviceName()); err != nil {
		if _, err := launchctl("bootstrap", domain(), plistPath()); err != nil {
			return Status{}, err
		}
	}
	_, err := launchctl("kickstart", serviceName())
	return statusOrError(ctx, err)
}
func stop(ctx context.Context) (Status, error) {
	_, err := launchctl("bootout", serviceName())
	return statusOrError(ctx, err)
}
func restart(ctx context.Context) (Status, error) {
	_, err := launchctl("kickstart", "-k", serviceName())
	return statusOrError(ctx, err)
}
func restartLaunchd(ctx context.Context) error {
	_, err := launchctl("kickstart", "-k", serviceName())
	return err
}
func restartSystemd(context.Context) error { return fmt.Errorf("systemd is unavailable on darwin") }

func statusOrError(ctx context.Context, err error) (Status, error) {
	if err != nil {
		return Status{}, err
	}
	return status(ctx)
}

func status(ctx context.Context) (Status, error) {
	result := Status{Manager: "launchd", Installed: true}
	out, err := launchctl("print", serviceName())
	if err != nil {
		result.Detail = "未加载"
		return result, nil
	}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch key {
		case "state":
			if result.Detail == "" {
				result.Detail = value
			}
			result.Running = value == "running"
		case "pid":
			if pid, err := strconv.Atoi(value); err == nil && pid > 0 {
				result.PID = pid
			}
		}
	}
	result.Enabled = true
	return result, nil
}

func launchctl(args ...string) (string, error) {
	command := exec.Command("launchctl", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

var _ = fmt.Sprintf
