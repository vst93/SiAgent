package platform

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	agentruntime "siagent/internal/runtime"
)

type SkillFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}
type Skill struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Description   string      `json:"description"`
	Content       string      `json:"content"`
	Compatibility string      `json:"compatibility"`
	Files         []SkillFile `json:"files"`
}
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	ReadOnly    bool            `json:"read_only"`
}
type MCPConfig struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Transport string    `json:"transport"`
	Command   string    `json:"command"`
	Args      []string  `json:"args"`
	URL       string    `json:"url"`
	Tools     []MCPTool `json:"tools"`
}
type MCPSecret struct {
	Env     map[string]string `json:"env"`
	Headers map[string]string `json:"headers"`
}
type MCPView struct {
	MCPConfig
	EnvKeys    []string `json:"env_keys"`
	HeaderKeys []string `json:"header_keys"`
}
type MCPBinding struct {
	ID    string   `json:"id"`
	Tools []string `json:"tools"`
}
type CapabilityBindings struct {
	Skills []string     `json:"skills"`
	MCPs   []MCPBinding `json:"mcps"`
}
type Capabilities struct {
	Skills []Skill     `json:"skills"`
	MCPs   []MCPConfig `json:"mcps"`
}
type RunCredentials struct {
	ModelKey     string               `json:"model_key"`
	FallbackKeys []string             `json:"fallback_keys,omitempty"`
	MCPs         map[string]MCPSecret `json:"mcps"`
	// Env holds the Agent's system environment variables. They are frozen with
	// the run so later edits cannot change an admitted task.
	Env map[string]string `json:"env,omitempty"`
}

func (s *Store) skillValues() ([]Skill, error) {
	rows, err := s.db.Query("SELECT config FROM skills WHERE archived=0 ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	skills := []Skill{}
	for rows.Next() {
		var raw string
		var skill Skill
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &skill) != nil {
			return nil, fmt.Errorf("技能数据无法读取。")
		}
		skills = append(skills, skill)
	}
	return skills, rows.Err()
}

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := s.store.skillValues()
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, skills)
}

// exportSkills streams a ZIP download. A single skill keeps SKILL.md at the
// archive root; the whole library puts every skill in its own top-level folder
// so the same archive can be uploaded again as a bundle.
func (s *Server) exportSkills(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	skills := []Skill{}
	if id != "" {
		var raw string
		var skill Skill
		if s.store.db.QueryRow("SELECT config FROM skills WHERE id=? AND archived=0", id).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &skill) != nil {
			fail(w, 404, "技能不存在。")
			return
		}
		skills = append(skills, skill)
	} else {
		var err error
		skills, err = s.store.skillValues()
		if err != nil {
			internal(w)
			return
		}
		if len(skills) == 0 {
			fail(w, 404, "技能库为空，暂无可导出的技能。")
			return
		}
	}
	bundle := id == ""
	name := skills[0].Name + ".zip"
	if bundle {
		name = "siagent-skills-" + time.Now().Format("20060102-150405") + ".zip"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Cache-Control", "no-store")
	archive := zip.NewWriter(w)
	defer archive.Close()
	modified := time.Now()
	used := map[string]int{}
	for _, skill := range skills {
		prefix := ""
		if bundle {
			folder := skill.Name
			used[folder]++
			if used[folder] > 1 {
				folder = fmt.Sprintf("%s-%d", folder, used[folder])
			}
			prefix = folder + "/"
		}
		root, err := os.OpenRoot(filepath.Join(s.store.directory, "skills", skill.ID))
		if err != nil {
			return
		}
		for _, file := range skill.Files {
			source, err := root.Open(file.Path)
			if err != nil {
				root.Close()
				return
			}
			header := &zip.FileHeader{Name: prefix + file.Path, Method: zip.Deflate, Modified: modified}
			header.SetMode(0600)
			target, err := archive.CreateHeader(header)
			if err == nil {
				_, err = io.Copy(target, source)
			}
			source.Close()
			if err != nil {
				root.Close()
				return
			}
		}
		root.Close()
	}
}
func (s *Store) importSkill(directory string) (Skill, error) {
	skill, _, err := s.importSkillWithOverwrite(directory, false)
	return skill, err
}

// installedSkillIDs indexes installed skills by declared name. The oldest
// record wins so an overwrite keeps existing Agent bindings stable.
func (s *Store) installedSkillIDs() (map[string]string, error) {
	rows, err := s.db.Query("SELECT id,config FROM skills WHERE archived=0 ORDER BY rowid")
	if err != nil {
		return nil, fmt.Errorf("无法读取已安装技能。")
	}
	defer rows.Close()
	ids := map[string]string{}
	for rows.Next() {
		var id, raw string
		var skill Skill
		if rows.Scan(&id, &raw) != nil || json.Unmarshal([]byte(raw), &skill) != nil {
			return nil, fmt.Errorf("已安装技能的数据无法读取。")
		}
		if _, exists := ids[skill.Name]; !exists {
			ids[skill.Name] = id
		}
	}
	return ids, rows.Err()
}

// importSkillWithOverwrite installs one skill directory. When overwrite is set
// and the declared name is already installed, the existing skill ID is reused
// so Agent bindings survive the replacement.
func (s *Store) importSkillWithOverwrite(directory string, overwrite bool) (Skill, bool, error) {
	skill := Skill{Files: []SkillFile{}}
	source, err := os.OpenRoot(directory)
	if err != nil {
		return skill, false, fmt.Errorf("无法打开本地技能目录，请检查路径和读取权限。")
	}
	defer source.Close()
	// Read and validate SKILL.md before copying so the target name is known.
	metadataFile, err := source.Open("SKILL.md")
	if err != nil {
		return skill, false, fmt.Errorf("技能根目录必须包含 UTF-8 SKILL.md，且不超过 64,000 字节。")
	}
	content, readErr := io.ReadAll(io.LimitReader(metadataFile, 64001))
	metadataFile.Close()
	if readErr != nil || len(content) > 64000 || !utf8.Valid(content) {
		return skill, false, fmt.Errorf("技能根目录必须包含 UTF-8 SKILL.md，且不超过 64,000 字节。")
	}
	name, description, compatibility, body, err := validateSkillContent(string(content))
	if err != nil {
		return skill, false, err
	}
	skill.Name, skill.Description, skill.Compatibility, skill.Content = name, description, compatibility, body
	installed, err := s.installedSkillIDs()
	if err != nil {
		return skill, false, err
	}
	existing, replaced := installed[name]
	if replaced && !overwrite {
		return skill, false, fmt.Errorf("已存在同名技能“%s”。如需替换，请勾选“覆盖同名技能”后重新上传。", name)
	}
	if replaced {
		skill.ID = existing
	} else {
		skill.ID = id()
	}
	skillsRoot := filepath.Join(s.directory, "skills")
	if err := os.MkdirAll(skillsRoot, 0700); err != nil {
		return skill, false, fmt.Errorf("无法创建技能目录。")
	}
	// Copy into disposable staging first so a failed import never damages an
	// installed skill, including the one being overwritten.
	staging, err := os.MkdirTemp(s.directory, ".skill-import-")
	if err != nil {
		return skill, false, fmt.Errorf("无法准备技能导入，请检查存储空间。")
	}
	defer os.RemoveAll(staging)
	// OpenRoot confines every read, including a directory changed during import.
	var total int64
	entries := 0
	err = fs.WalkDir(source.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 400 {
			return fmt.Errorf("too many entries")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed")
		}
		if path == "." {
			return nil
		}
		target := filepath.Join(staging, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		info, err := source.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("not a regular file")
		}
		if len(skill.Files) >= 200 || info.Size() > 2<<20 || total+info.Size() > 8<<20 {
			return fmt.Errorf("skill exceeds size limits")
		}
		file, err := source.Open(path)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (2<<20)+1))
		file.Close()
		if readErr != nil || len(data) > 2<<20 || total+int64(len(data)) > 8<<20 {
			return fmt.Errorf("skill exceeds size limits")
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return err
		}
		total += int64(len(data))
		skill.Files = append(skill.Files, SkillFile{path, int64(len(data))})
		return nil
	})
	if err != nil {
		return skill, false, fmt.Errorf("技能导入失败：不允许符号链接或特殊文件，最多 200 个文件、400 个目录项，单文件 2 MiB，总计 8 MiB；请检查读取权限。")
	}
	destination := filepath.Join(skillsRoot, skill.ID)
	backup := ""
	if replaced {
		backup = destination + ".old-" + filepath.Base(staging)
		if err := os.Rename(destination, backup); err != nil && !os.IsNotExist(err) {
			return skill, false, fmt.Errorf("无法替换同名技能，请检查存储状态。")
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		if backup != "" {
			os.Rename(backup, destination)
		}
		return skill, false, fmt.Errorf("无法保存技能文件，请检查存储状态。")
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(destination)
			if backup != "" {
				os.Rename(backup, destination)
			}
		}
	}()
	raw, _ := json.Marshal(skill)
	if _, err := s.db.Exec("INSERT INTO skills(id,config) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config", skill.ID, string(raw)); err != nil {
		return skill, false, fmt.Errorf("无法保存技能，请检查磁盘空间。")
	}
	committed = true
	if backup != "" {
		os.RemoveAll(backup)
	}
	return skill, replaced, nil
}
func validateSkillContent(content string) (name, description, compatibility, body string, err error) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return "", "", "", "", fmt.Errorf("SKILL.md 必须包含 YAML frontmatter，填写 name 和 description。")
	}
	front, body, found := strings.Cut(normalized[4:], "\n---\n")
	var metadata struct {
		Name          string `yaml:"name"`
		Description   string `yaml:"description"`
		Compatibility string `yaml:"compatibility"`
	}
	if !found || yaml.Unmarshal([]byte(front), &metadata) != nil || !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(metadata.Name) || len(metadata.Name) > 64 || strings.TrimSpace(metadata.Description) == "" || len(metadata.Description) > 1024 || len(metadata.Compatibility) > 2000 {
		return "", "", "", "", fmt.Errorf("技能元数据无效：name 只允许小写字母、数字和单连字符，最长 64 字符；description 必填且不超过 1024 字节。")
	}
	return metadata.Name, metadata.Description, metadata.Compatibility, body, nil
}

func (s *Server) updateSkillFile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.Content) > 128000 || !utf8.ValidString(input.Content) {
		fail(w, 400, "技能文件必须是 UTF-8 文本，且不超过 128,000 字节。")
		return
	}
	var raw string
	var skill Skill
	if s.store.db.QueryRow("SELECT config FROM skills WHERE id=? AND archived=0", r.PathValue("id")).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &skill) != nil {
		fail(w, 404, "技能不存在。")
		return
	}
	path := r.PathValue("path")
	index := slices.IndexFunc(skill.Files, func(file SkillFile) bool { return file.Path == path })
	if index < 0 {
		fail(w, 404, "技能文件不存在。")
		return
	}
	if path == "SKILL.md" {
		if len(input.Content) > 64000 {
			fail(w, 400, "SKILL.md 不能超过 64,000 字节。")
			return
		}
		name, description, compatibility, body, err := validateSkillContent(input.Content)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		skill.Name, skill.Description, skill.Compatibility, skill.Content = name, description, compatibility, body
	}
	root, err := os.OpenRoot(filepath.Join(s.store.directory, "skills", skill.ID))
	if err != nil {
		internal(w)
		return
	}
	defer root.Close()
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		internal(w)
		return
	}
	if _, err := file.WriteString(input.Content); err != nil {
		file.Close()
		internal(w)
		return
	}
	if err := file.Close(); err != nil {
		internal(w)
		return
	}
	skill.Files[index].Size = int64(len(input.Content))
	updated, _ := json.Marshal(skill)
	if _, err := s.store.db.Exec("UPDATE skills SET config=? WHERE id=? AND archived=0", string(updated), skill.ID); err != nil {
		internal(w)
		return
	}
	respond(w, 200, skill)
}

func (s *Server) skillFile(w http.ResponseWriter, r *http.Request) {
	var raw string
	var skill Skill
	if s.store.db.QueryRow("SELECT config FROM skills WHERE id=? AND archived=0", r.PathValue("id")).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &skill) != nil {
		fail(w, 404, "技能不存在。")
		return
	}
	path := r.PathValue("path")
	index := slices.IndexFunc(skill.Files, func(file SkillFile) bool { return file.Path == path })
	if index < 0 || skill.Files[index].Size > 128000 {
		fail(w, 400, "该资源不存在或超过 128 KB 预览上限。")
		return
	}
	root, err := os.OpenRoot(filepath.Join(s.store.directory, "skills", skill.ID))
	if err != nil {
		internal(w)
		return
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		internal(w)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 128001))
	if err != nil || len(data) > 128000 || !utf8.Valid(data) {
		fail(w, 400, "资源不是可预览的 UTF-8 文本。")
		return
	}
	respond(w, 200, map[string]string{"content": string(data)})
}
func (s *Store) mcp(id string) (MCPConfig, MCPSecret, int, error) {
	var config MCPConfig
	var secret MCPSecret
	var raw, encrypted string
	var version int
	err := s.db.QueryRow("SELECT config,secrets,version FROM mcps WHERE id=? AND archived=0", id).Scan(&raw, &encrypted, &version)
	if err != nil {
		return config, secret, version, err
	}
	if err = json.Unmarshal([]byte(raw), &config); err != nil {
		return config, secret, version, err
	}
	config.Version = version
	plain, err := s.decrypt(encrypted)
	if err == nil {
		err = json.Unmarshal([]byte(plain), &secret)
	}
	return config, secret, version, err
}
func mcpView(config MCPConfig, secret MCPSecret, version int) MCPView {
	config.Version = version
	view := MCPView{MCPConfig: config, EnvKeys: []string{}, HeaderKeys: []string{}}
	for key := range secret.Env {
		view.EnvKeys = append(view.EnvKeys, key)
	}
	for key := range secret.Headers {
		view.HeaderKeys = append(view.HeaderKeys, key)
	}
	slices.Sort(view.EnvKeys)
	slices.Sort(view.HeaderKeys)
	return view
}
func (s *Server) listMCPs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.store.db.Query("SELECT id FROM mcps WHERE archived=0 ORDER BY rowid")
	if err != nil {
		internal(w)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			internal(w)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internal(w)
		return
	}
	views := []MCPView{}
	for _, id := range ids {
		config, secret, version, err := s.store.mcp(id)
		if err != nil {
			internal(w)
			return
		}
		views = append(views, mcpView(config, secret, version))
	}
	respond(w, 200, views)
}
func validSecrets(secret MCPSecret) bool {
	if len(secret.Env) > 64 || len(secret.Headers) > 32 {
		return false
	}
	envKey := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerKey := regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)
	for key, value := range secret.Env {
		if !envKey.MatchString(key) || len(value) > 16000 || strings.ContainsRune(value, 0) {
			return false
		}
	}
	for key, value := range secret.Headers {
		if !headerKey.MatchString(key) || len(value) > 16000 || strings.ContainsAny(value, "\r\n\x00") {
			return false
		}
		switch strings.ToLower(key) {
		case "host", "content-length", "connection", "transfer-encoding", "mcp-session-id", "mcp-protocol-version", "content-type", "accept":
			return false
		}
	}
	return true
}
func (s *Server) saveMCP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MCPConfig
		Env          map[string]string `json:"env"`
		Headers      map[string]string `json:"headers"`
		ClearSecrets bool              `json:"clear_secrets"`
		Trusted      bool              `json:"trusted"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Args == nil {
		input.Args = []string{}
	}
	if !input.Trusted || input.Name == "" || len(input.Name) > 100 || len(input.Args) > 64 {
		fail(w, 400, "请填写名称并确认可信来源；程序参数最多 64 个。")
		return
	}
	for _, arg := range input.Args {
		if len(arg) > 8000 || strings.ContainsRune(arg, 0) {
			fail(w, 400, "程序参数无效。")
			return
		}
	}
	switch input.Transport {
	case "stdio":
		if !filepath.IsAbs(input.Command) || strings.ContainsRune(input.Command, 0) || len(input.Command) > 4096 {
			fail(w, 400, "本地 MCP 程序需填写绝对路径，不使用 Shell 命令；参数逐项传入。")
			return
		}
		input.URL = ""
	case "http":
		parsed, err := url.Parse(input.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(input.URL) > 4000 {
			fail(w, 400, "远程 MCP 地址须为 HTTP(S)，不含用户名、查询参数或片段；认证信息请填入请求头。")
			return
		}
		input.Command = ""
		input.Args = []string{}
	default:
		fail(w, 400, "请选择 stdio 或 Streamable HTTP。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	secret := MCPSecret{Env: map[string]string{}, Headers: map[string]string{}}
	version := 1
	status := 201
	input.ID = r.PathValue("id")
	if input.ID != "" {
		_, saved, prior, err := s.store.mcp(input.ID)
		if err != nil {
			fail(w, 404, "MCP 配置不存在。")
			return
		}
		version = prior + 1
		status = 200
		if !input.ClearSecrets {
			secret = saved
		}
	} else {
		input.ID = id()
	}
	if input.Env != nil {
		secret.Env = input.Env
	}
	if input.Headers != nil {
		secret.Headers = input.Headers
	}
	if input.Transport == "stdio" {
		secret.Headers = map[string]string{}
	} else {
		secret.Env = map[string]string{}
	}
	if !validSecrets(secret) {
		fail(w, 400, "环境变量或请求头无效：检查字段名、长度和换行；协议保留请求头不可覆盖。")
		return
	}
	input.MCPConfig.Version = version
	input.Tools = []MCPTool{}
	config, _ := json.Marshal(input.MCPConfig)
	encodedSecret, _ := json.Marshal(secret)
	_, err := s.store.db.Exec("INSERT INTO mcps(id,config,secrets,version) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config,secrets=excluded.secrets,version=excluded.version", input.ID, string(config), s.store.encrypt(string(encodedSecret)), version)
	if err != nil {
		internal(w)
		return
	}
	respond(w, status, mcpView(input.MCPConfig, secret, version))
}
func (s *Store) bindings(agentID string) (CapabilityBindings, error) {
	bindings := CapabilityBindings{Skills: []string{}, MCPs: []MCPBinding{}}
	var raw string
	err := s.db.QueryRow("SELECT config FROM agent_capabilities WHERE agent_id=?", agentID).Scan(&raw)
	if err == sql.ErrNoRows {
		return bindings, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &bindings)
	}
	if bindings.Skills == nil {
		bindings.Skills = []string{}
	}
	if bindings.MCPs == nil {
		bindings.MCPs = []MCPBinding{}
	}
	return bindings, err
}
func (s *Server) getCapabilities(w http.ResponseWriter, r *http.Request) {
	a, ok := s.capabilityAgent(w, r, false)
	if !ok {
		return
	}
	bindings, err := s.store.bindings(a.ID)
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, bindings)
}
func (s *Store) capabilitySnapshot(bindings CapabilityBindings) (Capabilities, map[string]MCPSecret, error) {
	snapshot := Capabilities{Skills: []Skill{}, MCPs: []MCPConfig{}}
	secrets := map[string]MCPSecret{}
	seen := map[string]bool{}
	if len(bindings.Skills) > 20 || len(bindings.MCPs) > 16 {
		return snapshot, secrets, fmt.Errorf("单个助手最多启用 20 个技能和 16 个 MCP 服务。")
	}
	for _, id := range bindings.Skills {
		var raw string
		var skill Skill
		if seen[id] || s.db.QueryRow("SELECT config FROM skills WHERE id=? AND archived=0", id).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &skill) != nil {
			return snapshot, secrets, fmt.Errorf("技能不存在、已归档或重复选择。")
		}
		seen[id] = true
		snapshot.Skills = append(snapshot.Skills, skill)
	}
	tools := 0
	for _, binding := range bindings.MCPs {
		config, secret, _, err := s.mcp(binding.ID)
		if err != nil || seen[binding.ID] || len(binding.Tools) == 0 {
			return snapshot, secrets, fmt.Errorf("MCP 配置不存在、重复选择或未指定工具。")
		}
		seen[binding.ID] = true
		selected := []MCPTool{}
		names := map[string]bool{}
		for _, name := range binding.Tools {
			index := slices.IndexFunc(config.Tools, func(tool MCPTool) bool { return tool.Name == name })
			if index < 0 || names[name] {
				return snapshot, secrets, fmt.Errorf("MCP 工具尚未通过连接测试或配置已变更，请先测试连接并重新确认授权。")
			}
			names[name] = true
			selected = append(selected, config.Tools[index])
			tools++
		}
		config.Tools = selected
		snapshot.MCPs = append(snapshot.MCPs, config)
		secrets[config.ID] = secret
	}
	if tools > 64 {
		return snapshot, secrets, fmt.Errorf("单个助手最多启用 64 个 MCP 工具。")
	}
	return snapshot, secrets, nil
}
func (s *Server) saveCapabilities(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CapabilityBindings
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Skills == nil {
		input.Skills = []string{}
	}
	if input.MCPs == nil {
		input.MCPs = []MCPBinding{}
	}
	if (len(input.Skills) > 0 || len(input.MCPs) > 0) && !input.Confirm {
		fail(w, 400, "启用前须确认可信来源和工具权限；工具可能写入或删除外部数据，取消不能撤销已有操作。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.capabilityAgent(w, r, true)
	if !ok {
		return
	}
	if _, _, err := s.store.capabilitySnapshot(input.CapabilityBindings); err != nil {
		fail(w, 400, err.Error())
		return
	}
	raw, _ := json.Marshal(input.CapabilityBindings)
	if _, err := s.store.db.Exec("INSERT INTO agent_capabilities VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET config=excluded.config", a.ID, string(raw)); err != nil {
		internal(w)
		return
	}
	respond(w, 200, input.CapabilityBindings)
}

type LinkedAgent struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

func (s *Server) linkedAgents(kind, id string) ([]LinkedAgent, error) {
	rows, err := s.store.db.Query("SELECT a.id,a.name,a.archived,c.config FROM agents a LEFT JOIN agent_capabilities c ON c.agent_id=a.id ORDER BY CASE WHEN a.id='default' THEN 0 ELSE 1 END,a.rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []LinkedAgent{}
	for rows.Next() {
		var agent LinkedAgent
		var raw sql.NullString
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.Archived, &raw); err != nil {
			return nil, err
		}
		if !raw.Valid {
			continue
		}
		var binding CapabilityBindings
		if err := json.Unmarshal([]byte(raw.String), &binding); err != nil {
			continue
		}
		linked := slices.Contains(binding.Skills, id)
		if kind == "mcps" {
			linked = slices.ContainsFunc(binding.MCPs, func(item MCPBinding) bool { return item.ID == id })
		}
		if linked {
			result = append(result, agent)
		}
	}
	return result, rows.Err()
}

func capabilityKind(r *http.Request) string {
	if kind := r.PathValue("kind"); kind != "" {
		return kind
	}
	if strings.HasPrefix(r.URL.Path, "/api/mcps/") {
		return "mcps"
	}
	return "skills"
}

func (s *Server) linkedCapabilityAgents(w http.ResponseWriter, r *http.Request) {
	kind := capabilityKind(r)
	if kind != "skills" && kind != "mcps" {
		fail(w, 404, "资源不存在。")
		return
	}
	agents, err := s.linkedAgents(kind, r.PathValue("id"))
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, agents)
}

func (s *Server) deleteCapability(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "skills" && kind != "mcps" {
		fail(w, 404, "资源不存在。")
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.store.db.Begin()
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec("DELETE FROM "+kind+" WHERE id=?", id)
	if err != nil {
		internal(w)
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		fail(w, 404, "资源不存在。")
		return
	}
	rows, err := tx.Query("SELECT agent_id,config FROM agent_capabilities")
	if err != nil {
		internal(w)
		return
	}
	type update struct {
		agent  string
		config string
	}
	updates := []update{}
	unlinked := []string{}
	for rows.Next() {
		var agentID, raw string
		if err := rows.Scan(&agentID, &raw); err != nil {
			rows.Close()
			internal(w)
			return
		}
		var binding CapabilityBindings
		if json.Unmarshal([]byte(raw), &binding) != nil {
			continue
		}
		changed := false
		if kind == "skills" && slices.Contains(binding.Skills, id) {
			binding.Skills = slices.DeleteFunc(binding.Skills, func(value string) bool { return value == id })
			changed = true
		}
		if kind == "mcps" && slices.ContainsFunc(binding.MCPs, func(item MCPBinding) bool { return item.ID == id }) {
			binding.MCPs = slices.DeleteFunc(binding.MCPs, func(item MCPBinding) bool { return item.ID == id })
			changed = true
		}
		if changed {
			encoded, _ := json.Marshal(binding)
			updates = append(updates, update{agentID, string(encoded)})
			unlinked = append(unlinked, agentID)
		}
	}
	rows.Close()
	for _, item := range updates {
		if _, err := tx.Exec("UPDATE agent_capabilities SET config=? WHERE agent_id=?", item.config, item.agent); err != nil {
			internal(w)
			return
		}
		if _, err := tx.Exec("UPDATE agents SET revision=revision+1 WHERE id=?", item.agent); err != nil {
			internal(w)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internal(w)
		return
	}
	if kind == "skills" {
		_ = os.RemoveAll(filepath.Join(s.store.directory, "skills", id))
	}
	respond(w, 200, map[string]any{"deleted": true, "unlinked_agents": unlinked})
}

func (s *Server) unlinkCapabilityAgent(w http.ResponseWriter, r *http.Request) {
	kind := capabilityKind(r)
	if kind != "skills" && kind != "mcps" {
		fail(w, 404, "资源不存在。")
		return
	}
	agentID := r.PathValue("agent")
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	if err := s.store.db.QueryRow("SELECT config FROM agent_capabilities WHERE agent_id=?", agentID).Scan(&raw); err == sql.ErrNoRows {
		respond(w, 200, map[string]bool{"unlinked": false})
		return
	} else if err != nil {
		internal(w)
		return
	}
	var binding CapabilityBindings
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		internal(w)
		return
	}
	changed := false
	if kind == "skills" {
		before := len(binding.Skills)
		binding.Skills = slices.DeleteFunc(binding.Skills, func(value string) bool { return value == r.PathValue("id") })
		changed = len(binding.Skills) != before
	} else {
		before := len(binding.MCPs)
		binding.MCPs = slices.DeleteFunc(binding.MCPs, func(item MCPBinding) bool { return item.ID == r.PathValue("id") })
		changed = len(binding.MCPs) != before
	}
	if !changed {
		respond(w, 200, map[string]bool{"unlinked": false})
		return
	}
	encoded, _ := json.Marshal(binding)
	tx, err := s.store.db.Begin()
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE agent_capabilities SET config=? WHERE agent_id=?", string(encoded), agentID); err != nil {
		internal(w)
		return
	}
	if _, err := tx.Exec("UPDATE agents SET revision=revision+1 WHERE id=?", agentID); err != nil {
		internal(w)
		return
	}
	if err := tx.Commit(); err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]bool{"unlinked": true})
}

func (s *Server) testMCP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	config, secret, version, err := s.store.mcp(r.PathValue("id"))
	if err != nil {
		s.mu.Unlock()
		fail(w, 404, "MCP 配置不存在。")
		return
	}
	if !s.ready() || s.restarting || s.stopping || len(s.pendingTests) >= 4 {
		s.mu.Unlock()
		fail(w, 503, "工具测试暂不可用，请等待对话服务就绪或已有测试结束。")
		return
	}
	requestID := "mcp-test-" + id()
	result := make(chan agentruntime.Event, 1)
	s.pendingTests[requestID] = result
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pendingTests, requestID); s.mu.Unlock() }()
	if err := s.runtime.Send(map[string]any{"v": 1, "type": "mcp_test", "request_id": requestID, "mcp": config, "mcp_secret": secret}); err != nil {
		fail(w, 503, "对话服务不可用，请稍后重试。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	select {
	case event := <-result:
		if event.Type != "mcp_test_result" {
			fail(w, 502, "MCP 连接失败，请检查程序路径、参数、依赖或地址及认证信息。")
			return
		}
		var payload struct {
			Tools []MCPTool `json:"tools"`
		}
		if json.Unmarshal(event.Data, &payload) != nil || len(payload.Tools) > 128 {
			fail(w, 502, "MCP 返回的工具列表无效。")
			return
		}
		before, _ := json.Marshal(config.Tools)
		after, _ := json.Marshal(payload.Tools)
		nextVersion := version
		if string(before) != string(after) {
			nextVersion++
		}
		config.Tools = payload.Tools
		config.Version = nextVersion
		raw, _ := json.Marshal(config)
		s.mu.Lock()
		updated, err := s.store.db.Exec("UPDATE mcps SET config=?,version=? WHERE id=? AND version=? AND archived=0", string(raw), nextVersion, config.ID, version)
		s.mu.Unlock()
		if err != nil {
			internal(w)
			return
		}
		count, _ := updated.RowsAffected()
		if count == 0 {
			fail(w, 409, "测试期间配置已修改或归档，请重新测试。")
			return
		}
		respond(w, 200, mcpView(config, secret, nextVersion))
	case <-ctx.Done():
		fail(w, 504, "MCP 测试超时或连接已断开，请检查服务状态后重试。")
	}
}
