package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
)

// A directly started process receives its boot token in the environment. A
// service-managed restart cannot pass environment variables through systemd or
// launchd, so the token is handed over in the install directory instead. The
// new process consumes it before serving /health/ready.
func bootTokenPath(root string) string { return filepath.Join(root, ".update-boot") }

func writeBootToken(root, token string) error {
	path := bootTokenPath(root)
	if err := os.WriteFile(path+".tmp", []byte(token), 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// BootToken returns the token prepared for this boot and removes it, so a later
// manual restart never reports a stale update boot.
func BootToken(root string) string {
	if root == "" {
		return ""
	}
	path := bootTokenPath(root)
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	os.Remove(path)
	return strings.TrimSpace(string(raw))
}
