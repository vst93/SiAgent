package platform

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Creating a Skill by hand is the same operation as importing one, minus the
// upload: validate the SKILL.md the editor produced, write the same directory
// layout, and register the same record. It deliberately reuses
// validateSkillContent so a hand-written Skill cannot be more permissive than an
// uploaded one.
func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content string `json:"content"`
		Mount   string `json:"mount"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !utf8.ValidString(input.Content) || len(input.Content) > 64000 {
		fail(w, 400, "SKILL.md 必须是 UTF-8 文本，且不超过 64,000 字节。")
		return
	}
	name, description, compatibility, body, err := validateSkillContent(input.Content)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	installed, err := s.store.installedSkillIDs()
	if err != nil {
		internal(w)
		return
	}
	if _, exists := installed[name]; exists {
		fail(w, 409, fmt.Sprintf("已存在同名技能“%s”。请改用其他 name，或在技能详情中编辑它。", name))
		return
	}
	skill := Skill{
		ID:            id(),
		Name:          name,
		Description:   description,
		Compatibility: compatibility,
		Content:       body,
		Files:         []SkillFile{},
	}
	root := filepath.Join(s.store.directory, "skills")
	if err := os.MkdirAll(root, 0700); err != nil {
		fail(w, 500, "无法创建技能目录。")
		return
	}
	directory := filepath.Join(root, skill.ID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		fail(w, 500, "无法创建技能目录。")
		return
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(directory)
		}
	}()
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(input.Content), 0600); err != nil {
		fail(w, 500, "无法保存 SKILL.md，请检查存储空间。")
		return
	}
	skill.Files = append(skill.Files, SkillFile{Path: "SKILL.md", Size: int64(len(input.Content))})
	raw, err := json.Marshal(skill)
	if err != nil {
		internal(w)
		return
	}
	if _, err := s.store.db.Exec("INSERT INTO skills(id,config) VALUES(?,?)", skill.ID, string(raw)); err != nil {
		fail(w, 500, "无法保存技能，请检查磁盘空间。")
		return
	}
	committed = true
	// Mounting is a follow-up, so a failure leaves a valid unmounted Skill rather
	// than reporting an error for work that already succeeded.
	s.mountOrAudit(strings.TrimSpace(input.Mount), []string{skill.ID})
	s.audit("skill.create", skill.ID, "name="+skill.Name)
	respond(w, 201, skill)
}

// Adding a file to an existing Skill keeps the catalog editable end to end: a
// hand-created Skill can grow references and scripts without re-uploading it.
func (s *Server) createSkillFile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decode(w, r, &input) {
		return
	}
	path := strings.TrimSpace(input.Path)
	if err := validSkillFilePath(path); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !utf8.ValidString(input.Content) || len(input.Content) > 128000 {
		fail(w, 400, "技能文件必须是 UTF-8 文本，且不超过 128,000 字节。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	var skill Skill
	if err := s.store.db.QueryRow("SELECT config FROM skills WHERE id=? AND archived=0", r.PathValue("id")).Scan(&raw); err != nil {
		fail(w, 404, "技能不存在。")
		return
	}
	if json.Unmarshal([]byte(raw), &skill) != nil {
		internal(w)
		return
	}
	if path == "SKILL.md" {
		fail(w, 400, "SKILL.md 已存在，请直接编辑它。")
		return
	}
	if len(skill.Files) >= 200 {
		fail(w, 400, "单个技能最多 200 个文件。")
		return
	}
	for _, file := range skill.Files {
		if file.Path == path {
			fail(w, 409, "同名文件已存在。")
			return
		}
		// A file cannot also be a directory of another file.
		if strings.HasPrefix(file.Path, path+"/") || strings.HasPrefix(path, file.Path+"/") {
			fail(w, 400, "路径与已有文件冲突。")
			return
		}
	}
	root, err := os.OpenRoot(filepath.Join(s.store.directory, "skills", skill.ID))
	if err != nil {
		internal(w)
		return
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(path), 0700); err != nil {
		fail(w, 500, "无法创建文件目录。")
		return
	}
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		fail(w, 500, "无法写入文件，请检查存储状态。")
		return
	}
	if _, err := file.WriteString(input.Content); err != nil {
		file.Close()
		fail(w, 500, "无法写入文件，请检查存储状态。")
		return
	}
	if err := file.Close(); err != nil {
		internal(w)
		return
	}
	skill.Files = append(skill.Files, SkillFile{Path: path, Size: int64(len(input.Content))})
	updated, _ := json.Marshal(skill)
	if _, err := s.store.db.Exec("UPDATE skills SET config=? WHERE id=? AND archived=0", string(updated), skill.ID); err != nil {
		internal(w)
		return
	}
	s.audit("skill.file.create", skill.ID, "path="+path)
	respond(w, 201, skill)
}

// validSkillFilePath enforces the same shape the upload importer accepts, so a
// path created through the editor cannot escape the Skill directory.
func validSkillFilePath(path string) error {
	if path == "" || len(path) > 200 {
		return fmt.Errorf("文件路径须为 1–200 字节。")
	}
	if strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || strings.Contains(path, "\\") {
		return fmt.Errorf("文件路径不能以斜杠开头或结尾，也不能包含反斜杠。")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("文件路径不能包含空段、. 或 ..。")
		}
	}
	if !utf8.ValidString(path) {
		return fmt.Errorf("文件路径必须是 UTF-8 文本。")
	}
	return nil
}

// Removing a file completes the editing loop: a hand-created reference with a
// wrong name must be removable without deleting and re-creating the Skill.
func (s *Server) deleteSkillFile(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "SKILL.md" {
		fail(w, 400, "SKILL.md 是技能入口，不能删除；可编辑其内容或删除整个技能。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	var skill Skill
	if err := s.store.db.QueryRow("SELECT config FROM skills WHERE id=? AND archived=0", r.PathValue("id")).Scan(&raw); err != nil {
		fail(w, 404, "技能不存在。")
		return
	}
	if json.Unmarshal([]byte(raw), &skill) != nil {
		internal(w)
		return
	}
	index := -1
	for i, file := range skill.Files {
		if file.Path == path {
			index = i
			break
		}
	}
	if index < 0 {
		fail(w, 404, "技能文件不存在。")
		return
	}
	root, err := os.OpenRoot(filepath.Join(s.store.directory, "skills", skill.ID))
	if err != nil {
		internal(w)
		return
	}
	defer root.Close()
	if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
		fail(w, 500, "无法删除文件，请检查存储状态。")
		return
	}
	// An empty directory left behind by the deleted file is harmless but noisy in
	// the file tree, so prune upward until something non-empty is reached.
	directory := filepath.Dir(path)
	for directory != "." && directory != "/" {
		if err := root.Remove(directory); err != nil {
			break
		}
		directory = filepath.Dir(directory)
	}
	skill.Files = append(skill.Files[:index], skill.Files[index+1:]...)
	updated, _ := json.Marshal(skill)
	if _, err := s.store.db.Exec("UPDATE skills SET config=? WHERE id=? AND archived=0", string(updated), skill.ID); err != nil {
		internal(w)
		return
	}
	s.audit("skill.file.delete", skill.ID, "path="+path)
	respond(w, 200, skill)
}
