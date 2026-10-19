package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	appconfig "siagent/internal/config"
	"siagent/internal/selfupdate"
	"siagent/internal/service"
)

type commandOptions struct {
	Command, DataDir, Listen, PublicURL, BasePath, Runtime, Node, ChannelWorker, Probe, Conversation string
	ConfigPath, ServiceAction, ServiceManager                                                        string
	Attachments                                                                                      appconfig.Attachments
	Limits                                                                                           *appconfig.Limits
	AdvancedToolsDisabled                                                                            *bool
	ProbeTool                                                                                        bool
	ServerArgs                                                                                       []string
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func parseCommand(args []string, installed installation, output io.Writer) (commandOptions, error) {
	options := commandOptions{Command: "serve"}
	file, configPath, err := appconfig.Load(env("SIAGENT_CONFIG", ""))
	if err != nil {
		return options, err
	}
	if _, err := os.Stat(configPath); err == nil {
		options.ConfigPath = configPath
	}
	options.Attachments = file.Attachments
	options.Limits = file.Limits
	options.AdvancedToolsDisabled = file.AdvancedToolsDisabled
	flags := flag.NewFlagSet("siagent", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&options.DataDir, "data-dir", env("SIAGENT_DATA_DIR", firstNonEmpty(file.DataDir, installed.DataDir)), "persistent data directory")
	flags.StringVar(&options.Listen, "listen", env("SIAGENT_LISTEN", firstNonEmpty(file.Listen, "127.0.0.1:8080")), "HTTP address")
	flags.StringVar(&options.PublicURL, "public-url", env("SIAGENT_PUBLIC_URL", file.PublicURL), "public HTTP(S) origin; HTTPS enables Secure cookies")
	flags.StringVar(&options.BasePath, "base-path", env("SIAGENT_BASE_PATH", file.BasePath), "URL path prefix, for example /siagent; empty means the site root")
	flags.StringVar(&options.Runtime, "runtime", env("SIAGENT_RUNTIME", firstNonEmpty(file.Runtime, installed.Runtime)), "runtime adapter entrypoint")
	flags.StringVar(&options.Node, "node", env("SIAGENT_NODE", firstNonEmpty(file.Node, installed.Node)), "Node.js executable")
	flags.StringVar(&options.ChannelWorker, "channel-worker", env("SIAGENT_CHANNEL_WORKER", firstNonEmpty(file.ChannelWorker, installed.ChannelWorker)), "bundled channel plugin entrypoint")
	flags.StringVar(&options.Probe, "probe", "", "run a model prompt through the private protocol, then exit")
	flags.StringVar(&options.Conversation, "conversation", "probe", "probe conversation identifier")
	flags.BoolVar(&options.ProbeTool, "probe-tool", false, "enable the harmless add tool for a probe")
	flags.Usage = func() {
		fmt.Fprintln(output, "SiAgent — Agent 服务\n\n用法：siagent [命令] [选项]\n\n命令（无需横杠）：\n  serve    启动服务；不写命令时默认启动\n  update   检查、下载、校验并自动备份、更新和重启\n  version  显示版本\n  help     显示帮助；兼容 -h、--help 和 -help\n\n示例：\n  siagent serve -listen 127.0.0.1:8080\n  siagent update\n  siagent update -data-dir /path/to/data\n\nupdate 连接本机正在运行的实例，等待现有任务结束后更新。\n未运行的安装器部署也可更新并启动；Docker、源码及服务管理器部署提供手动更新指引。\nWindows 请在 WSL 中运行，或使用安装器提供的 siagent 命令。\n\n选项（兼容原有单横杠和双横杠形式）：")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	parsedArgs := append([]string{}, args...)
	if rest := flags.Args(); len(rest) > 0 {
		options.Command = rest[0]
		commandIndex := len(args) - len(rest)
		parsedArgs = append(append([]string{}, args[:commandIndex]...), rest[1:]...)
		// Installer launchers prepend -data-dir before the command; retain those
		// parsed values while accepting options after the command as well.
		if options.Command == "service" {
			if len(rest) < 2 {
				return options, errors.New("用法：siagent service install|uninstall|start|stop|restart|status")
			}
			options.ServiceAction = rest[1]
			if err := flags.Parse(rest[2:]); err != nil {
				return options, err
			}
		} else if err := flags.Parse(rest[1:]); err != nil {
			return options, err
		}
		if len(flags.Args()) != 0 {
			return options, fmt.Errorf("不支持的参数：%s；运行 siagent help 查看帮助", strings.Join(flags.Args(), " "))
		}
	}
	switch options.Command {
	case "help":
		flags.Usage()
		return options, flag.ErrHelp
	case "serve", "service", "update", "version":
	default:
		return options, fmt.Errorf("未知命令 %q；运行 siagent help 查看帮助", options.Command)
	}
	if options.Command == "update" && options.Probe != "" {
		return options, errors.New("update 不接受 probe 选项")
	}
	options.ServerArgs = append([]string{"serve"}, parsedArgs...)
	if options.BasePath != "" {
		// Keep an environment-provided prefix across self-update restarts too.
		options.ServerArgs = append(options.ServerArgs, "-base-path", options.BasePath)
	}
	return options, nil
}

func runCLI(args []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	installed := locateInstallation(executable)
	options, err := parseCommand(args, installed, os.Stdout)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	switch options.Command {
	case "version":
		fmt.Fprintln(os.Stdout, buildVersion)
		return nil
	case "update":
		return runUpdate(options, executable)
	case "service":
		return runService(options, executable, installed)
	default:
		return runServer(options, executable, installed)
	}
}

func runUpdate(options commandOptions, executable string) error {
	syscall.Umask(0o077)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	directory, err := filepath.Abs(options.DataDir)
	if err != nil {
		return err
	}
	// Prefer the live instance's saved configuration and update loop. No HTTP
	// credentials or data-lock takeover are needed for the private Unix socket.
	if _, err := selfupdate.ControlRequest(ctx, directory, http.MethodGet, "/status"); err == nil {
		if _, err := selfupdate.ControlRequest(ctx, directory, http.MethodPost, "/update"); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "已发起自动更新；关闭此终端不会中断服务端更新。")
		return followUpdate(ctx, directory)
	}
	manager := selfupdate.New(selfupdate.Config{Context: ctx, Executable: executable, Data: directory, Listen: options.Listen, Version: buildVersion, Args: options.ServerArgs})
	if !manager.Status().Supported {
		return errors.New(manager.Status().Message)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "siagent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("服务正在运行，但无法连接本地更新入口。请使用运行与维护页面，或先停止旧版服务后执行 siagent update。")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := manager.BeginUpdate(); err != nil {
		return err
	}
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	previous := ""
	for {
		status := manager.Status()
		printUpdateStatus(status, &previous)
		switch status.Phase {
		case "ready":
			if err := manager.StartWorker(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, "下载与校验完成；后台更新进程将自动备份、更新并启动服务。日志位于安装目录 update.log。")
			return nil // The worker waits for this process and its data lock to exit.
		case "failed":
			return errors.New(status.Message)
		case "idle":
			if !status.Automatic {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func printUpdateStatus(status selfupdate.Status, previous *string) {
	text := status.Phase + ": " + status.Message
	if text != *previous {
		fmt.Fprintln(os.Stdout, text)
		*previous = text
	}
}

func followUpdate(ctx context.Context, data string) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	previous := ""
	reconnecting := true
	disconnectedAt := time.Time{}
	for {
		status, err := selfupdate.ControlRequest(ctx, data, http.MethodGet, "/status")
		if err == nil {
			disconnectedAt = time.Time{}
			printUpdateStatus(status, &previous)
			switch status.Phase {
			case "completed":
				return nil
			case "failed", "rolled_back":
				return errors.New(status.Message)
			case "idle":
				if !status.Automatic {
					return nil
				}
			}
			reconnecting = status.Automatic || status.Phase == "restarting" || status.Phase == "waiting" || status.Phase == "ready"
		} else {
			if !reconnecting {
				return fmt.Errorf("无法读取更新状态；服务端更新可能仍在继续，请查看运行与维护页面或 update.log：%w", err)
			}
			if disconnectedAt.IsZero() {
				disconnectedAt = time.Now()
				fmt.Fprintln(os.Stdout, "服务正在重启，等待重新连接…")
			}
			if time.Since(disconnectedAt) > 4*time.Minute {
				return errors.New("等待重启超时，请查看安装目录 update.log 和备份状态；请勿重复启动实例。")
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func runService(options commandOptions, executable string, installed installation) error {
	launcher := installed.Launcher
	if launcher == "" {
		launcher = executable
	}
	dataDir, err := filepath.Abs(options.DataDir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	basePath, err := normalizeBasePath(options.BasePath)
	if err != nil {
		return err
	}
	options.ServiceManager = service.Detect()
	serviceOptions := service.Options{Launcher: launcher, DataDir: dataDir, Listen: options.Listen, BasePath: basePath, ConfigPath: options.ConfigPath}
	var status service.Status
	switch options.ServiceAction {
	case "install":
		status, err = service.Install(ctx, serviceOptions)
	case "uninstall":
		status, err = service.Uninstall(ctx)
	case "start":
		status, err = service.Start(ctx)
	case "stop":
		status, err = service.Stop(ctx)
	case "restart":
		status, err = service.Restart(ctx)
	case "status":
		status, err = service.Current(ctx)
	default:
		return fmt.Errorf("未知服务操作 %q；使用 install|uninstall|start|stop|restart|status", options.ServiceAction)
	}
	if err != nil {
		return err
	}
	state := "已停止"
	if status.Running {
		state = "运行中"
	}
	enabled := "未启用开机启动"
	if status.Enabled {
		enabled = "已启用开机启动"
	}
	fmt.Fprintf(os.Stdout, "服务：%s · %s · %s", status.Manager, state, enabled)
	if status.PID > 0 {
		fmt.Fprintf(os.Stdout, " · PID %d", status.PID)
	}
	fmt.Fprintln(os.Stdout)
	if status.Detail != "" {
		fmt.Fprintln(os.Stdout, status.Detail)
	}
	return nil
}
