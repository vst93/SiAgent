package platform

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

type Agent struct {
	Policy                 AgentPolicy `json:"policy"`
	ID                     string      `json:"id"`
	AdvancedToolsAvailable bool        `json:"advanced_tools_available"`
	Name                   string      `json:"name"`
	Instructions           string      `json:"instructions"`
	Enabled                bool        `json:"enabled"`
	ModelID                int64       `json:"model_id"`
	Archived               bool        `json:"archived"`
}

func agentPath(r *http.Request) string {
	if value := r.PathValue("agent"); value != "" {
		return value
	}
	return "default"
}
func (s *Server) agent() (Agent, error) { return s.agentByID("default") }
func (s *Server) agentByID(id string) (Agent, error) {
	var a Agent
	var policy string
	err := s.store.db.QueryRow("SELECT id,name,instructions,enabled,model_id,archived,policy FROM agents WHERE id=?", id).Scan(&a.ID, &a.Name, &a.Instructions, &a.Enabled, &a.ModelID, &a.Archived, &policy)
	if err == nil {
		a.Policy, err = parseAgentPolicy([]byte(policy), defaultAgentPolicy())
	}
	return a, err
}
func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.agentByID(agentPath(r))
	if err == sql.ErrNoRows {
		if r.PathValue("agent") == "" {
			respond(w, 200, nil)
		} else {
			fail(w, 404, "助手不存在。")
		}
		return
	}
	if err != nil {
		internal(w)
		return
	}
	a.AdvancedToolsAvailable = !s.advancedToolsDisabled
	respond(w, 200, a)
}
func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id,name,instructions,enabled,model_id,archived,policy FROM agents ORDER BY CASE WHEN id='default' THEN 0 ELSE 1 END,rowid")
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	values := []Agent{}
	for rows.Next() {
		var a Agent
		var policy string
		if err := rows.Scan(&a.ID, &a.Name, &a.Instructions, &a.Enabled, &a.ModelID, &a.Archived, &policy); err != nil {
			internal(w)
			return
		}
		a.Policy, err = parseAgentPolicy([]byte(policy), defaultAgentPolicy())
		if err != nil {
			internal(w)
			return
		}
		a.AdvancedToolsAvailable = !s.advancedToolsDisabled
		values = append(values, a)
	}
	if rows.Err() != nil {
		internal(w)
		return
	}
	respond(w, 200, values)
}
func (s *Server) saveAgent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Policy       json.RawMessage `json:"policy"`
		Name         string          `json:"name"`
		Instructions string          `json:"instructions"`
		Enabled      bool            `json:"enabled"`
		ModelID      int64           `json:"model_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 100 || len(input.Instructions) > 20000 {
		fail(w, 400, "助手名称须为 1–100 字节，角色指令不超过 20,000 字节。")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	agentID := agentPath(r)
	basePolicy := defaultAgentPolicy()
	status := 200
	if r.Method == "POST" {
		agentID = id()
		status = 201
		if input.ModelID == 0 {
			input.ModelID = 1
		}
	} else {
		old, err := s.agentByID(agentID)
		if err == sql.ErrNoRows {
			fail(w, 404, "助手不存在，请先配置模型或创建助手。")
			return
		}
		if err != nil {
			internal(w)
			return
		}
		if old.Archived {
			fail(w, 409, "助手已归档，仅可查看历史。")
			return
		}
		basePolicy = old.Policy
		if input.ModelID == 0 {
			input.ModelID = old.ModelID
		}
	}
	policy, err := parseAgentPolicy(input.Policy, basePolicy)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if policy.AdvancedTools && s.advancedToolsDisabled {
		fail(w, 409, "当前部署已禁用高级工具（SIAGENT_DISABLE_ADVANCED_TOOLS），不能为助手开启。")
		return
	}
	encodedPolicy, _ := json.Marshal(policy)
	model, err := s.store.modelByID(input.ModelID)
	if err == sql.ErrNoRows || model.Archived {
		fail(w, 400, "请选择存在且未归档的模型。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if r.Method == "POST" {
		_, err = s.store.db.Exec("INSERT INTO agents(id,name,instructions,enabled,model_id,policy) VALUES(?,?,?,?,?,?)", agentID, input.Name, input.Instructions, input.Enabled, input.ModelID, string(encodedPolicy))
	} else {
		_, err = s.store.db.Exec("UPDATE agents SET name=?,instructions=?,enabled=?,model_id=?,policy=? WHERE id=?", input.Name, input.Instructions, input.Enabled, input.ModelID, string(encodedPolicy), agentID)
	}
	if err != nil {
		internal(w)
		return
	}
	if policy.AdvancedTools != basePolicy.AdvancedTools {
		detail := "disabled"
		if policy.AdvancedTools {
			detail = "enabled"
		}
		s.audit("agent.advanced_tools."+detail, agentID, "")
	}
	a, err := s.agentByID(agentID)
	if err != nil {
		internal(w)
		return
	}
	a.AdvancedToolsAvailable = !s.advancedToolsDisabled
	respond(w, status, a)
}
func (s *Server) archiveAgent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agentID := agentPath(r)
	if agentID == "default" {
		fail(w, 409, "默认助手保留，可停用以停止接收新任务。")
		return
	}
	a, err := s.agentByID(agentID)
	if err == sql.ErrNoRows {
		fail(w, 404, "助手不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if a.Enabled {
		fail(w, 409, "请先停用助手，再归档。已有任务仍正常执行，必要时可单独取消。")
		return
	}
	var bindings int
	if err := s.store.db.QueryRow("SELECT count(*) FROM channels WHERE agent_id=? AND archived=0", agentID).Scan(&bindings); err != nil {
		internal(w)
		return
	}
	if bindings > 0 {
		fail(w, 409, "请先解除所有渠道绑定或归档渠道，再归档助手。")
		return
	}
	if _, err := s.store.db.Exec("UPDATE agents SET archived=1 WHERE id=?", agentID); err != nil {
		internal(w)
		return
	}
	respond(w, 200, map[string]bool{"archived": true})
}
func (s *Server) capabilityAgent(w http.ResponseWriter, r *http.Request, write bool) (Agent, bool) {
	a, err := s.agentByID(agentPath(r))
	if err == sql.ErrNoRows && !write && agentPath(r) == "default" {
		return Agent{ID: "default"}, true
	}
	if err == sql.ErrNoRows {
		fail(w, 409, "请先配置模型并创建助手。")
		return a, false
	}
	if err != nil {
		internal(w)
		return a, false
	}
	if write && a.Archived {
		fail(w, 409, "助手已归档，不能修改关联。")
		return a, false
	}
	return a, true
}
