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
func skillFilesZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	root := t.TempDir()
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return skillZIP(t, root)
}
func (f *fixture) skillUpload(t *testing.T, name string, data []byte, trusted bool) *httptest.ResponseRecorder {
	t.Helper()
	return f.skillUploadWithOverwrite(t, name, data, trusted, false)
}
func (f *fixture) skillUploadWithOverwrite(t *testing.T, name string, data []byte, trusted, overwrite bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if trusted {
		writer.WriteField("trusted", "true")
	}
	if overwrite {
		writer.WriteField("overwrite", "true")
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
	folderContent := []byte("---\nname: folder-skill\ndescription: Folder uploaded instructions.\n---\nFolder body.\n")
	for name, data := range map[string][]byte{"SKILL.md": folderContent, "references/guide.md": []byte("folder reference")} {
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
	// Same declared name requires an explicit overwrite.
	requireStatus(t, f.skillUpload(t, "SKILL.md", content, true), 400)
	response = f.skillUploadWithOverwrite(t, "SKILL.md", content, true, true)
	requireStatus(t, response, 200)
	var replaced Skill
	json.Unmarshal(response.Body.Bytes(), &replaced)
	if replaced.ID != installed.ID {
		t.Fatal("overwrite did not keep the installed skill id", replaced)
	}
	bindings, err = f.store.bindings("default")
	if err != nil || len(bindings.Skills) != 1 || bindings.Skills[0] != installed.ID {
		t.Fatal("overwrite dropped Agent bindings", bindings, err)
	}
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

func TestSkillFileCanBeEditedAndMetadataUpdated(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	content := []byte("---\nname: editable\ndescription: Before.\n---\nOld body.\n")
	response := f.skillUpload(t, "SKILL.md", content, true)
	requireStatus(t, response, 201)
	var skill Skill
	if err := json.Unmarshal(response.Body.Bytes(), &skill); err != nil {
		t.Fatal(err)
	}
	updatedContent := "---\nname: editable\ndescription: After.\ncompatibility: Node 24\n---\nNew **body**.\n"
	response = f.request(t, "PUT", "/api/skills/"+skill.ID+"/files/SKILL.md", map[string]string{"content": updatedContent}, true, nil)
	requireStatus(t, response, 200)
	var updated Skill
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Description != "After." || updated.Compatibility != "Node 24" || updated.Content != "New **body**.\n" {
		t.Fatalf("metadata not updated: %+v", updated)
	}
	raw, err := os.ReadFile(filepath.Join(f.directory, "skills", skill.ID, "SKILL.md"))
	if err != nil || string(raw) != updatedContent {
		t.Fatal("file content not updated", err)
	}
	requireStatus(t, f.request(t, "PUT", "/api/skills/"+skill.ID+"/files/missing.md", map[string]string{"content": "x"}, true, nil), 404)
}

func TestSkillExportSingleAndBundleRoundTrip(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	first := "---\nname: export-one\ndescription: First exported skill.\n---\nOne.\n"
	second := "---\nname: export-two\ndescription: Second exported skill.\n---\nTwo.\n"
	response := f.skillUpload(t, "one.zip", skillFilesZIP(t, map[string]string{"SKILL.md": first, "references/note.md": "note"}), true)
	requireStatus(t, response, 201)
	var firstSkill Skill
	if err := json.Unmarshal(response.Body.Bytes(), &firstSkill); err != nil {
		t.Fatal(err)
	}
	response = f.skillUpload(t, "two.zip", skillFilesZIP(t, map[string]string{"SKILL.md": second}), true)
	requireStatus(t, response, 201)
	var secondSkill Skill
	if err := json.Unmarshal(response.Body.Bytes(), &secondSkill); err != nil {
		t.Fatal(err)
	}
	archiveNames := func(response *httptest.ResponseRecorder) map[string]string {
		t.Helper()
		reader, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]string{}
		for _, entry := range reader.File {
			file, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			names[entry.Name] = string(data)
		}
		return names
	}
	response = f.request(t, "GET", "/api/skills/"+firstSkill.ID+"/export", nil, true, nil)
	requireStatus(t, response, 200)
	if response.Header().Get("Content-Type") != "application/zip" || !strings.Contains(response.Header().Get("Content-Disposition"), "export-one.zip") {
		t.Fatal("single export headers are incorrect", response.Header())
	}
	single := archiveNames(response)
	if single["SKILL.md"] != first || single["references/note.md"] != "note" {
		t.Fatalf("single export is incomplete: %+v", single)
	}
	response = f.request(t, "GET", "/api/skills/export", nil, true, nil)
	requireStatus(t, response, 200)
	bundle := archiveNames(response)
	if bundle["export-one/SKILL.md"] != first || bundle["export-one/references/note.md"] != "note" || bundle["export-two/SKILL.md"] != second {
		t.Fatalf("bundle export is incomplete: %+v", bundle)
	}
	// Exporting the whole library produces a package that can be restored with
	// overwrite while keeping every installed skill ID and Agent binding.
	response = f.skillUploadWithOverwrite(t, "library.zip", f.request(t, "GET", "/api/skills/export", nil, true, nil).Body.Bytes(), true, true)
	requireStatus(t, response, 200)
	var restored struct {
		Skills   []Skill `json:"skills"`
		Replaced int     `json:"replaced"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.Skills) != 2 || restored.Replaced != 2 {
		t.Fatalf("bundle restore did not overwrite both skills: %+v", restored)
	}
	ids := map[string]string{}
	for _, skill := range restored.Skills {
		ids[skill.Name] = skill.ID
	}
	if ids["export-one"] != firstSkill.ID || ids["export-two"] != secondSkill.ID {
		t.Fatal("bundle restore changed skill IDs", ids)
	}
	response = f.request(t, "GET", "/api/skills", nil, true, nil)
	requireStatus(t, response, 200)
	var installed []Skill
	json.Unmarshal(response.Body.Bytes(), &installed)
	if len(installed) != 2 {
		t.Fatal("bundle restore duplicated skills", installed)
	}
}
