package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func controlPath(data string) string {
	if resolved, err := filepath.EvalSymlinks(data); err == nil {
		data = resolved
	}
	base := os.TempDir()
	// macOS TMPDIR and custom installation paths can exceed Unix socket limits.
	if len(base) > 40 {
		base = "/tmp"
	}
	digest := sha256.Sum256([]byte(data))
	return filepath.Join(base, fmt.Sprintf("siagent-control-%d", os.Getuid()), fmt.Sprintf("%x.sock", digest[:12]))
}

// ListenControl exposes update commands only to the local service account. The
// caller holds the data lock, so a socket left by a previous instance is stale.
func ListenControl(ctx context.Context, data string, manager *Manager) (func(), error) {
	path := controlPath(data)
	directory := filepath.Dir(path)
	if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if err := validateControlDirectory(directory); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("更新控制路径被其他文件占用：%s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(manager.Status())
	})
	mux.HandleFunc("POST /update", func(w http.ResponseWriter, r *http.Request) {
		if err := manager.BeginUpdate(); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(manager.Status())
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	go server.Serve(listener)
	go func() { <-ctx.Done(); server.Close() }()
	return func() { server.Close() }, nil
}

// ControlRequest uses no TCP, browser session or administrator password. Unix
// socket permissions and the private data directory restrict it to its owner.
func ControlRequest(ctx context.Context, data, method, path string) (Status, error) {
	if err := validateControlDirectory(filepath.Dir(controlPath(data))); err != nil {
		return Status{}, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", controlPath(data))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, nil)
	if err != nil {
		return Status{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return Status{}, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return Status{}, fmt.Errorf("%s", raw)
	}
	var status Status
	err = json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&status)
	return status, err
}

func validateControlDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("本地更新控制目录的所有者或权限无效：%s", directory)
	}
	return nil
}
