package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"siagent/internal/platform"
	agentruntime "siagent/internal/runtime"
	"siagent/internal/selfupdate"
	"siagent/web"
)

var buildVersion = "dev"

func main() {
	if len(os.Args) == 3 && os.Args[1] == "-update-worker" {
		if err := selfupdate.RunWorker(os.Args[2]); err != nil {
			slog.Error("update worker failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := runCLI(os.Args[1:]); err != nil {
		slog.Error("siagent stopped", "error", err)
		os.Exit(1)
	}
}
func runServer(options commandOptions, executable string, installed installation) error {
	// A service-managed restart cannot pass SIAGENT_UPDATE_BOOT through systemd
	// or launchd; the update worker leaves it in the install directory instead.
	if os.Getenv("SIAGENT_UPDATE_BOOT") == "" {
		if boot := selfupdate.BootToken(installed.Root); boot != "" {
			os.Setenv("SIAGENT_UPDATE_BOOT", boot)
		}
	}
	syscall.Umask(0o077)
	dataDir, listen, publicURL := &options.DataDir, &options.Listen, &options.PublicURL
	basePath, err := normalizeBasePath(options.BasePath)
	if err != nil {
		return err
	}
	script, node, channelWorker := &options.Runtime, &options.Node, &options.ChannelWorker
	probe, conversation, tool := &options.Probe, &options.Conversation, &options.ProbeTool
	if installed.Bundled {
		if err := checkBundledRuntime(*node, *script); err != nil {
			return err
		}
	}
	directory, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	for _, path := range []string{"", "runtime", "files", "workspaces"} {
		if err := os.MkdirAll(filepath.Join(directory, path), 0700); err != nil {
			return err
		}
	}
	lock, err := os.OpenFile(filepath.Join(directory, "siagent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("data directory is already in use: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	supervisor := agentruntime.New(*node, *script, directory)
	runtimeDone := make(chan error, 1)
	if *probe != "" {
		go func() { runtimeDone <- supervisor.Run(ctx) }()
		defer func() { stop(); <-runtimeDone }()
		timeout := time.NewTimer(5 * time.Minute)
		defer timeout.Stop()
		sent := false
		requestID := fmt.Sprintf("probe-%d", time.Now().UnixNano())
		for {
			select {
			case event, ok := <-supervisor.Events:
				if !ok {
					return fmt.Errorf("runtime stopped before probe finished")
				}
				if event.Type == "ready" {
					if sent {
						return fmt.Errorf("runtime restarted; probe was interrupted and will not be replayed")
					}
					sent = true
					err := supervisor.Send(map[string]any{"v": 1, "type": "start", "request_id": requestID, "run_id": requestID, "conversation_id": *conversation, "input": *probe, "probe_tool": *tool})
					if err != nil {
						return err
					}
				}
				if err := json.NewEncoder(os.Stdout).Encode(event); err != nil {
					return err
				}
				if event.Type == "run_completed" {
					return nil
				}
				if event.Type == "error" || event.Type == "run_failed" || event.Type == "run_cancelled" || event.Type == "runtime_unavailable" {
					return fmt.Errorf("probe did not complete: %s %s", event.Type, event.Code)
				}
			case <-timeout.C:
				return fmt.Errorf("probe timed out")
			case <-ctx.Done():
				return nil
			}
		}
	}

	store, err := platform.Open(directory)
	if err != nil {
		return err
	}
	defer store.Close()
	serverOptions := platform.ServerOptions{Attachments: platform.AttachmentLimits{
		MaxFileBytes:         options.Attachments.MaxFileBytes,
		MaxImageBytes:        options.Attachments.MaxImageBytes,
		MaxMessageImageBytes: options.Attachments.MaxMessageImageBytes,
		MaxExtractedRunes:    options.Attachments.MaxExtractedRunes,
		MaxMessageCount:      options.Attachments.MaxMessageCount,
		MaxMessageBytes:      options.Attachments.MaxMessageBytes,
		MaxConversationCount: options.Attachments.MaxConversationCount,
		MaxConversationBytes: options.Attachments.MaxConversationBytes,
		OrphanTTLHours:       options.Attachments.OrphanTTLHours,
	}}
	if options.Limits != nil {
		serverOptions.Limits = &platform.Limits{Concurrent: options.Limits.Concurrent, ConversationQueue: options.Limits.ConversationQueue, GlobalQueue: options.Limits.GlobalQueue, RunTimeout: options.Limits.RunTimeout, ToolTimeout: options.Limits.ToolTimeout, ToolRounds: options.Limits.ToolRounds}
	}
	serverOptions.AdvancedToolsDisabled = options.AdvancedToolsDisabled
	api, err := platform.New(store, supervisor, supervisor.Ready.Load, *publicURL, serverOptions)
	if err != nil {
		return err
	}
	manager := selfupdate.New(selfupdate.Config{Context: ctx, Executable: executable, Data: directory, Listen: *listen, Version: buildVersion, Args: options.ServerArgs})
	api.ConfigureUpdates(manager, stop)
	closeControl, err := selfupdate.ListenControl(ctx, directory, manager)
	if err != nil {
		slog.Warn("本地 update 命令不可用，请使用运行与维护页面", "error", err)
	} else {
		defer closeControl()
	}
	updatesDone := make(chan error, 1)
	go func() { updatesDone <- api.RunUpdates(ctx) }()
	if err := api.ConfigureChannelAdapters(*node, *channelWorker); err != nil {
		return err
	}
	if token := api.SetupToken(); token != "" {
		fmt.Fprintf(os.Stderr, "SiAgent 首次启动令牌（远程设置密码时使用）: %s\n", token)
	}
	go func() { runtimeDone <- supervisor.Run(ctx) }()
	schedulerDone := make(chan error, 1)
	go func() {
		err := api.RunScheduler(ctx)
		if err != nil {
			slog.Error("scheduler failed; stopping service")
			stop()
		}
		schedulerDone <- err
	}()
	knowledgeDone := make(chan error, 1)
	go func() {
		err := api.RunKnowledge(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("knowledge worker failed; stopping service")
			stop()
		}
		knowledgeDone <- err
	}()
	maintenanceDone := make(chan error, 1)
	go func() {
		err := api.RunMaintenance(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("maintenance failed; stopping service")
			stop()
		}
		maintenanceDone <- err
	}()
	channelsDone := make(chan error, 1)
	go func() {
		err := api.RunChannels(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("channel worker failed; stopping service")
			stop()
		}
		channelsDone <- err
	}()
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for event := range supervisor.Events {
			if err := api.HandleEvent(event); err != nil {
				slog.Error("cannot persist runtime state; stopping service")
				stop()
				return
			}
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"alive"}`)
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !supervisor.Ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, `{"status":"starting"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ready", "version": buildVersion, "update_boot": os.Getenv("SIAGENT_UPDATE_BOOT")})
	})
	mux.Handle("/api/", api.Handler())
	mux.Handle("/callbacks/", api.CallbackHandler())
	mux.Handle("/", web.Handler())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if basePath != "" {
			mountBasePath(basePath, mux).ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	serverDone := make(chan error, 1)
	go func() { slog.Info("SiAgent listening", "address", *listen); serverDone <- server.ListenAndServe() }()
	var result error
	select {
	case <-ctx.Done():
	case result = <-runtimeDone:
		runtimeDone = nil
	case result = <-serverDone:
	}
	stop()
	api.StopKnowledge()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		server.Close()
	}
	if runtimeDone != nil {
		<-runtimeDone
	}
	<-updatesDone
	<-eventsDone
	if err := <-schedulerDone; result == nil && err != nil {
		result = err
	}
	if err := <-knowledgeDone; result == nil && err != nil && err != context.Canceled {
		result = err
	}
	if err := <-channelsDone; result == nil && err != nil {
		result = err
	}
	if err := <-maintenanceDone; result == nil && err != nil {
		result = err
	}
	if result == http.ErrServerClosed {
		return nil
	}
	return result
}
func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
