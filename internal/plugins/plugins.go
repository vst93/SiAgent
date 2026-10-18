// Package plugins owns the extension lifecycle: manifest validation, install,
// enable/disable, rollback, health and a bounded event log. Extensions are
// trusted local packages; they are not sandboxed.
package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const Schema = `
 CREATE TABLE plugins(id TEXT PRIMARY KEY,version TEXT NOT NULL,type TEXT NOT NULL,name TEXT NOT NULL,entry TEXT NOT NULL,extensions TEXT NOT NULL DEFAULT '',enabled INTEGER NOT NULL DEFAULT 0,installed_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
 CREATE TABLE plugin_events(id INTEGER PRIMARY KEY AUTOINCREMENT,plugin_id TEXT NOT NULL,kind TEXT NOT NULL,message TEXT NOT NULL,created INTEGER NOT NULL);
 CREATE INDEX plugin_events_plugin ON plugin_events(plugin_id,id DESC);
`

// Manifest is plugin.json. Only declarative capabilities are accepted; nothing
// here can grant itself permissions.
type Manifest struct {
	Schema      string        `json:"schema"`
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	Type        string        `json:"type"`
	APIVersion  string        `json:"api_version"`
	Entry       string        `json:"entry"`
	Description string        `json:"description"`
	Extensions  []string      `json:"extensions"`
	Config      []ConfigField `json:"config"`
	Health      string        `json:"health"`
	TimeoutMS   int           `json:"timeout_ms"`
}

// ConfigField declares one plugin setting. Secret values are encrypted and
// passed only through the environment at execution time.
type ConfigField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
}

// Plugin is one installed package version.
type Plugin struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Type        string            `json:"type"`
	Entry       string            `json:"entry"`
	Description string            `json:"description"`
	Extensions  []string          `json:"extensions"`
	Config      []ConfigField     `json:"config_fields"`
	Values      map[string]string `json:"values"`
	HasSecret   bool              `json:"has_secret"`
	Enabled     bool              `json:"enabled"`
	Healthy     bool              `json:"healthy"`
	State       string            `json:"state"`
	Message     string            `json:"message"`
	InstalledAt int64             `json:"installed_at"`
	UpdatedAt   int64             `json:"updated_at"`
}

// Candidate is a package version discovered on disk but not installed.
type Candidate struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	Type       string `json:"type"`
	Installed  bool   `json:"installed"`
	Current    bool   `json:"current"`
	Extensions string `json:"extensions"`
}

// Event is one lifecycle log line.
type Event struct {
	ID       int64  `json:"id"`
	PluginID string `json:"plugin_id"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Created  int64  `json:"created"`
}

type Registry struct {
	db        *sql.DB
	root      string
	types     map[string]bool
	encryptFn func(string) string
	decryptFn func(string) (string, error)
}

// SetCipher installs the platform's credential encryption. Without it, plugin
// secrets cannot be stored.
func (r *Registry) SetCipher(encrypt func(string) string, decrypt func(string) (string, error)) {
	r.encryptFn = encrypt
	r.decryptFn = decrypt
}

var validID = regexp.MustCompile(`^[a-z][a-z0-9.-]{1,63}$`)
var validVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}(-[0-9A-Za-z.-]{1,20})?$`)

func New(db *sql.DB, dataDirectory string) (*Registry, error) {
	root := filepath.Join(dataDirectory, "plugin-packages")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Registry{db: db, root: root, types: map[string]bool{"parser": true}}, nil
}

// Discover scans <root>/<id>/<version>/plugin.json and reports what is on disk.
func (r *Registry) Discover() ([]Candidate, error) {
	installed, err := r.installed()
	if err != nil {
		return nil, err
	}
	values := []Candidate{}
	ids, err := os.ReadDir(r.root)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !id.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(r.root, id.Name()))
		if err != nil {
			continue
		}
		for _, version := range versions {
			if !version.IsDir() {
				continue
			}
			manifest, err := readManifest(filepath.Join(r.root, id.Name(), version.Name()))
			if err != nil {
				continue
			}
			current, ok := installed[manifest.ID]
			values = append(values, Candidate{
				ID:         manifest.ID,
				Name:       manifest.Name,
				Version:    manifest.Version,
				Type:       manifest.Type,
				Installed:  ok,
				Current:    ok && current.Version == manifest.Version,
				Extensions: strings.Join(manifest.Extensions, " "),
			})
		}
	}
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].ID != values[j].ID {
			return values[i].ID < values[j].ID
		}
		return values[i].Version > values[j].Version
	})
	return values, nil
}

// Install registers one discovered version. A newer version replaces the
// current row, and the previous version stays on disk for rollback.
func (r *Registry) Install(ctx context.Context, id, version string) (Plugin, error) {
	if !validID.MatchString(id) || !validVersion.MatchString(version) {
		return Plugin{}, errors.New("插件标识或版本无效。")
	}
	directory := filepath.Join(r.root, id, version)
	manifest, err := readManifest(directory)
	if err != nil {
		return Plugin{}, err
	}
	if manifest.ID != id || manifest.Version != version {
		return Plugin{}, errors.New("插件清单与安装目录不一致。")
	}
	if _, err := safeEntry(directory, manifest.Entry); err != nil {
		return Plugin{}, err
	}
	// Store the relative entry so health checks and parsing keep resolving it
	// inside the package directory.
	entry := manifest.Entry
	now := time.Now().UnixMilli()
	extensions, _ := json.Marshal(manifest.Extensions)
	previous, previousErr := r.Get(id)
	enabled := 0
	if previousErr == nil && previous.Enabled {
		enabled = 1
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO plugins(id,version,type,name,entry,extensions,enabled,installed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)
  ON CONFLICT(id) DO UPDATE SET version=excluded.version,type=excluded.type,name=excluded.name,entry=excluded.entry,extensions=excluded.extensions,updated_at=excluded.updated_at`,
		id, version, manifest.Type, manifest.Name, entry, string(extensions), enabled, now, now); err != nil {
		return Plugin{}, err
	}
	r.log(ctx, id, "install", "安装版本 "+version)
	if previousErr == nil && previous.Version != version {
		r.log(ctx, id, "upgrade", "从 "+previous.Version+" 升级到 "+version)
	}
	return r.Get(id)
}

// Rollback switches to another installed version of the same plugin.
func (r *Registry) Rollback(ctx context.Context, id string) (Plugin, error) {
	current, err := r.Get(id)
	if err != nil {
		return Plugin{}, err
	}
	candidates, err := r.versions(id)
	if err != nil {
		return Plugin{}, err
	}
	previous := ""
	for _, version := range candidates {
		if version != current.Version {
			previous = version
			break
		}
	}
	if previous == "" {
		return Plugin{}, errors.New("没有可回滚的版本。")
	}
	value, err := r.Install(ctx, id, previous)
	if err != nil {
		return Plugin{}, err
	}
	r.log(ctx, id, "rollback", "回滚到 "+previous)
	return value, nil
}

func (r *Registry) SetEnabled(ctx context.Context, id string, enabled bool) (Plugin, error) {
	value, err := r.Get(id)
	if err != nil {
		return Plugin{}, err
	}
	flag := 0
	kind := "disable"
	if enabled {
		flag = 1
		kind = "enable"
	}
	if _, err := r.db.ExecContext(ctx, "UPDATE plugins SET enabled=?,updated_at=? WHERE id=?", flag, time.Now().UnixMilli(), id); err != nil {
		return Plugin{}, err
	}
	r.log(ctx, id, kind, value.Name)
	return r.Get(id)
}

func (r *Registry) Uninstall(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM plugins WHERE id=?", id); err != nil {
		return err
	}
	r.log(ctx, id, "uninstall", "保留磁盘文件以便重新安装")
	return nil
}

func (r *Registry) List() ([]Plugin, error) {
	rows, err := r.db.Query("SELECT id,version,type,name,entry,extensions,enabled,installed_at,updated_at,config,secrets FROM plugins ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Plugin{}
	for rows.Next() {
		value, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Registry) Get(id string) (Plugin, error) {
	row := r.db.QueryRow("SELECT id,version,type,name,entry,extensions,enabled,installed_at,updated_at,config,secrets FROM plugins WHERE id=?", id)
	return r.scan(row)
}

func (r *Registry) scan(row interface{ Scan(...any) error }) (Plugin, error) {
	var value Plugin
	var extensions, config, secrets string
	var enabled int
	if err := row.Scan(&value.ID, &value.Version, &value.Type, &value.Name, &value.Entry, &extensions, &enabled, &value.InstalledAt, &value.UpdatedAt, &config, &secrets); err != nil {
		return value, err
	}
	_ = json.Unmarshal([]byte(extensions), &value.Extensions)
	value.Enabled = enabled == 1
	if manifest, err := readManifest(filepath.Join(r.root, value.ID, value.Version)); err == nil {
		value.Config = manifest.Config
	}
	value.Values = map[string]string{}
	_ = json.Unmarshal([]byte(config), &value.Values)
	value.HasSecret = secrets != ""
	value.Healthy, value.State, value.Message = r.health(value)
	return value, nil
}

// health reports whether the package entry still exists on disk. Built-in
// capabilities (channels, model packs) have no local entry file.
func (r *Registry) health(value Plugin) (bool, string, string) {
	if value.Type == "channel" || value.Type == "model" {
		if !value.Enabled {
			return false, "disabled", "已安装但未启用。"
		}
		return true, "ready", "随系统分发。"
	}
	directory := filepath.Join(r.root, value.ID, value.Version)
	if _, err := safeEntry(directory, value.Entry); err != nil {
		return false, "missing", "插件文件缺失，请重新安装。"
	}
	if !value.Enabled {
		return false, "disabled", "已安装但未启用。"
	}
	return true, "ready", "已启用。"
}

func (r *Registry) Events(id string, limit int) ([]Event, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	query := "SELECT id,plugin_id,kind,message,created FROM plugin_events"
	args := []any{}
	if id != "" {
		query += " WHERE plugin_id=?"
		args = append(args, id)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Event{}
	for rows.Next() {
		var value Event
		if err := rows.Scan(&value.ID, &value.PluginID, &value.Kind, &value.Message, &value.Created); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Registry) log(ctx context.Context, id, kind, message string) {
	if len(message) > 500 {
		message = message[:500]
	}
	_, _ = r.db.ExecContext(ctx, "INSERT INTO plugin_events(plugin_id,kind,message,created) VALUES(?,?,?,?)", id, kind, message, time.Now().UnixMilli())
}

// ParserFor returns the enabled parser plugin that declares ext, if any.
func (r *Registry) ParserFor(ext string) (Plugin, bool) {
	values, err := r.List()
	if err != nil {
		return Plugin{}, false
	}
	for _, value := range values {
		if value.Type != "parser" || !value.Enabled {
			continue
		}
		for _, declared := range value.Extensions {
			if strings.EqualFold(declared, ext) {
				return value, true
			}
		}
	}
	return Plugin{}, false
}

// Parse runs the parser entry with the file path as its only argument. It never
// uses a shell, bounds output and time, and treats the exit code as failure.
func (r *Registry) Parse(ctx context.Context, value Plugin, path string) (string, error) {
	directory := filepath.Join(r.root, value.ID, value.Version)
	entry, err := safeEntry(directory, value.Entry)
	if err != nil {
		return "", err
	}
	timeout := 20 * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, entry, path)
	command.Dir = directory
	command.Env = append([]string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}, r.environment(value.ID)...)
	command.WaitDelay = 2 * time.Second
	var output limitedBuffer
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return "", errors.New("解析插件执行失败，请检查插件和文件。")
	}
	text := output.String()
	if strings.TrimSpace(text) == "" {
		return "", errors.New("解析插件没有返回文本。")
	}
	return text, nil
}

type limitedBuffer struct {
	data     strings.Builder
	exceeded bool
}

func (b *limitedBuffer) Write(chunk []byte) (int, error) {
	if b.exceeded {
		return len(chunk), nil
	}
	if b.data.Len()+len(chunk) > 4<<20 {
		b.exceeded = true
		return len(chunk), nil
	}
	b.data.Write(chunk)
	return len(chunk), nil
}

func (b *limitedBuffer) String() string {
	if b.exceeded {
		return ""
	}
	return b.data.String()
}

func readManifest(directory string) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "plugin.json"))
	if err != nil {
		return Manifest{}, errors.New("缺少 plugin.json。")
	}
	if len(raw) > 64000 {
		return Manifest{}, errors.New("plugin.json 过大。")
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, errors.New("plugin.json 格式无效。")
	}
	if manifest.Schema != "siagent.plugin/v1" || !validID.MatchString(manifest.ID) || !validVersion.MatchString(manifest.Version) {
		return Manifest{}, errors.New("插件清单的 schema、标识或版本无效。")
	}
	if manifest.Name == "" || len(manifest.Name) > 200 || manifest.APIVersion != "1" {
		return Manifest{}, errors.New("插件名称或 API 版本无效。")
	}
	if manifest.Type != "parser" {
		return Manifest{}, fmt.Errorf("暂不支持插件类型 %q。", manifest.Type)
	}
	if len(manifest.Extensions) == 0 || len(manifest.Extensions) > 16 {
		return Manifest{}, errors.New("解析插件必须声明 1–16 个扩展名。")
	}
	for _, ext := range manifest.Extensions {
		if !strings.HasPrefix(ext, ".") || len(ext) > 16 {
			return Manifest{}, errors.New("扩展名必须以点开头且不超过 16 字节。")
		}
	}
	return manifest, nil
}

// safeEntry resolves the entry inside the package and rejects escapes.
func safeEntry(directory, entry string) (string, error) {
	if entry == "" || filepath.IsAbs(entry) || strings.Contains(entry, "..") || strings.ContainsAny(entry, "\\\x00") {
		return "", errors.New("插件入口无效。")
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", errors.New("插件目录不存在。")
	}
	target := filepath.Join(root, entry)
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("插件入口必须是包内普通文件。")
	}
	if info.Mode()&0111 == 0 {
		return "", errors.New("插件入口必须可执行。")
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", errors.New("插件入口无法解析。")
	}
	if !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return "", errors.New("插件入口必须位于包内。")
	}
	return resolved, nil
}

// environment exposes configured values to the plugin. Secrets are decrypted
// only for the child process.
func (r *Registry) environment(id string) []string {
	var config, secrets string
	if err := r.db.QueryRow("SELECT config,secrets FROM plugins WHERE id=?", id).Scan(&config, &secrets); err != nil {
		return nil
	}
	values := map[string]string{}
	_ = json.Unmarshal([]byte(config), &values)
	if secrets != "" && r.decryptFn != nil {
		decrypted, err := r.decryptFn(secrets)
		if err == nil {
			_ = json.Unmarshal([]byte(decrypted), &values)
		}
	}
	out := []string{}
	for key, value := range values {
		out = append(out, "SIAGENT_PLUGIN_"+strings.ToUpper(strings.ReplaceAll(key, "-", "_"))+"="+value)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) installed() (map[string]Plugin, error) {
	values, err := r.List()
	if err != nil {
		return nil, err
	}
	index := map[string]Plugin{}
	for _, value := range values {
		index[value.ID] = value
	}
	return index, nil
}

func (r *Registry) versions(id string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(r.root, id))
	if err != nil {
		return nil, err
	}
	values := []string{}
	for _, entry := range entries {
		if entry.IsDir() && validVersion.MatchString(entry.Name()) {
			values = append(values, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(values)))
	return values, nil
}
