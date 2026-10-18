//go:build !linux && !darwin

package service

import (
	"context"
	"errors"
)

func detect() string { return "" }
func install(context.Context, Options) (Status, error) {
	return Status{}, errors.New("当前系统不支持用户级自动启动；请在服务管理器中运行 siagent serve")
}
func uninstall(context.Context) (Status, error) {
	return Status{}, errors.New("当前系统不支持用户级自动启动")
}
func start(context.Context) (Status, error) {
	return Status{}, errors.New("当前系统不支持用户级自动启动")
}
func stop(context.Context) (Status, error) {
	return Status{}, errors.New("当前系统不支持用户级自动启动")
}
func restart(context.Context) (Status, error) {
	return Status{}, errors.New("当前系统不支持用户级自动启动")
}
func status(context.Context) (Status, error) {
	return Status{}, errors.New("当前系统不支持用户级自动启动")
}
func restartSystemd(context.Context) error { return errors.New("systemd is unavailable") }
func restartLaunchd(context.Context) error { return errors.New("launchd is unavailable") }
