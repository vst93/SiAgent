package platform

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
)

// CapabilityLinks answers "which Agents use this resource" for every resource at
// once. The scoped catalog view needs the whole picture to group rows into
// mounted, shared and unused, and one round trip beats N per-resource lookups.
type CapabilityLinks struct {
	Skills    map[string][]LinkedAgent `json:"skills"`
	MCPs      map[string][]LinkedAgent `json:"mcps"`
	Knowledge map[string][]LinkedAgent `json:"knowledge"`
}

func (s *Server) capabilityLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.store.capabilityLinks()
	if err != nil {
		internal(w)
		return
	}
	respond(w, 200, links)
}

func (s *Store) capabilityLinks() (CapabilityLinks, error) {
	links := CapabilityLinks{
		Skills:    map[string][]LinkedAgent{},
		MCPs:      map[string][]LinkedAgent{},
		Knowledge: map[string][]LinkedAgent{},
	}
	agents := map[string]LinkedAgent{}
	rows, err := s.db.Query("SELECT id,name,archived FROM agents ORDER BY CASE WHEN id='default' THEN 0 ELSE 1 END,rowid")
	if err != nil {
		return links, err
	}
	defer rows.Close()
	for rows.Next() {
		var agent LinkedAgent
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.Archived); err != nil {
			return links, err
		}
		agents[agent.ID] = agent
	}
	if err := rows.Err(); err != nil {
		return links, err
	}
	capabilities, err := s.db.Query("SELECT agent_id,config FROM agent_capabilities")
	if err != nil {
		return links, err
	}
	defer capabilities.Close()
	for capabilities.Next() {
		var agentID, raw string
		if err := capabilities.Scan(&agentID, &raw); err != nil {
			return links, err
		}
		agent, ok := agents[agentID]
		if !ok {
			continue
		}
		var binding CapabilityBindings
		if json.Unmarshal([]byte(raw), &binding) != nil {
			continue
		}
		for _, id := range binding.Skills {
			links.Skills[id] = append(links.Skills[id], agent)
		}
		for _, item := range binding.MCPs {
			links.MCPs[item.ID] = append(links.MCPs[item.ID], agent)
		}
	}
	if err := capabilities.Err(); err != nil {
		return links, err
	}
	bindings, err := s.db.Query("SELECT agent_id,knowledge_id FROM knowledge_bindings")
	if err != nil {
		return links, err
	}
	defer bindings.Close()
	for bindings.Next() {
		var agentID, knowledgeID string
		if err := bindings.Scan(&agentID, &knowledgeID); err != nil {
			return links, err
		}
		if agent, ok := agents[agentID]; ok {
			links.Knowledge[knowledgeID] = append(links.Knowledge[knowledgeID], agent)
		}
	}
	return links, bindings.Err()
}

// mountSkills adds skills to one Agent's authorization and bumps its revision.
// Uploads name an Agent so the new skill is already mounted there; that is what
// removes the checkbox from the common case. A missing or archived Agent is not
// an error: the upload still succeeds as an unmounted skill.
func (s *Server) mountSkills(agentID string, skillIDs []string) error {
	if agentID == "" || len(skillIDs) == 0 {
		return nil
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var archived bool
	if err := tx.QueryRow("SELECT archived FROM agents WHERE id=?", agentID).Scan(&archived); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if archived {
		return nil
	}
	binding := CapabilityBindings{Skills: []string{}, MCPs: []MCPBinding{}}
	var raw string
	if err := tx.QueryRow("SELECT config FROM agent_capabilities WHERE agent_id=?", agentID).Scan(&raw); err == nil {
		if err := json.Unmarshal([]byte(raw), &binding); err != nil {
			return err
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	if binding.Skills == nil {
		binding.Skills = []string{}
	}
	if binding.MCPs == nil {
		binding.MCPs = []MCPBinding{}
	}
	changed := false
	for _, skillID := range skillIDs {
		if !slices.Contains(binding.Skills, skillID) {
			binding.Skills = append(binding.Skills, skillID)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO agent_capabilities VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET config=excluded.config", agentID, string(encoded)); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE agents SET revision=revision+1 WHERE id=?", agentID); err != nil {
		return err
	}
	return tx.Commit()
}

// mountSkill is the single explicit action left in the scoped catalog: attach an
// existing skill to the Agent you are currently viewing. It avoids re-saving the
// whole authorization set just to share one skill.
func (s *Server) mountSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent")
	skillID := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	var archived int
	var agentArchived int
	if err := s.store.db.QueryRow("SELECT archived FROM skills WHERE id=?", skillID).Scan(&archived); err != nil {
		fail(w, 404, "技能不存在。")
		return
	}
	if archived != 0 {
		fail(w, 409, "技能已归档，不能挂载。")
		return
	}
	if err := s.store.db.QueryRow("SELECT archived FROM agents WHERE id=?", agentID).Scan(&agentArchived); err != nil {
		fail(w, 404, "助手不存在。")
		return
	}
	if agentArchived != 0 {
		fail(w, 409, "助手已归档，不能修改授权。")
		return
	}
	bindings, err := s.store.bindings(agentID)
	if err != nil {
		internal(w)
		return
	}
	if len(bindings.Skills) >= 20 {
		fail(w, 400, "单个助手最多启用 20 个技能。")
		return
	}
	if err := s.mountSkills(agentID, []string{skillID}); err != nil {
		internal(w)
		return
	}
	s.audit("skill.mount", skillID, "agent="+agentID)
	respond(w, 200, map[string]bool{"mounted": true})
}

// mountOrAudit keeps an upload successful when the follow-up mount fails. The
// skill is already stored at that point, so failing the request would report a
// loss that did not happen; the audit entry preserves the operator signal.
func (s *Server) mountOrAudit(agentID string, skillIDs []string) {
	if err := s.mountSkills(agentID, skillIDs); err != nil {
		s.audit("skill.mount_failed", agentID, err.Error())
	}
}
