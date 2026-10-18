package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var validField = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// SetConfig validates values against the manifest, encrypts secrets and stores
// both. Empty secret values keep the previous secret.
func (r *Registry) SetConfig(ctx context.Context, id string, values, secrets map[string]string) (Plugin, error) {
	value, err := r.Get(id)
	if err != nil {
		return Plugin{}, err
	}
	manifest, err := readManifest(filepath.Join(r.root, value.ID, value.Version))
	if err != nil {
		return Plugin{}, err
	}
	allowed := map[string]ConfigField{}
	for _, field := range manifest.Config {
		allowed[field.Key] = field
	}
	cleanValues := map[string]string{}
	for key, item := range values {
		field, ok := allowed[key]
		if !ok || field.Secret {
			return Plugin{}, errors.New("插件包含不支持的配置字段。")
		}
		cleanValues[key] = item
	}
	cleanSecrets := map[string]string{}
	if previous := r.previousSecrets(id); len(previous) > 0 {
		for key, item := range previous {
			cleanSecrets[key] = item
		}
	}
	for key, item := range secrets {
		field, ok := allowed[key]
		if !ok || !field.Secret {
			return Plugin{}, errors.New("插件包含不支持的密钥字段。")
		}
		if item != "" {
			cleanSecrets[key] = item
		}
	}
	for _, field := range manifest.Config {
		if !field.Required {
			continue
		}
		if field.Secret && cleanSecrets[field.Key] == "" {
			return Plugin{}, errors.New("请填写必填的密钥字段。")
		}
		if !field.Secret && strings.TrimSpace(cleanValues[field.Key]) == "" {
			return Plugin{}, errors.New("请填写必填的配置字段。")
		}
	}
	rawValues, _ := json.Marshal(cleanValues)
	rawSecrets, _ := json.Marshal(cleanSecrets)
	encrypted := ""
	if len(cleanSecrets) > 0 {
		if r.encryptFn == nil {
			return Plugin{}, errors.New("平台未配置凭据加密。")
		}
		encrypted = r.encryptFn(string(rawSecrets))
	}
	if _, err := r.db.ExecContext(ctx, "UPDATE plugins SET config=?,secrets=?,updated_at=? WHERE id=?", string(rawValues), encrypted, time.Now().UnixMilli(), id); err != nil {
		return Plugin{}, err
	}
	r.log(ctx, id, "configure", "更新插件配置")
	return r.Get(id)
}

func (r *Registry) previousSecrets(id string) map[string]string {
	var encrypted string
	if err := r.db.QueryRow("SELECT secrets FROM plugins WHERE id=?", id).Scan(&encrypted); err != nil || encrypted == "" || r.decryptFn == nil {
		return nil
	}
	raw, err := r.decryptFn(encrypted)
	if err != nil {
		return nil
	}
	values := map[string]string{}
	_ = json.Unmarshal([]byte(raw), &values)
	return values
}

// CheckHealth runs the manifest health command (default "--health") with a
// short timeout and records the result.
func (r *Registry) CheckHealth(ctx context.Context, id string) (Plugin, error) {
	value, err := r.Get(id)
	if err != nil {
		return Plugin{}, err
	}
	manifest, err := readManifest(filepath.Join(r.root, value.ID, value.Version))
	if err != nil {
		return Plugin{}, err
	}
	if value.Type != "parser" {
		return value, nil
	}
	directory := filepath.Join(r.root, value.ID, value.Version)
	entry, err := safeEntry(directory, value.Entry)
	if err != nil {
		return Plugin{}, err
	}
	argument := manifest.Health
	if argument == "" {
		argument = "--health"
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(runCtx, entry, argument)
	command.Dir = directory
	command.Env = append([]string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}, r.environment(id)...)
	command.WaitDelay = time.Second
	var output limitedBuffer
	command.Stdout = &output
	command.Stderr = io.Discard
	state, message := "ready", "健康检查通过。"
	if err := command.Run(); err != nil {
		state, message = "failed", "健康检查失败，请检查插件配置与依赖。"
	}
	if _, err := r.db.ExecContext(ctx, "UPDATE plugins SET health=?,last_error=?,updated_at=? WHERE id=?", state, message, time.Now().UnixMilli(), id); err != nil {
		return Plugin{}, err
	}
	r.log(ctx, id, "health", message)
	return r.Get(id)
}

// RegisterBuiltin records a capability that ships with the binary so it appears
// in the extension list without a package directory.
func (r *Registry) RegisterBuiltin(ctx context.Context, id, name, typ, version string) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO plugins(id,version,type,name,entry,extensions,enabled,installed_at,updated_at) VALUES(?,?,?,?,'','',1,?,?)
  ON CONFLICT(id) DO UPDATE SET name=excluded.name,version=excluded.version`, id, version, typ, name, time.Now().UnixMilli(), time.Now().UnixMilli())
	return err
}

// InstallZip extracts a self-contained package archive and installs it. The
// archive must contain plugin.json at its root or inside one top-level folder.
func (r *Registry) InstallZip(ctx context.Context, reader io.Reader) (Plugin, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, 10<<20+1))
	if err != nil || len(raw) > 10<<20 {
		return Plugin{}, errors.New("扩展包不能超过 10 MiB。")
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return Plugin{}, errors.New("扩展包不是有效的 ZIP。")
	}
	manifest, prefix, err := zipManifest(archive)
	if err != nil {
		return Plugin{}, err
	}
	directory := filepath.Join(r.root, manifest.ID, manifest.Version)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return Plugin{}, err
	}
	total := 0
	files := 0
	for _, file := range archive.File {
		name := file.Name
		if prefix != "" {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		if filepath.IsAbs(name) || strings.Contains(name, "..") || strings.ContainsAny(name, "\\\x00") {
			return Plugin{}, errors.New("扩展包包含不安全路径。")
		}
		if file.Mode()&os.ModeSymlink != 0 || !file.Mode().IsRegular() {
			return Plugin{}, errors.New("扩展包只能包含普通文件。")
		}
		if file.UncompressedSize64 > 2<<20 {
			return Plugin{}, errors.New("扩展包内单个文件不能超过 2 MiB。")
		}
		files++
		total += int(file.UncompressedSize64)
		if files > 200 || total > 8<<20 {
			return Plugin{}, errors.New("扩展包文件数量或总大小超限。")
		}
		target := filepath.Join(directory, name)
		if !strings.HasPrefix(target, directory+string(os.PathSeparator)) {
			return Plugin{}, errors.New("扩展包包含不安全路径。")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return Plugin{}, err
		}
		source, err := file.Open()
		if err != nil {
			return Plugin{}, err
		}
		mode := file.Mode().Perm()
		if mode&0111 != 0 {
			mode = 0700
		} else {
			mode = 0600
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			source.Close()
			return Plugin{}, err
		}
		_, copyErr := io.Copy(output, io.LimitReader(source, 2<<20))
		output.Close()
		source.Close()
		if copyErr != nil {
			return Plugin{}, copyErr
		}
	}
	return r.Install(ctx, manifest.ID, manifest.Version)
}

// zipManifest finds plugin.json at the archive root or one level down.
func zipManifest(archive *zip.Reader) (Manifest, string, error) {
	candidates := []string{}
	for _, file := range archive.File {
		name := strings.TrimPrefix(file.Name, "./")
		if name == "plugin.json" {
			candidates = append(candidates, "")
		} else if strings.Count(name, "/") == 1 && strings.HasSuffix(name, "/plugin.json") {
			candidates = append(candidates, strings.TrimSuffix(name, "plugin.json"))
		}
	}
	sort.Strings(candidates)
	for _, prefix := range candidates {
		for _, file := range archive.File {
			name := strings.TrimPrefix(file.Name, "./")
			if name != prefix+"plugin.json" {
				continue
			}
			handle, err := file.Open()
			if err != nil {
				return Manifest{}, "", err
			}
			raw, err := io.ReadAll(io.LimitReader(handle, 64001))
			handle.Close()
			if err != nil || len(raw) > 64000 {
				return Manifest{}, "", errors.New("plugin.json 过大或无法读取。")
			}
			var manifest Manifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				return Manifest{}, "", errors.New("plugin.json 格式无效。")
			}
			if manifest.Schema != "siagent.plugin/v1" || !validID.MatchString(manifest.ID) || !validVersion.MatchString(manifest.Version) {
				return Manifest{}, "", errors.New("插件清单的 schema、标识或版本无效。")
			}
			if manifest.Type != "parser" {
				return Manifest{}, "", errors.New("暂不支持该插件类型。")
			}
			return manifest, prefix, nil
		}
	}
	return Manifest{}, "", errors.New("扩展包缺少 plugin.json。")
}
