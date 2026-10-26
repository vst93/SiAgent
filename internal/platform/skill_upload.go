package platform

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxSkillUpload = 10 << 20

// Uploaded paths are package-relative names, never host filesystem addresses.
func skillUploadPath(name string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	if len(name) > 512 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("技能文件路径无效，请使用不超过 512 字节的相对路径。")
	}
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.HasPrefix(name, "/") || path.Clean(name) != name || strings.Count(name, "/") >= 16 {
		return "", fmt.Errorf("技能包不能包含绝对路径、上级目录或超过 16 层的路径。")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." {
			return "", fmt.Errorf("技能包不能包含上级目录。")
		}
	}
	return name, nil
}

type skillUploadTree struct {
	root    string
	files   int
	total   int
	entries int
	dirs    map[string]bool
	seen    map[string]bool
}

func (tree *skillUploadTree) add(name string, directory bool, reader io.Reader) error {
	name, err := skillUploadPath(name)
	if err != nil {
		return err
	}
	// Finder metadata is not part of the skill, but its paths must still be valid.
	if strings.Split(name, "/")[0] == "__MACOSX" || path.Base(name) == ".DS_Store" {
		return nil
	}
	if tree.seen[name] {
		return fmt.Errorf("技能包包含重复文件或目录，请移除重复项。")
	}
	tree.seen[name] = true
	parent := path.Dir(name)
	if directory {
		parent = name
	}
	if parent != "." {
		current := ""
		for _, part := range strings.Split(parent, "/") {
			if current != "" {
				current += "/"
			}
			current += part
			if !tree.dirs[current] {
				tree.entries++
				if tree.entries > 4000 {
					return fmt.Errorf("单次上传最多包含 4000 个目录项。")
				}
				if err := os.Mkdir(filepath.Join(tree.root, filepath.FromSlash(current)), 0700); err != nil {
					return fmt.Errorf("技能包的文件与目录冲突，或存储不可用。")
				}
				tree.dirs[current] = true
			}
		}
	}
	if directory {
		return nil
	}
	if tree.files >= 2000 {
		return fmt.Errorf("上传最多包含 2000 个文件。")
	}
	data, err := io.ReadAll(io.LimitReader(reader, (2<<20)+1))
	if err != nil {
		return fmt.Errorf("技能文件读取失败，请检查文件是否完整。")
	}
	if len(data) > 2<<20 || tree.total+len(data) > 80<<20 {
		return fmt.Errorf("技能单文件最多 2 MiB，单次上传解包后总计最多 80 MiB。")
	}
	tree.entries++
	if tree.entries > 4000 {
		return fmt.Errorf("单次上传最多包含 4000 个目录项。")
	}
	file, err := os.OpenFile(filepath.Join(tree.root, filepath.FromSlash(name)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("技能包的文件与目录冲突，或存储不可用。")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return fmt.Errorf("技能文件保存失败，请检查可用空间。")
	}
	tree.files++
	tree.total += len(data)
	return nil
}

func (s *Server) importSkill(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSkillUpload+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "请上传 ZIP 技能包、SKILL.md 或本机文件夹，不再接受服务器目录地址。")
		return
	}
	stagingRoot := filepath.Join(s.store.directory, ".skill-uploads")
	if err := os.MkdirAll(stagingRoot, 0700); err != nil {
		fail(w, 500, "无法准备技能上传，请检查存储空间。")
		return
	}
	staging, err := os.MkdirTemp(stagingRoot, "upload-")
	if err != nil {
		fail(w, 500, "无法准备技能上传，请检查存储空间。")
		return
	}
	defer os.RemoveAll(staging)
	tree := skillUploadTree{root: staging, entries: 1, dirs: map[string]bool{}, seen: map[string]bool{}}
	trusted, trustSeen := false, false
	overwrite, overwriteSeen := false, false
	mode := ""
	for {
		if r.Context().Err() != nil {
			return
		}
		part, readErr := reader.NextPart()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			fail(w, 400, "上传未完成或超过 10 MiB，请重新选择技能文件。")
			return
		}
		if part.FormName() == "trusted" && !trustSeen {
			value, err := io.ReadAll(io.LimitReader(part, 9))
			part.Close()
			trustSeen = true
			if err != nil || string(value) != "true" {
				fail(w, 400, "请确认技能来自可信来源。")
				return
			}
			trusted = true
			continue
		}
		if part.FormName() == "overwrite" && !overwriteSeen {
			value, err := io.ReadAll(io.LimitReader(part, 9))
			part.Close()
			overwriteSeen = true
			if err != nil || (string(value) != "true" && string(value) != "false") {
				fail(w, 400, "覆盖选项无效。")
				return
			}
			overwrite = string(value) == "true"
			continue
		}
		field := part.FormName()
		if field != "file" && field != "files" {
			part.Close()
			fail(w, 400, "技能上传字段无效。")
			return
		}
		if mode != "" && (mode != field || field == "file") {
			part.Close()
			fail(w, 400, "一次只能上传一个技能包或一个技能文件夹。")
			return
		}
		mode = field
		_, parameters, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		name := parameters["filename"]
		if err != nil || name == "" {
			part.Close()
			fail(w, 400, "请选择技能文件。")
			return
		}
		if field == "files" {
			err = tree.add(name, false, part)
			part.Close()
		} else if name == "SKILL.md" {
			err = tree.add(name, false, part)
			part.Close()
		} else if strings.EqualFold(path.Ext(name), ".zip") {
			// The original ZIP is never installed or used as a filesystem path.
			data, readErr := io.ReadAll(io.LimitReader(part, maxSkillUpload+1))
			part.Close()
			if readErr != nil || len(data) > maxSkillUpload {
				fail(w, 400, "上传未完成或超过 10 MiB。")
				return
			}
			archive, zipErr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if zipErr != nil || len(archive.File) > 4000 {
				fail(w, 400, "ZIP 无效或超过 4000 个目录项，请检查技能包。")
				return
			}
			for _, entry := range archive.File {
				if r.Context().Err() != nil {
					return
				}
				if entry.Mode()&os.ModeSymlink != 0 || (!entry.Mode().IsRegular() && !entry.Mode().IsDir()) {
					err = fmt.Errorf("技能包不接受符号链接或特殊文件。")
					break
				}
				if entry.UncompressedSize64 > 2<<20 {
					err = fmt.Errorf("技能单文件不能超过 2 MiB。")
					break
				}
				if entry.Mode().IsDir() {
					err = tree.add(entry.Name, true, nil)
				} else {
					file, openErr := entry.Open()
					if openErr != nil {
						err = fmt.Errorf("ZIP 无法读取，请检查文件是否损坏或加密。")
						break
					}
					err = tree.add(entry.Name, false, file)
					file.Close()
				}
				if err != nil {
					break
				}
			}
		} else {
			part.Close()
			err = fmt.Errorf("请选择 ZIP 技能包或名称为 SKILL.md 的文件。")
		}
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	if !trusted || tree.files == 0 {
		fail(w, 400, "请选择技能文件并确认可信来源。")
		return
	}
	source := staging
	units, err := skillImportUnits(source)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(units) > 100 {
		fail(w, 400, "一次最多导入 100 个技能。")
		return
	}
	if len(units) == 1 {
		skill, replaced, err := s.store.importSkillWithOverwrite(units[0], overwrite)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if replaced {
			respond(w, 200, skill)
		} else {
			respond(w, 201, skill)
		}
		return
	}
	imported := make([]Skill, 0, len(units))
	replaced := 0
	for _, unit := range units {
		if r.Context().Err() != nil {
			return
		}
		skill, overwritten, err := s.store.importSkillWithOverwrite(unit, overwrite)
		if err != nil {
			fail(w, 400, fmt.Sprintf("批量导入在“%s”处中止：%s 已导入 %d 个技能。", filepath.Base(unit), err.Error(), len(imported)))
			return
		}
		imported = append(imported, skill)
		if overwritten {
			replaced++
		}
	}
	status := 201
	if replaced > 0 {
		status = 200
	}
	respond(w, status, map[string]any{"skills": imported, "replaced": replaced})
}

// skillImportUnits accepts one skill at the staging root, one skill inside a
// single top-level folder, or a bundle whose top-level folders each hold a skill.
func skillImportUnits(staging string) ([]string, error) {
	if info, err := os.Stat(filepath.Join(staging, "SKILL.md")); err == nil && info.Mode().IsRegular() {
		return []string{staging}, nil
	}
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) == 0 {
		return nil, fmt.Errorf("技能包根部或顶层文件夹内须包含 SKILL.md。")
	}
	units := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			return nil, fmt.Errorf("技能包根部或每个顶层文件夹内须包含 SKILL.md。")
		}
		info, err := os.Stat(filepath.Join(staging, entry.Name(), "SKILL.md"))
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("技能包根部或每个顶层文件夹内须包含 SKILL.md。")
		}
		units = append(units, filepath.Join(staging, entry.Name()))
	}
	return units, nil
}
