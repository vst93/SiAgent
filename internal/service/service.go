// Package service installs and controls the per-user SiAgent service.
// Linux uses a systemd user unit; macOS uses a launchd LaunchAgent. The unit
// always starts the stable launcher, so a version switch is picked up by the
// next service restart.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Options struct {
	Launcher string
	DataDir  string
	Listen   string
	LogPath  string
}

type Status struct {
	Manager   string `json:"manager"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	PID       int    `json:"pid,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

func Detect() string { return detect() }

func Install(ctx context.Context, options Options) (Status, error) {
	if err := validate(options); err != nil {
		return Status{}, err
	}
	return install(ctx, options)
}
func Uninstall(ctx context.Context) (Status, error) { return uninstall(ctx) }
func Start(ctx context.Context) (Status, error)     { return start(ctx) }
func Stop(ctx context.Context) (Status, error)      { return stop(ctx) }
func Restart(ctx context.Context) (Status, error)   { return restart(ctx) }
func Current(ctx context.Context) (Status, error)   { return status(ctx) }

// RestartKind restarts the manager named by SIAGENT_SERVICE. The detached
// update worker uses it after switching the current version.
func RestartKind(ctx context.Context, kind string) error {
	switch kind {
	case "systemd":
		return restartSystemd(ctx)
	case "launchd":
		return restartLaunchd(ctx)
	default:
		return errors.New("unknown service manager")
	}
}

func validate(options Options) error {
	if options.Launcher == "" || !filepath.IsAbs(options.Launcher) {
		return errors.New("服务入口必须是绝对路径")
	}
	info, err := os.Lstat(options.Launcher)
	if err != nil || info.IsDir() {
		return fmt.Errorf("服务入口不可用：%s", options.Launcher)
	}
	if options.DataDir == "" || !filepath.IsAbs(options.DataDir) {
		return errors.New("数据目录必须是绝对路径")
	}
	if options.Listen == "" {
		return errors.New("监听地址不能为空")
	}
	return nil
}
