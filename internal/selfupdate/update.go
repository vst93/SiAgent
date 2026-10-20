package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"siagent/scripts"
)

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)

type Status struct {
	Mode      string `json:"mode"`
	Supported bool   `json:"supported"`
	Automatic bool   `json:"automatic"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Phase     string `json:"phase"`
	Message   string `json:"message"`
	Backup    string `json:"backup,omitempty"`
	Notes     string `json:"notes,omitempty"`
	// Downloaded and DownloadTotal describe the release archive transfer.
	// Total is 0 when the server does not publish a content length, in which
	// case the UI shows an indeterminate bar.
	Downloaded    int64 `json:"downloaded,omitempty"`
	DownloadTotal int64 `json:"download_total,omitempty"`
}
type Config struct {
	Context                           context.Context
	Executable, Data, Listen, Version string
	Service                           string
	Args                              []string
}
type Manager struct {
	mu          sync.Mutex
	config      Config
	root        string
	work        string
	status      Status
	client      *http.Client
	releaseBase string
	repository  string
}

func New(config Config) *Manager {
	serviceKind := strings.TrimSpace(os.Getenv("SIAGENT_SERVICE"))
	config.Service = serviceKind
	m := &Manager{config: config, client: &http.Client{}, releaseBase: ReleaseBase, repository: "vst93/SiAgent", status: Status{Current: config.Version, Mode: "source", Phase: "idle", Message: "源码部署请拉取代码、重新构建并重启。"}}
	if containerEnvironment() {
		m.status.Mode = "docker"
		m.status.Message = "Docker 请拉取新镜像并重建容器，保留原数据卷。Compose：docker compose pull && docker compose up -d；本地构建：docker compose up -d --build。容器内更新会在重建时丢失，因此不提供一键替换。"
		return m
	}
	if serviceKind == "" && (os.Getenv("INVOCATION_ID") != "" || os.Getenv("NOTIFY_SOCKET") != "" || os.Getenv("SUPERVISOR_ENABLED") != "" || os.Getenv("SIAGENT_DEPLOYMENT") == "managed") {
		m.status.Mode = "managed"
		m.status.Message = "此实例由外部服务管理器托管。请先停止服务，更新安装包，再由原服务管理器启动；应用内不另起进程。"
		return m
	}
	data, err := resolveDataPath(config.Data)
	if err != nil {
		return m
	}
	config.Data = data
	m.config.Data = data
	executable, err := filepath.EvalSymlinks(config.Executable)
	if err != nil {
		return m
	}
	versionDir := filepath.Dir(executable)
	root := filepath.Dir(filepath.Dir(versionDir))
	if filepath.Base(filepath.Dir(versionDir)) != "versions" {
		return m
	}
	if _, err := os.Stat(filepath.Join(root, ".siagent-install")); err != nil {
		return m
	}
	current, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil || current != versionDir {
		return m
	}
	versions, backups := filepath.Join(root, "versions"), filepath.Join(root, "backups")
	if config.Data == root || strings.HasPrefix(root, config.Data+string(os.PathSeparator)) || config.Data == versions || strings.HasPrefix(config.Data, versions+string(os.PathSeparator)) || config.Data == backups || strings.HasPrefix(config.Data, backups+string(os.PathSeparator)) {
		m.status.Message = "数据目录不能位于版本或备份目录内，也不能包含安装目录。请迁移到独立数据目录。"
		return m
	}
	if _, port, err := net.SplitHostPort(config.Listen); err != nil || port == "0" {
		m.status.Message = "应用内更新需要固定的监听端口。"
		return m
	}
	m.root = root
	if raw, err := os.ReadFile(filepath.Join(root, ".siagent-repository")); err == nil {
		repository := strings.TrimSpace(string(raw))
		if regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repository) {
			m.repository = repository
			m.releaseBase = "https://github.com/" + repository + "/releases"
		}
	}
	if serviceKind != "" {
		m.status.Mode = "service"
		m.status.Supported = true
		m.status.Message = "由 " + serviceKind + " 托管，支持应用内更新并重启服务；更新前须等待所有任务和渠道投递结束。"
	} else {
		m.status.Mode = "installer"
		m.status.Supported = true
		m.status.Message = "支持下载更新、停机备份及自动重启；更新前须等待所有任务和渠道投递结束。"
	}
	if raw, err := os.ReadFile(filepath.Join(root, ".update-result.json")); err == nil {
		var result Status
		if json.Unmarshal(raw, &result) == nil {
			m.status.Phase = result.Phase
			m.status.Message = result.Message
			m.status.Backup = result.Backup
		}
	}
	m.recoverPrepared()
	return m
}
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Phase == "restarting" {
		if raw, err := os.ReadFile(filepath.Join(m.root, ".update-result.json")); err == nil {
			var result Status
			if json.Unmarshal(raw, &result) == nil {
				m.status.Phase = result.Phase
				m.status.Message = result.Message
				m.status.Backup = result.Backup
			}
		}
	}
	return m.status
}
func (m *Manager) BeginCheck() error {
	m.mu.Lock()
	if m.status.Automatic || m.status.Phase == "downloading" || m.status.Phase == "restarting" || m.status.Phase == "checking" || m.status.Phase == "ready" || m.status.Phase == "waiting" {
		m.mu.Unlock()
		return fmt.Errorf("更新操作正在进行")
	}
	m.status.Phase = "checking"
	m.status.Message = "正在检查新版本。"
	m.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(m.context(), 2*time.Minute)
		defer cancel()
		m.check(ctx)
	}()
	return nil
}
func (m *Manager) message(value string) { m.mu.Lock(); m.status.Message = value; m.mu.Unlock() }
func (m *Manager) check(ctx context.Context) error {
	folder, err := os.MkdirTemp("", "siagent-version-")
	if err != nil {
		return m.failed(err)
	}
	defer os.RemoveAll(folder)
	file := filepath.Join(folder, "VERSION")
	err = Fetch(ctx, m.client, m.releaseBase+"/latest/download/VERSION", file, 1024, 12*time.Second, Mirrors(), m.message)
	if err != nil {
		return m.failed(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return m.failed(err)
	}
	latest := strings.TrimSpace(string(data))
	if !versionPattern.MatchString(latest) {
		return m.failed(fmt.Errorf("发布版本信息无效"))
	}
	notes := m.fetchReleaseNotes(ctx, latest)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Latest = latest
	m.status.Notes = notes
	m.status.Available = newer(latest, m.config.Version)
	m.status.Phase = "idle"
	if m.status.Available {
		m.status.Message = "发现新版本，可更新并自动重启。"
	} else {
		m.status.Automatic = false
		m.status.Message = "当前已是最新版本。"
	}
	if !m.status.Supported {
		m.status.Message += " 当前部署请使用原安装或部署方式手动更新。"
	}
	return nil
}
func (m *Manager) fetchReleaseNotes(ctx context.Context, version string) string {
	if m.repository == "" {
		return ""
	}
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	endpoint := "https://api.github.com/repos/" + m.repository + "/releases/tags/" + url.PathEscape(version)
	request, err := http.NewRequestWithContext(timeout, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "SiAgent/"+m.config.Version)
	response, err := m.client.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ""
	}
	var release struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&release); err != nil {
		return ""
	}
	return strings.TrimSpace(release.Body)
}

func newer(latest, current string) bool {
	if !versionPattern.MatchString(current) {
		return latest != current
	}
	var a, b [3]int
	fmt.Sscanf(latest, "v%d.%d.%d", &a[0], &a[1], &a[2])
	fmt.Sscanf(current, "v%d.%d.%d", &b[0], &b[1], &b[2])
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return strings.Contains(current, "-") && !strings.Contains(latest, "-")
}
func (m *Manager) failed(err error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Phase = "failed"
	m.status.Automatic = false
	m.status.Message = err.Error()
	return err
}
func (m *Manager) Prepare() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.status.Supported {
		return fmt.Errorf("当前部署方式不支持应用内更新")
	}
	if !m.status.Available || m.status.Latest == "" {
		return fmt.Errorf("请先检查可用的新版本")
	}
	if m.status.Phase == "checking" || m.status.Phase == "downloading" || m.status.Phase == "ready" || m.status.Phase == "waiting" || m.status.Phase == "restarting" {
		return fmt.Errorf("更新已在进行或准备完成")
	}
	if err := os.Mkdir(filepath.Join(m.root, ".update-lock"), 0700); err != nil {
		return fmt.Errorf("另一个更新正在进行，或上次更新锁未清理，请检查安装目录")
	}
	if err := os.Mkdir(filepath.Join(m.root, ".install-lock"), 0700); err != nil {
		unlock(m.root)
		return fmt.Errorf("安装脚本正在运行，请稍后重试")
	}
	if err := os.WriteFile(filepath.Join(m.root, ".install-lock", ".application-update"), nil, 0600); err != nil {
		os.Remove(filepath.Join(m.root, ".install-lock"))
		unlock(m.root)
		return err
	}
	if err := lockOwner(m.root, os.Getpid()); err != nil {
		unlock(m.root)
		return err
	}
	latest := m.status.Latest
	automatic := m.status.Automatic
	m.status.Phase = "downloading"
	m.status.Message = "正在准备更新。"
	go func() {
		ctx, cancel := context.WithTimeout(m.context(), 15*time.Minute)
		defer cancel()
		work, err := os.MkdirTemp(m.root, ".update-")
		if err != nil {
			unlock(m.root)
			m.failed(err)
			return
		}
		if err := writePrepared(m.root, work, latest, false, automatic); err != nil {
			os.RemoveAll(work)
			unlock(m.root)
			m.failed(err)
			return
		}
		success := false
		defer func() {
			if !success {
				os.RemoveAll(work)
				os.Remove(filepath.Join(m.root, ".update-prepared.json"))
				unlock(m.root)
			}
		}()
		installer := filepath.Join(work, "install.sh")
		if err := os.WriteFile(installer, []byte(scripts.Installer), 0600); err != nil {
			m.failed(err)
			return
		}
		m.message("正在下载并校验发布包，完成前应用继续运行。")
		command := exec.CommandContext(ctx, "sh", append([]string{installer}, stagedInstallerArgs(latest, work)...)...)
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
		command.WaitDelay = time.Second
		command.Env = append(os.Environ(), "SIAGENT_RELEASE_BASE_URL="+m.releaseBase)
		writer := &downloadProgress{}
		command.Stdout = writer
		command.Stderr = writer
		// The installer prints transfer progress on stdout; publish it so the
		// maintenance page can show a bounded progress bar while it runs.
		pollDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(400 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-pollDone:
					return
				case <-ticker.C:
					current, total := writer.transfer()
					if current == 0 && total == 0 {
						continue
					}
					m.mu.Lock()
					if m.status.Phase == "downloading" {
						m.status.Downloaded = current
						m.status.DownloadTotal = total
					}
					m.mu.Unlock()
				}
			}
		}()
		err = command.Run()
		close(pollDone)
		output := writer.log()
		if err != nil {
			m.failed(fmt.Errorf("下载或校验失败：%s", bounded(output, 3000)))
			return
		}
		if err := writePrepared(m.root, work, latest, true, m.Status().Automatic); err != nil {
			m.failed(err)
			return
		}
		success = true
		m.mu.Lock()
		m.work = work
		m.status.Phase = "ready"
		m.status.Downloaded = 0
		m.status.DownloadTotal = 0
		m.status.Message = "发布包校验通过，等待备份并自动重启。"
		m.mu.Unlock()
	}()
	return nil
}

// stagedInstallerArgs installs a release into the private staging area only.
// The installer must never register or restart a service: its prefix is a
// temporary .update-* directory, so a service registered there would start a
// fresh instance from a path that is deleted as soon as the update finishes.
func stagedInstallerArgs(version, work string) []string {
	return []string{
		"--version", version,
		"--prefix", filepath.Join(work, "stage"),
		"--bin-dir", filepath.Join(work, "bin"),
		"--no-modify-path",
		"--no-service",
	}
}

func bounded(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}
func (m *Manager) Cancel() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Phase != "ready" && m.status.Phase != "waiting" {
		return fmt.Errorf("仅可取消已准备好或等待空闲的更新")
	}
	if err := os.RemoveAll(m.work); err != nil {
		return err
	}
	unlock(m.root)
	os.Remove(filepath.Join(m.root, ".update-prepared.json"))
	m.work = ""
	m.status.Phase = "idle"
	m.status.Automatic = false
	m.status.Message = "已取消准备的更新。"
	return nil
}
func (m *Manager) StartWorker() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Phase != "ready" && m.status.Phase != "waiting" {
		return fmt.Errorf("更新包尚未准备完成")
	}
	target := filepath.Join(m.work, "stage", "versions", m.status.Latest+"-"+runtime.GOOS+"-"+runtime.GOARCH)
	old, err := filepath.EvalSymlinks(filepath.Join(m.root, "current"))
	if err != nil {
		return err
	}
	plan := Plan{Root: m.root, Work: m.work, Data: m.config.Data, Old: old, Target: target, Version: m.status.Latest, OldVersion: m.config.Version, Listen: m.config.Listen, Restart: m.config.Service, Args: m.config.Args, Parent: os.Getpid()}
	path := filepath.Join(m.work, "plan.json")
	raw, _ := json.Marshal(plan)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return err
	}
	marker := filepath.Join(m.work, ".worker-ready")
	os.Remove(marker)
	command := exec.Command(m.config.Executable, "-update-worker", path)
	command.Stdin = nil
	log, err := os.OpenFile(filepath.Join(m.root, "update.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			command.Process.Kill()
			command.Wait()
			lockOwner(m.root, os.Getpid())
			return fmt.Errorf("更新进程未就绪，尚未停止当前服务；请查看 update.log 后重试")
		}
		time.Sleep(25 * time.Millisecond)
	}
	command.Process.Release()
	saveResult(m.root, "restarting", "正在停机并创建完整备份。", "")
	m.status.Phase = "restarting"
	m.status.Message = "正在停机、备份和重启，请等待自动重新连接。"
	return nil
}

// downloadProgress keeps the bounded installer log and extracts the release
// archive transfer from the progress lines the installer prints. The installer
// writes from an os/exec pipe goroutine while the poller reads, so the buffer
// is guarded.
type downloadProgress struct {
	mu   sync.Mutex
	tail string
}

func (w *downloadProgress) Write(value []byte) (int, error) {
	w.mu.Lock()
	w.tail = bounded(w.tail+string(value), 3000)
	w.mu.Unlock()
	return len(value), nil
}

// log returns the retained output, used for failure messages.
func (w *downloadProgress) log() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tail
}

// progressLine matches "progress: <downloaded> <total>" printed by the
// installer while it downloads the release archive.
var progressLine = regexp.MustCompile(`(?m)^progress: (\d+) (\d+)$`)

func (w *downloadProgress) transfer() (int64, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	matches := progressLine.FindAllStringSubmatch(w.tail, -1)
	if len(matches) == 0 {
		return 0, 0
	}
	last := matches[len(matches)-1]
	current, err := strconv.ParseInt(last[1], 10, 64)
	if err != nil || current < 0 {
		return 0, 0
	}
	total, err := strconv.ParseInt(last[2], 10, 64)
	if err != nil || total < 0 {
		total = 0
	}
	return current, total
}

// BeginUpdate records the user's intent once. Download completion is consumed by
// the host's update loop, independently of browser polling or connectivity.
func (m *Manager) BeginUpdate() error {
	m.mu.Lock()
	if !m.status.Supported {
		message := m.status.Message
		m.mu.Unlock()
		return fmt.Errorf("%s", message)
	}
	if m.status.Automatic {
		m.mu.Unlock()
		return fmt.Errorf("更新已在进行")
	}
	switch m.status.Phase {
	case "ready":
		if m.status.Automatic {
			m.mu.Unlock()
			return fmt.Errorf("更新已在进行")
		}
		if err := writePrepared(m.root, m.work, m.status.Latest, true, true); err != nil {
			m.mu.Unlock()
			return err
		}
		m.status.Automatic = true
		m.mu.Unlock()
		return nil
	case "checking", "downloading", "waiting", "restarting":
		m.mu.Unlock()
		return fmt.Errorf("更新已在进行")
	}
	m.status.Automatic = true
	m.status.Phase = "checking"
	m.status.Message = "正在检查新版本，发现更新后将自动下载并重启。"
	m.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(m.context(), 2*time.Minute)
		defer cancel()
		if err := m.check(ctx); err != nil {
			return
		}
		if !m.Status().Available {
			return
		}
		if err := m.Prepare(); err != nil {
			m.failed(err)
		}
	}()
	return nil
}

func (m *Manager) WaitForIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Automatic && (m.status.Phase == "ready" || m.status.Phase == "waiting") {
		m.status.Phase = "waiting"
		m.status.Message = "正在等待现有任务、渠道投递和后台处理结束；已暂停接收新任务，完成后自动备份并重启。"
	}
}

func containerEnvironment() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return os.Getenv("SIAGENT_DEPLOYMENT") == "docker" || os.Getenv("container") != ""
}

// Fail stops an automatic apply attempt while retaining diagnostics and staged files.
func (m *Manager) Fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Automatic = false
	if m.work != "" {
		m.status.Phase = "ready"
		m.status.Message = "自动重启未能开始，可重试或取消更新：" + err.Error()
		writePrepared(m.root, m.work, m.status.Latest, true, false)
	} else {
		m.status.Phase = "failed"
		m.status.Message = err.Error()
	}
}

func (m *Manager) context() context.Context {
	if m.config.Context != nil {
		return m.config.Context
	}
	return context.Background()
}
