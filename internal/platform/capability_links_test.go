package platform

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

// An upload that names an Agent must mount the skill there without a second
// checkbox step; that is the whole point of scoping the catalog.
func TestSkillUploadMountsToNamedAgent(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	first := f.agent(t, "客服 A")
	second := f.agent(t, "售后 B")
	root := skillDirectory(t)
	response := f.skillUploadTo(t, "skill.zip", skillZIP(t, root), true, first)
	requireStatus(t, response, 201)
	var skill Skill
	json.Unmarshal(response.Body.Bytes(), &skill)
	mounted := f.request(t, "GET", "/api/agents/"+first+"/capabilities", nil, true, nil)
	requireStatus(t, mounted, 200)
	var bindings CapabilityBindings
	json.Unmarshal(mounted.Body.Bytes(), &bindings)
	if len(bindings.Skills) != 1 || bindings.Skills[0] != skill.ID {
		t.Fatalf("upload must mount to the named Agent: %+v", bindings.Skills)
	}
	other := f.request(t, "GET", "/api/agents/"+second+"/capabilities", nil, true, nil)
	requireStatus(t, other, 200)
	var untouched CapabilityBindings
	json.Unmarshal(other.Body.Bytes(), &untouched)
	if len(untouched.Skills) != 0 {
		t.Fatalf("other Agents must not inherit the upload: %+v", untouched.Skills)
	}
}

// Sharing is a reference, never a copy: mounting into a second Agent leaves one
// skill record that both Agents point at.
func TestSkillMountSharesOneRecord(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	first := f.agent(t, "客服 A")
	second := f.agent(t, "售后 B")
	root := skillDirectory(t)
	response := f.skillUploadTo(t, "skill.zip", skillZIP(t, root), true, first)
	requireStatus(t, response, 201)
	var skill Skill
	json.Unmarshal(response.Body.Bytes(), &skill)

	requireStatus(t, f.request(t, "POST", "/api/agents/"+second+"/skills/"+skill.ID+"/mount", nil, true, nil), 200)
	for _, agent := range []string{first, second} {
		w := f.request(t, "GET", "/api/agents/"+agent+"/capabilities", nil, true, nil)
		requireStatus(t, w, 200)
		var bindings CapabilityBindings
		json.Unmarshal(w.Body.Bytes(), &bindings)
		if len(bindings.Skills) != 1 || bindings.Skills[0] != skill.ID {
			t.Fatalf("%s should mount the shared skill: %+v", agent, bindings.Skills)
		}
	}
	all := f.request(t, "GET", "/api/skills", nil, true, nil)
	requireStatus(t, all, 200)
	var skills []Skill
	json.Unmarshal(all.Body.Bytes(), &skills)
	if len(skills) != 1 {
		t.Fatalf("sharing must not duplicate the skill: %d records", len(skills))
	}

	// The link view is what the scoped catalog groups by.
	links := f.request(t, "GET", "/api/capability-links", nil, true, nil)
	requireStatus(t, links, 200)
	var usage CapabilityLinks
	json.Unmarshal(links.Body.Bytes(), &usage)
	if len(usage.Skills[skill.ID]) != 2 {
		t.Fatalf("both Agents must be reported as users: %+v", usage.Skills[skill.ID])
	}

	// Editing content affects both Agents because there is only one file set.
	requireStatus(t, f.request(t, "PUT", "/api/skills/"+skill.ID+"/files/SKILL.md", map[string]string{"content": "---\nname: sample-skill\ndescription: Edited once.\n---\nShared body.\n"}, true, nil), 200)
	preview := f.request(t, "GET", "/api/skills/"+skill.ID+"/files/SKILL.md", nil, true, nil)
	requireStatus(t, preview, 200)
	if !bytes.Contains(preview.Body.Bytes(), []byte("Shared body")) {
		t.Fatal("the single stored copy should carry the edit")
	}
}

// The mount endpoint refuses unknown or archived resources instead of silently
// succeeding, so the scoped catalog cannot drift out of sync with the library.
func TestSkillMountValidatesTargets(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	agent := f.agent(t, "客服 A")
	requireStatus(t, f.request(t, "POST", "/api/agents/"+agent+"/skills/missing/mount", nil, true, nil), 404)
	requireStatus(t, f.request(t, "POST", "/api/agents/missing/skills/missing/mount", nil, true, nil), 404)
}

// mountSkills must not resurrect authorization for an archived Agent.
func TestMountSkillsIgnoresArchivedAgent(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	agent := f.agent(t, "客服 A")
	// Archiving requires a disabled Agent first.
	requireStatus(t, f.request(t, "PUT", "/api/agents/"+agent, map[string]any{"name": "客服 A", "enabled": false, "model_id": 1, "instructions": "test"}, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/agents/"+agent, nil, true, nil), 200)
	if err := f.server.mountSkills(agent, []string{"anything"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.store.db.QueryRow("SELECT count(*) FROM agent_capabilities WHERE agent_id=?", agent).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("archived Agents must not receive new mounts")
	}
}

func (f *fixture) skillUploadTo(t *testing.T, name string, data []byte, trusted bool, agent string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if trusted {
		writer.WriteField("trusted", "true")
	}
	if agent != "" {
		writer.WriteField("mount", agent)
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

func (f *fixture) agent(t *testing.T, name string) string {
	t.Helper()
	w := f.request(t, "POST", "/api/agents", map[string]any{"name": name, "enabled": true, "model_id": 1, "instructions": "test"}, true, nil)
	requireStatus(t, w, 201)
	var agent Agent
	json.Unmarshal(w.Body.Bytes(), &agent)
	return agent.ID
}

// A failed follow-up mount must not turn a stored skill into an upload error:
// the file exists, so the request succeeds and the skill simply stays unmounted.
func TestSkillUploadSurvivesMountFailure(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	root := skillDirectory(t)
	// An unknown Agent is the simplest reproducible mount failure.
	response := f.skillUploadTo(t, "skill.zip", skillZIP(t, root), true, "no-such-agent")
	requireStatus(t, response, 201)
	var skill Skill
	json.Unmarshal(response.Body.Bytes(), &skill)
	all := f.request(t, "GET", "/api/skills", nil, true, nil)
	requireStatus(t, all, 200)
	var skills []Skill
	json.Unmarshal(all.Body.Bytes(), &skills)
	if len(skills) != 1 || skills[0].ID != skill.ID {
		t.Fatalf("the skill must stay stored after a mount failure: %+v", skills)
	}
}
