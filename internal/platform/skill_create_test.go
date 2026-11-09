package platform

import (
	"encoding/json"
	"strings"
	"testing"
)

// Creating a Skill by hand must produce the same record an upload produces, so
// the rest of the catalog cannot tell them apart.
func TestSkillCreateMatchesUpload(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	agent := f.agent(t, "客服 A")
	body := "---\nname: hand-written\ndescription: Created in the editor.\ncompatibility: none\n---\nUse the reference.\n"
	response := f.request(t, "POST", "/api/skills/new", map[string]any{"content": body, "mount": agent}, true, nil)
	requireStatus(t, response, 201)
	var skill Skill
	json.Unmarshal(response.Body.Bytes(), &skill)
	if skill.Name != "hand-written" || skill.Description != "Created in the editor." {
		t.Fatalf("incorrect metadata: %+v", skill)
	}
	if len(skill.Files) != 1 || skill.Files[0].Path != "SKILL.md" {
		t.Fatalf("a created Skill starts with SKILL.md only: %+v", skill.Files)
	}
	preview := f.request(t, "GET", "/api/skills/"+skill.ID+"/files/SKILL.md", nil, true, nil)
	requireStatus(t, preview, 200)
	if !strings.Contains(preview.Body.String(), "Use the reference.") {
		t.Fatal("the stored file must keep the body")
	}
	// mount was honoured, exactly like an upload.
	mounted := f.request(t, "GET", "/api/agents/"+agent+"/capabilities", nil, true, nil)
	requireStatus(t, mounted, 200)
	var bindings CapabilityBindings
	json.Unmarshal(mounted.Body.Bytes(), &bindings)
	if len(bindings.Skills) != 1 || bindings.Skills[0] != skill.ID {
		t.Fatalf("creation must mount to the named Agent: %+v", bindings.Skills)
	}
}

// A hand-written Skill must satisfy exactly the rules an uploaded one does.
func TestSkillCreateValidatesMetadata(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	for _, body := range []string{
		"no frontmatter\n",
		"---\nname: Bad_Name\ndescription: x\n---\nbody\n",
		"---\nname: ok\ndescription:\n---\nbody\n",
		"---\nname: ok\n---\nbody\n",
	} {
		requireStatus(t, f.request(t, "POST", "/api/skills/new", map[string]any{"content": body}, true, nil), 400)
	}
	valid := "---\nname: ok\ndescription: fine\n---\nbody\n"
	requireStatus(t, f.request(t, "POST", "/api/skills/new", map[string]any{"content": valid}, true, nil), 201)
	// A duplicate name is refused rather than silently overwriting an edited Skill.
	requireStatus(t, f.request(t, "POST", "/api/skills/new", map[string]any{"content": valid}, true, nil), 409)
}

// Files added through the editor must not escape the Skill directory.
func TestSkillFilePathsAreConfined(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	response := f.request(t, "POST", "/api/skills/new", map[string]any{"content": "---\nname: confined\ndescription: d\n---\nbody\n"}, true, nil)
	requireStatus(t, response, 201)
	var skill Skill
	json.Unmarshal(response.Body.Bytes(), &skill)
	for _, path := range []string{"", "/etc/passwd", "../escape.md", "a//b.md", "a/./b.md", "a\\b.md", "trailing/"} {
		requireStatus(t, f.request(t, "POST", "/api/skills/"+skill.ID+"/files", map[string]any{"path": path, "content": "x"}, true, nil), 400)
	}
	// SKILL.md is edited, not added.
	requireStatus(t, f.request(t, "POST", "/api/skills/"+skill.ID+"/files", map[string]any{"path": "SKILL.md", "content": "x"}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/skills/"+skill.ID+"/files", map[string]any{"path": "references/status.md", "content": "ok"}, true, nil), 201)
	// A file cannot also become a directory, and duplicates are refused.
	requireStatus(t, f.request(t, "POST", "/api/skills/"+skill.ID+"/files", map[string]any{"path": "references", "content": "x"}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/skills/"+skill.ID+"/files", map[string]any{"path": "references/status.md", "content": "again"}, true, nil), 409)
}

// Deleting a file must update the record and prune the directory it leaves empty.
func TestSkillFileDeleteUpdatesRecord(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	response := f.request(t, "POST", "/api/skills/new", map[string]any{"content": "---\nname: prunable\ndescription: d\n---\nbody\n"}, true, nil)
	requireStatus(t, response, 201)
	var skill Skill
	json.Unmarshal(response.Body.Bytes(), &skill)
	requireStatus(t, f.request(t, "POST", "/api/skills/"+skill.ID+"/files", map[string]any{"path": "references/status.md", "content": "ok"}, true, nil), 201)
	deleted := f.request(t, "DELETE", "/api/skills/"+skill.ID+"/files/references/status.md", nil, true, nil)
	requireStatus(t, deleted, 200)
	var updated Skill
	json.Unmarshal(deleted.Body.Bytes(), &updated)
	if len(updated.Files) != 1 || updated.Files[0].Path != "SKILL.md" {
		t.Fatalf("the record must drop the deleted file: %+v", updated.Files)
	}
	// SKILL.md is the entry point and cannot be removed on its own.
	requireStatus(t, f.request(t, "DELETE", "/api/skills/"+skill.ID+"/files/SKILL.md", nil, true, nil), 400)
	requireStatus(t, f.request(t, "DELETE", "/api/skills/"+skill.ID+"/files/missing.md", nil, true, nil), 404)
}
