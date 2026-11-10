package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Backup describes one offline data snapshot taken before an update. The
// directory name is the only index: an update writes
// `backups/update-<UTC timestamp>-<from version>`, so the list is derived from
// disk instead of a second table that could drift.
type Backup struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Created int64  `json:"created"`
	Bytes   int64  `json:"bytes"`
	Files   int    `json:"files"`
	// Current marks the snapshot the last update refers to. The UI warns before
	// removing it because it is the documented rollback point.
	Current bool `json:"current"`
}

var backupName = regexp.MustCompile(`^update-([0-9]{8}T[0-9]{6})-(\S+)$`)

// backupCreated parses the UTC stamp embedded in the directory name. File
// modification times are not reliable: a copy or restore rewrites them, so the
// name is the only stable ordering key.
func backupCreated(stamp string) int64 {
	parsed, err := time.Parse("20060102T150405", stamp)
	if err != nil {
		return 0
	}
	return parsed.UTC().UnixMilli()
}

// Backups lists snapshots newest first. A missing backups directory is not an
// error: a fresh install has none.
func (m *Manager) Backups() ([]Backup, error) {
	if m.root == "" {
		return []Backup{}, nil
	}
	entries, err := os.ReadDir(filepath.Join(m.root, "backups"))
	if os.IsNotExist(err) {
		return []Backup{}, nil
	}
	if err != nil {
		return nil, err
	}
	current := ""
	m.mu.Lock()
	if raw, err := os.ReadFile(filepath.Join(m.root, ".update-result.json")); err == nil {
		var result Status
		if json.Unmarshal(raw, &result) == nil && result.Backup != "" {
			current = filepath.Base(result.Backup)
		}
	}
	m.mu.Unlock()
	list := []Backup{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		match := backupName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		size, files, err := directoryUsage(filepath.Join(m.root, "backups", entry.Name()))
		if err != nil {
			continue
		}
		created := backupCreated(match[1])
		if created == 0 {
			created = info.ModTime().UnixMilli()
		}
		list = append(list, Backup{
			Name:    entry.Name(),
			Version: match[2],
			Created: created,
			Bytes:   size,
			Files:   files,
			Current: entry.Name() == current,
		})
	}
	sort.Slice(list, func(a, b int) bool { return list[a].Created > list[b].Created })
	return list, nil
}

// DeleteBackup removes one snapshot. The name is validated against the exact
// pattern the updater writes, so a crafted name cannot escape the directory.
func (m *Manager) DeleteBackup(name string) error {
	directory, err := m.backupDirectory(name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("删除备份失败：%w", err)
	}
	return nil
}

// DeleteBackups removes every snapshot and reports how many were deleted.
func (m *Manager) DeleteBackups() (int, error) {
	if m.root == "" {
		return 0, fmt.Errorf("此实例未配置更新检查。")
	}
	list, err := m.Backups()
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, backup := range list {
		directory, err := m.backupDirectory(backup.Name)
		if err != nil {
			continue
		}
		if err := os.RemoveAll(directory); err != nil {
			return deleted, fmt.Errorf("删除备份失败：%w", err)
		}
		deleted++
	}
	return deleted, nil
}

func (m *Manager) backupDirectory(name string) (string, error) {
	if m.root == "" {
		return "", fmt.Errorf("此实例未配置更新检查。")
	}
	if !backupName.MatchString(name) || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", fmt.Errorf("备份名称无效。")
	}
	return filepath.Join(m.root, "backups", name), nil
}

// directoryUsage walks a snapshot to report its real size. Snapshots can be
// large, so the caller lists them rarely rather than on every status poll.
func directoryUsage(root string) (int64, int, error) {
	var size int64
	files := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		size += info.Size()
		files++
		return nil
	})
	return size, files, err
}
