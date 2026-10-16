package platform

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillZIP(t *testing.T, directory string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	if err := filepath.WalkDir(directory, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, name)
		if err != nil {
			return err
		}
		file, err := writer.Create(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
func (f *fixture) skillUpload(t *testing.T, name string, data []byte, trusted bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if trusted {
		writer.WriteField("trusted", "true")
	}
	file, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return f.skillMultipart(t, body.Bytes(), writer.FormDataContentType())
}
func (f *fixture) skillMultipart(t *testing.T, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest("POST", "http://localhost/api/skills", bytes.NewReader(body))
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Content-Type", contentType)
	request.AddCookie(f.cookie)
	request.Header.Set("X-CSRF-Token", f.csrf)
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, request)
	return response
}
func TestSkillUploadPackageAndFolderAreManagedWithoutAutoGrant(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	content := []byte("---\nname: uploaded-skill\ndescription: User uploaded instructions.\n---\nUse explicitly authorized tools.\n")
	var packageData bytes.Buffer
	writer := zip.NewWriter(&packageData)
	for name, data := range map[string][]byte{"bundle/SKILL.md": content, "bundle/references/guide.md": []byte("reference text"), "__MACOSX/._bundle": []byte("metadata")} {
		file, _ := writer.Create(name)
		file.Write(data)
	}
	writer.Close()
	response := f.skillUpload(t, "bundle.zip", packageData.Bytes(), true)
	requireStatus(t, response, 201)
	var installed Skill
	if err := json.Unmarshal(response.Body.Bytes(), &installed); err != nil {
		t.Fatal(err)
	}
	if installed.Name != "uploaded-skill" || len(installed.Files) != 2 || strings.Contains(response.Body.String(), f.directory) {
		t.Fatal("incorrect managed upload", installed)
	}
	requireStatus(t, f.request(t, "GET", "/api/skills/"+installed.ID+"/files/references/guide.md", nil, true, nil), 200)
	info, err := os.Stat(filepath.Join(f.directory, "skills", installed.ID, "references", "guide.md"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("managed file or permissions missing", err)
	}
	bindings, err := f.store.bindings("default")
	if err != nil || len(bindings.Skills) != 0 {
		t.Fatal("upload granted access automatically", err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/agents/default/capabilities", map[string]any{"skills": []string{installed.ID}, "mcps": []MCPBinding{}, "confirm": true}, true, nil), 200)
	snapshot, _, err := f.store.capabilitySnapshot(CapabilityBindings{Skills: []string{installed.ID}})
	if err != nil || len(snapshot.Skills) != 1 || snapshot.Skills[0].Name != installed.Name {
		t.Fatal("uploaded skill cannot be mounted", err)
	}
	var folder bytes.Buffer
	multipartWriter := multipart.NewWriter(&folder)
	multipartWriter.WriteField("trusted", "true")
	for name, data := range map[string][]byte{"SKILL.md": content, "references/guide.md": []byte("folder reference")} {
		part, _ := multipartWriter.CreateFormFile("files", name)
		part.Write(data)
	}
	multipartWriter.Close()
	response = f.skillMultipart(t, folder.Bytes(), multipartWriter.FormDataContentType())
	requireStatus(t, response, 201)
	var second Skill
	json.Unmarshal(response.Body.Bytes(), &second)
	if second.ID == installed.ID || len(second.Files) != 2 {
		t.Fatal("uploads did not get isolated directories")
	}
	entries, err := os.ReadDir(filepath.Join(f.directory, ".skill-uploads"))
	if err != nil || len(entries) != 0 {
		t.Fatal("upload staging left behind", err)
	}
	requireStatus(t, f.skillUpload(t, "SKILL.md", content, true), 201)
	requireStatus(t, f.skillUpload(t, "SKILL.md", content, false), 400)
	requireStatus(t, f.request(t, "POST", "/api/skills/import", map[string]any{"directory": f.directory, "trusted": true}, true, nil), 400)
}
func TestSkillUploadRejectsUnsafeAndOversizedPackages(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	valid := "---\nname: safe\ndescription: Safe instructions.\n---\nInstructions.\n"
	cases := []struct {
		name string
		mode os.FileMode
		data string
	}{
		{"../outside", 0600, "escape"}, {"/absolute", 0600, "escape"}, {"C:/outside", 0600, "escape"}, {`folder\outside`, 0600, "escape"},
		{"link", os.ModeSymlink | 0600, "../../master.key"}, {"device", os.ModeNamedPipe | 0600, ""}, {"large", 0600, strings.Repeat("x", (2<<20)+1)},
		{"SKILL.md", 0600, "missing frontmatter"}, {"SKILL.md", 0600, strings.Repeat("x", 64001)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var data bytes.Buffer
			writer := zip.NewWriter(&data)
			if test.name != "SKILL.md" {
				file, _ := writer.Create("SKILL.md")
				io.WriteString(file, valid)
			}
			header := &zip.FileHeader{Name: test.name, Method: zip.Deflate}
			header.SetMode(test.mode)
			file, err := writer.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			io.WriteString(file, test.data)
			writer.Close()
			requireStatus(t, f.skillUpload(t, "unsafe.zip", data.Bytes(), true), 400)
			if entries, err := os.ReadDir(filepath.Join(f.directory, "skills")); err == nil && len(entries) != 0 {
				t.Fatal("rejected upload installed files")
			}
			if entries, err := os.ReadDir(filepath.Join(f.directory, ".skill-uploads")); err == nil && len(entries) != 0 {
				t.Fatal("rejected upload left staging")
			}
		})
	}
	for _, kind := range []string{"duplicate", "count", "total", "missing-root"} {
		t.Run(kind, func(t *testing.T) {
			var data bytes.Buffer
			writer := zip.NewWriter(&data)
			file, _ := writer.Create("SKILL.md")
			io.WriteString(file, valid)
			switch kind {
			case "duplicate":
				file, _ := writer.Create("SKILL.md")
				io.WriteString(file, valid)
			case "count":
				for i := 0; i < 201; i++ {
					writer.Create(strings.Repeat("x", i+1))
				}
			case "total":
				for _, name := range []string{"a", "b", "c", "d", "e"} {
					file, _ := writer.Create(name)
					io.WriteString(file, strings.Repeat("x", 2<<20))
				}
			case "missing-root":
				writer.Close()
				data.Reset()
				writer = zip.NewWriter(&data)
				file, _ := writer.Create("README.md")
				io.WriteString(file, "no skill")
			}
			writer.Close()
			requireStatus(t, f.skillUpload(t, "bad.zip", data.Bytes(), true), 400)
		})
	}
}

func TestSkillUploadCleansAbandonedStagingOnOpen(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.importSkill(skillDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	staging := filepath.Join(directory, ".skill-uploads", "abandoned")
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "partial"), []byte("incomplete upload"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatal("abandoned upload was retained", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "skills", installed.ID, "SKILL.md")); err != nil {
		t.Fatal("published skill was removed", err)
	}
}
