package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"siagent/internal/service"
)

type Plan struct {
	Root, Work, Data, Old, Target, Version, OldVersion, Listen string
	Restart                                                    string
	Args                                                       []string
	Parent                                                     int
}

func copyTree(source, destination string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("备份中存在非普通文件：%s", relative)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, err = io.Copy(output, input)
		syncErr := output.Sync()
		closeErr := output.Close()
		if err != nil {
			return err
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	})
}
func activate(root, target string) error {
	temporary := filepath.Join(root, ".current-update")
	os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(root, "current"))
}
func saveResult(root, phase, message, backup string) {
	raw, _ := json.Marshal(Status{Phase: phase, Message: message, Backup: backup})
	path := filepath.Join(root, ".update-result.json")
	if os.WriteFile(path+".tmp", raw, 0600) == nil {
		os.Rename(path+".tmp", path)
	}
}
func startApplication(executable string, args []string, boot string) (*exec.Cmd, error) {
	command := exec.Command(executable, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Env = append(os.Environ(), "SIAGENT_UPDATE_BOOT="+boot)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command, command.Start()
}
func readyURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health/ready", nil
}
func waitReady(command *exec.Cmd, listen, boot, version string) error {
	endpoint, err := readyURL(listen)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	if command != nil {
		go func() { done <- command.Wait() }()
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	terminate := func() {
		if command == nil {
			return
		}
		command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			<-done
		}
		syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if command != nil {
				syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			}
			return fmt.Errorf("新进程启动退出：%v", err)
		case <-tick.C:
			response, err := client.Get(endpoint)
			if err != nil {
				continue
			}
			var status struct {
				Boot    string `json:"update_boot"`
				Version string `json:"version"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status)
			response.Body.Close()
			if decodeErr == nil && response.StatusCode == 200 && status.Boot == boot {
				if version != "" && status.Version != version {
					terminate()
					return fmt.Errorf("新进程版本不匹配：实际 %s，预期 %s", status.Version, version)
				}
				return nil
			}
		case <-deadline.C:
			terminate()
			return fmt.Errorf("新版本未在 90 秒内就绪")
		}
	}
}

// RunWorker waits for the old server to close storage and release its exclusive
// data lock, backs up offline, then verifies a unique application boot.
func RunWorker(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if err := lockOwner(p.Root, os.Getpid()); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(p.Work, ".worker-ready"), nil, 0600); err != nil {
		return err
	}
	defer unlock(p.Root)
	defer os.Remove(filepath.Join(p.Root, ".update-prepared.json"))
	if p.Parent <= 0 {
		return fmt.Errorf("invalid parent process")
	}
	lock, err := os.OpenFile(filepath.Join(p.Data, "siagent.lock"), os.O_RDWR, 0600)
	if err != nil {
		saveResult(p.Root, "failed", "无法取得停机数据锁，请检查 update.log 并手动恢复服务。", "")
		return err
	}
	defer lock.Close()
	// A fully stopped process can remain a zombie until its launcher reaps it.
	// kill(pid, 0) still succeeds then. The app releases this lock only after all
	// workers and the database close, so it is the cross-platform shutdown barrier.
	deadline := time.Now().Add(45 * time.Second)
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if (err != syscall.EWOULDBLOCK && err != syscall.EAGAIN) || time.Now().After(deadline) {
			saveResult(p.Root, "failed", "旧服务或其他实例未释放数据锁，更新已停止，请检查 update.log。", "")
			return fmt.Errorf("waiting for exclusive data lock: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	releaseLock := func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }
	backup := filepath.Join(p.Root, "backups", "update-"+time.Now().UTC().Format("20060102T150405")+"-"+p.Version)
	if err := copyTree(p.Data, backup); err != nil {
		releaseLock()
		saveResult(p.Root, "failed", "停机备份失败，已尝试重新启动旧版本："+err.Error(), backup)
		command, startErr := startApplication(filepath.Join(p.Old, "siagent"), p.Args, "backup-failed")
		if startErr == nil {
			command.Process.Release()
		}
		return err
	}
	destination := filepath.Join(p.Root, "versions", filepath.Base(p.Target))
	err = installVersion(p.Target, destination)
	if err == nil {
		err = activate(p.Root, destination)
	}
	if err != nil {
		releaseLock()
		saveResult(p.Root, "failed", "切换版本失败，数据未迁移："+err.Error(), backup)
		command, startErr := startApplication(filepath.Join(p.Old, "siagent"), p.Args, "activation-failed")
		if startErr == nil {
			command.Process.Release()
		}
		return err
	}
	saveResult(p.Root, "restarting", "正在验证新版本就绪；请勿手动启动另一实例。", backup)
	releaseLock()
	boot := strconv.FormatInt(time.Now().UnixNano(), 36)
	var command *exec.Cmd
	if p.Restart != "" {
		err = service.RestartKind(context.Background(), p.Restart)
	} else {
		command, err = startApplication(filepath.Join(destination, "siagent"), p.Args, boot)
	}
	if err == nil {
		err = waitReady(command, p.Listen, boot, p.Version)
	}
	if err == nil {
		saveResult(p.Root, "completed", "更新成功，新版本已就绪。", backup)
		os.RemoveAll(p.Work)
		return nil
	}
	// Failed boot may have migrated the database. Restore the complete offline
	// backup together with the old code; retain failed new data for diagnosis.
	if lockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lockErr != nil {
		saveResult(p.Root, "failed", "新版本失败，但数据锁未释放；请停止实例后从备份恢复。", backup)
		return lockErr
	}
	failedData := backup + "-failed-data"
	if restoreErr := os.Rename(p.Data, failedData); restoreErr != nil {
		saveResult(p.Root, "failed", "无法保留新版本数据，请从完整备份恢复："+restoreErr.Error(), backup)
		return restoreErr
	}
	if restoreErr := copyTree(backup, p.Data); restoreErr != nil {
		saveResult(p.Root, "failed", "数据恢复失败，请停止实例并手动从备份恢复："+restoreErr.Error(), backup)
		return restoreErr
	}
	if restoreErr := activate(p.Root, p.Old); restoreErr != nil {
		saveResult(p.Root, "failed", "数据已恢复，但无法切回旧程序，请手动恢复 current 链接。", backup)
		return restoreErr
	}
	releaseLock()
	var old *exec.Cmd
	var restoreErr error
	if p.Restart != "" {
		restoreErr = service.RestartKind(context.Background(), p.Restart)
	} else {
		old, restoreErr = startApplication(filepath.Join(p.Old, "siagent"), p.Args, boot+"-rollback")
	}
	if restoreErr == nil {
		restoreErr = waitReady(old, p.Listen, boot+"-rollback", p.OldVersion)
	}
	if restoreErr != nil {
		saveResult(p.Root, "failed", "新版本启动失败，旧数据已恢复，但旧服务未能就绪，请手动启动。", backup)
		return restoreErr
	}
	saveResult(p.Root, "rolled_back", "新版本启动失败，已恢复旧版本与完整数据备份。原因："+err.Error(), backup)
	os.RemoveAll(p.Work)
	return nil
}

// A failed boot can leave the verified version in versions/. Reuse only the
// identical archive on retry; never overwrite another version directory.
func installVersion(target, destination string) error {
	if info, err := os.Lstat(destination); os.IsNotExist(err) {
		return os.Rename(target, destination)
	} else if err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("目标版本必须是普通目录")
	}
	staged, err := os.ReadFile(filepath.Join(target, ".archive-sha256"))
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(filepath.Join(destination, ".archive-sha256"))
	if err != nil || len(strings.TrimSpace(string(staged))) != 64 || string(staged) != string(existing) {
		return fmt.Errorf("目标版本目录已存在且校验不一致，请检查 versions 目录")
	}
	return nil
}
