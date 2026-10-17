package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type preparedState struct {
	Work, Version string
	Ready         bool
}

func writePrepared(root, work, version string, ready bool) error {
	raw, _ := json.Marshal(preparedState{work, version, ready})
	return os.WriteFile(filepath.Join(root, ".update-prepared.json"), raw, 0600)
}
func lockOwner(root string, pid int) error {
	return os.WriteFile(filepath.Join(root, ".update-lock", "owner"), []byte(strconv.Itoa(pid)), 0600)
}
func unlock(root string) {
	marker := filepath.Join(root, ".install-lock", ".application-update")
	if _, err := os.Stat(marker); err == nil {
		os.Remove(marker)
		os.Remove(filepath.Join(root, ".install-lock"))
	}
	os.Remove(filepath.Join(root, ".update-lock", "owner"))
	os.Remove(filepath.Join(root, ".update-lock"))
}
func (m *Manager) recoverPrepared() {
	raw, err := os.ReadFile(filepath.Join(m.root, ".update-lock", "owner"))
	if err == nil {
		pid, err := strconv.Atoi(string(raw))
		if err == nil && pid > 0 && syscall.Kill(pid, 0) == nil {
			return
		}
	}
	var saved preparedState
	raw, err = os.ReadFile(filepath.Join(m.root, ".update-prepared.json"))
	if err != nil {
		unlock(m.root)
		return
	}
	if json.Unmarshal(raw, &saved) != nil || filepath.Dir(saved.Work) != m.root || !strings.HasPrefix(filepath.Base(saved.Work), ".update-") || !versionPattern.MatchString(saved.Version) {
		return
	}
	if saved.Ready {
		os.Mkdir(filepath.Join(m.root, ".install-lock"), 0700)
		os.WriteFile(filepath.Join(m.root, ".install-lock", ".application-update"), nil, 0600)
		m.work = saved.Work
		m.status.Latest = saved.Version
		m.status.Available = newer(saved.Version, m.config.Version)
		m.status.Phase = "ready"
		m.status.Message = "已恢复上次准备好的更新；可继续安装或取消。"
		os.Mkdir(filepath.Join(m.root, ".update-lock"), 0700)
		lockOwner(m.root, os.Getpid())
		return
	}
	os.RemoveAll(saved.Work)
	os.Remove(filepath.Join(m.root, ".update-prepared.json"))
	unlock(m.root)
}
