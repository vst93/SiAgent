package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBackup(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	directory := filepath.Join(root, "backups", name)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		full := filepath.Join(directory, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// A missing backups directory means a fresh install, not an error.
func TestBackupsMissingDirectory(t *testing.T) {
	m := &Manager{root: t.TempDir()}
	list, err := m.Backups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected no backups: %+v", list)
	}
}

// The list is derived from disk, newest first, and ignores unrelated entries.
func TestBackupsListsSnapshotsNewestFirst(t *testing.T) {
	root := t.TempDir()
	m := &Manager{root: root}
	writeBackup(t, root, "update-20260101T010101-v0.0.1", map[string]string{"siagent.db": "aaa"})
	writeBackup(t, root, "update-20260202T020202-v0.0.2", map[string]string{"siagent.db": "bbbbbb", "sub/extra": "c"})
	// Not a snapshot: must be ignored rather than listed or deleted.
	if err := os.MkdirAll(filepath.Join(root, "backups", "manual-keep"), 0700); err != nil {
		t.Fatal(err)
	}
	// A file at the top level is not a snapshot either.
	if err := os.WriteFile(filepath.Join(root, "backups", "notes.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	list, err := m.Backups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected two snapshots, got %+v", list)
	}
	if list[0].Version != "v0.0.2" {
		t.Fatalf("newest snapshot must come first: %+v", list)
	}
	if list[0].Files != 2 || list[0].Bytes == 0 {
		t.Fatalf("usage not reported: %+v", list[0])
	}
}

// Deleting one snapshot leaves the others and never touches unrelated entries.
func TestDeleteBackupRemovesOne(t *testing.T) {
	root := t.TempDir()
	m := &Manager{root: root}
	writeBackup(t, root, "update-20260101T010101-v0.0.1", map[string]string{"a": "1"})
	writeBackup(t, root, "update-20260202T020202-v0.0.2", map[string]string{"b": "2"})
	keep := filepath.Join(root, "backups", "manual-keep")
	if err := os.MkdirAll(keep, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteBackup("update-20260101T010101-v0.0.1"); err != nil {
		t.Fatal(err)
	}
	list, err := m.Backups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Version != "v0.0.2" {
		t.Fatalf("only the requested snapshot should be gone: %+v", list)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("unrelated directories must be left alone")
	}
}

// A crafted name must not escape the backups directory.
func TestDeleteBackupRejectsBadNames(t *testing.T) {
	m := &Manager{root: t.TempDir()}
	for _, name := range []string{"", ".", "..", "../escape", "update-2026/0101T010101-v1", "manual-keep", "update-20260101T010101"} {
		if err := m.DeleteBackup(name); err == nil {
			t.Fatalf("name %q must be rejected", name)
		}
	}
}

// Delete-all reports how many snapshots went and leaves other files behind.
func TestDeleteBackupsRemovesAll(t *testing.T) {
	root := t.TempDir()
	m := &Manager{root: root}
	writeBackup(t, root, "update-20260101T010101-v0.0.1", map[string]string{"a": "1"})
	writeBackup(t, root, "update-20260202T020202-v0.0.2", map[string]string{"b": "2"})
	keep := filepath.Join(root, "backups", "manual-keep")
	if err := os.MkdirAll(keep, 0700); err != nil {
		t.Fatal(err)
	}
	deleted, err := m.DeleteBackups()
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 deleted, got %d", deleted)
	}
	list, err := m.Backups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("no snapshots should remain: %+v", list)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("unrelated directories must survive delete-all")
	}
}

// Without an install root there is nothing to manage, and calls stay harmless.
func TestBackupsWithoutRoot(t *testing.T) {
	m := &Manager{}
	list, err := m.Backups()
	if err != nil || len(list) != 0 {
		t.Fatalf("expected an empty list: %+v %v", list, err)
	}
	if err := m.DeleteBackup("update-20260101T010101-v1"); err == nil {
		t.Fatal("deleting without a root must fail")
	}
	if _, err := m.DeleteBackups(); err == nil {
		t.Fatal("delete-all without a root must fail")
	}
}
