package platform

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Agent struct {
	Policy                 AgentPolicy `json:"policy"`
	ID                     string      `json:"id"`
	AdvancedToolsAvailable bool        `json:"advanced_tools_available"`
	Name                   string      `json:"name"`
	Instructions           string      `json:"instructions"`
	Enabled                bool        `json:"enabled"`
	FallbackModelIDs       []int64     `json:"fallback_model_ids"`
	ModelID                int64       `json:"model_id"`
	Revision               int         `json:"revision"`
	Archived               bool        `json:"archived"`
	// EnvKeys names the configured system environment variables. Values are
	// encrypted, never returned by the API and never written to run snapshots.
	EnvKeys []string `json:"env_keys"`
}

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// agentEnvironment validates and merges an Agent's system variables. A blank
// value keeps the stored secret so the administrator never has to re-enter it.
func agentEnvironment(stored, update map[string]string, remove []string) (map[string]string, error) {
	result := map[string]string{}
	for key, value := range stored {
		result[key] = value
	}
	if len(update) > 32 || len(remove) > 32 {
		return nil, errors.New("每个助手最多配置 32 个系统环境变量。")
	}
	total := 0
	for key, value := range update {
		if !envKeyPattern.MatchString(key) {
			return nil, errors.New("环境变量名须以字母或下划线开头，只使用字母、数字和下划线，最长 64 个字符。")
		}
		if value == "" {
			continue
		}
		if len(value) > 8192 || strings.ContainsRune(value, 0) {
			return nil, errors.New("环境变量值不能包含空字符，且不能超过 8192 字节。")
		}
		result[key] = value
	}
	for _, key := range remove {
		delete(result, key)
	}
	if len(result) > 32 {
		return nil, errors.New("每个助手最多配置 32 个系统环境变量。")
	}
	for key, value := range result {
		if !envKeyPattern.MatchString(key) || len(value) > 8192 {
			return nil, errors.New("已保存的系统环境变量无效，请重新填写。")
		}
		total += len(key) + len(value)
	}
	if total > 64*1024 {
		return nil, errors.New("系统环境变量合计不能超过 64 KiB。")
	}
	return result, nil
}

func envKeysFor(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) agentEnvironment(id string) (map[string]string, error) {
	var encrypted string
	if err := s.db.QueryRow("SELECT environment FROM agents WHERE id=?", id).Scan(&encrypted); err != nil {
		return nil, err
	}
	return s.openAgentEnvironment(encrypted)
}

func (s *Store) openAgentEnvironment(encrypted string) (map[string]string, error) {
	environment := map[string]string{}
	if encrypted == "" {
		return environment, nil
	}
	raw, err := s.decrypt(encrypted)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return environment, nil
	}
	if err := json.Unmarshal([]byte(raw), &environment); err != nil {
		return nil, err
	}
	if environment == nil {
		environment = map[string]string{}
	}
	return environment, nil
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
	var policy, fallbacks, environment string
	err := s.store.db.QueryRow("SELECT id,name,instructions,enabled,model_id,archived,policy,revision,fallback_model_ids,environment FROM agents WHERE id=?", id).Scan(&a.ID, &a.Name, &a.Instructions, &a.Enabled, &a.ModelID, &a.Archived, &policy, &a.Revision, &fallbacks, &environment)
	if err == nil {
		a.Policy, err = parseAgentPolicy([]byte(policy), defaultAgentPolicy())
	}
	if err == nil {
		if fallbacks != "" {
			err = json.Unmarshal([]byte(fallbacks), &a.FallbackModelIDs)
		}
		if a.FallbackModelIDs == nil {
			a.FallbackModelIDs = []int64{}
		}
	}
	if err == nil {
		var values map[string]string
		values, err = s.store.openAgentEnvironment(environment)
		if err == nil {
			a.EnvKeys = envKeysFor(values)
		}
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
	rows, err := s.store.db.Query("SELECT id,name,instructions,enabled,model_id,archived,policy,revision,fallback_model_ids,environment FROM agents ORDER BY CASE WHEN id='default' THEN 0 ELSE 1 END,rowid")
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	values := []Agent{}
	for rows.Next() {
		var a Agent
		var policy, fallbacks, environment string
		if err := rows.Scan(&a.ID, &a.Name, &a.Instructions, &a.Enabled, &a.ModelID, &a.Archived, &policy, &a.Revision, &fallbacks, &environment); err != nil {
			internal(w)
			return
		}
		a.Policy, err = parseAgentPolicy([]byte(policy), defaultAgentPolicy())
		if err != nil {
			internal(w)
			return
		}
		if fallbacks != "" {
			if err := json.Unmarshal([]byte(fallbacks), &a.FallbackModelIDs); err != nil {
				internal(w)
				return
			}
		}
		if a.FallbackModelIDs == nil {
			a.FallbackModelIDs = []int64{}
		}
		storedValues, err := s.store.openAgentEnvironment(environment)
		if err != nil {
			internal(w)
			return
		}
		a.EnvKeys = envKeysFor(storedValues)
		a.AdvancedToolsAvailable = !s.advancedToolsDisabled
		values = append(values, a)
	}
	if rows.Err() != nil {
		internal(w)
		return
	}
	respond(w, 200, values)
}
func (s *Server) validateFallbackModels(primary int64, ids []int64) ([]int64, error) {
	if len(ids) > 5 {
		return nil, fmt.Errorf("兜底模型最多配置 5 个。")
	}
	result := make([]int64, 0, len(ids))
	seen := map[int64]bool{primary: true}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		model, err := s.store.modelByID(id)
		if err == sql.ErrNoRows || model.Archived {
			return nil, fmt.Errorf("兜底模型必须存在且未归档。")
		}
		if err != nil {
			return nil, err
		}
		seen[id] = true
		result = append(result, id)
	}
	return result, nil
}

func (s *Server) saveAgent(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Policy           json.RawMessage   `json:"policy"`
		Env              map[string]string `json:"env"`
		Name             string            `json:"name"`
		Instructions     string            `json:"instructions"`
		EnvRemove        []string          `json:"env_remove"`
		Enabled          bool              `json:"enabled"`
		FallbackModelIDs []int64           `json:"fallback_model_ids"`
		ModelID          int64             `json:"model_id"`
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
	var old Agent
	status := 200
	if r.Method == "POST" {
		agentID = id()
		status = 201
		if input.ModelID == 0 {
			input.ModelID = 1
		}
	} else {
		var err error
		old, err = s.agentByID(agentID)
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
	storedEnv := map[string]string{}
	if r.Method != "POST" {
		storedEnv, err = s.store.agentEnvironment(agentID)
		if err != nil {
			internal(w)
			return
		}
	}
	environment, err := agentEnvironment(storedEnv, input.Env, input.EnvRemove)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	encodedEnvironment := ""
	if len(environment) > 0 {
		raw, _ := json.Marshal(environment)
		encodedEnvironment = s.store.encrypt(string(raw))
	}
	model, err := s.store.modelByID(input.ModelID)
	if err == sql.ErrNoRows || model.Archived {
		fail(w, 400, "请选择存在且未归档的模型。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if r.Method == "PUT" && input.FallbackModelIDs == nil {
		input.FallbackModelIDs = old.FallbackModelIDs
	}
	fallbacks, err := s.validateFallbackModels(input.ModelID, input.FallbackModelIDs)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	encodedFallbacks, _ := json.Marshal(fallbacks)
	if r.Method == "POST" {
		_, err = s.store.db.Exec("INSERT INTO agents(id,name,instructions,enabled,model_id,policy,fallback_model_ids,environment) VALUES(?,?,?,?,?,?,?,?)", agentID, input.Name, input.Instructions, input.Enabled, input.ModelID, string(encodedPolicy), string(encodedFallbacks), encodedEnvironment)
	} else {
		_, err = s.store.db.Exec("UPDATE agents SET name=?,instructions=?,enabled=?,model_id=?,policy=?,fallback_model_ids=?,environment=?,revision=revision+1 WHERE id=?", input.Name, input.Instructions, input.Enabled, input.ModelID, string(encodedPolicy), string(encodedFallbacks), encodedEnvironment, agentID)
	}
	if err != nil {
		slog.Error("save agent failed", "error", err)
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
	// Only variable names are audited; values stay encrypted.
	if strings.Join(envKeysFor(environment), ",") != strings.Join(envKeysFor(storedEnv), ",") {
		s.audit("agent.environment", agentID, strings.Join(envKeysFor(environment), ","))
	}
	a, err := s.agentByID(agentID)
	if err != nil {
		internal(w)
		return
	}
	a.AdvancedToolsAvailable = !s.advancedToolsDisabled
	respond(w, status, a)
}

// confirmPassword re-verifies the administrator password and opens a bounded
// window for reading sensitive values. The session cookie alone is not enough:
// an unattended browser must not expose stored secrets.
func (s *Server) confirmPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	s.mu.Lock()
	if time.Since(s.loginWindow) > 15*time.Minute {
		s.loginFailures = 0
		s.loginWindow = time.Now()
	}
	if s.loginFailures >= 10 {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "900")
		fail(w, 429, "密码错误次数过多，请在 15 分钟后重试。")
		return
	}
	var hash []byte
	err := s.store.db.QueryRow("SELECT password_hash FROM admin WHERE id=1").Scan(&hash)
	s.mu.Unlock()
	if err != nil {
		internal(w)
		return
	}
	if len(input.Password) > 72 || bcrypt.CompareHashAndPassword(hash, []byte(input.Password)) != nil {
		s.mu.Lock()
		s.loginFailures++
		s.mu.Unlock()
		// 400, not 401: the browser treats 401 as an expired login and signs out.
		fail(w, 400, "密码不正确，无法查看已保存的敏感值。")
		return
	}
	s.mu.Lock()
	s.loginFailures = 0
	s.mu.Unlock()
	cookie, err := r.Cookie("siagent_session")
	if err != nil {
		fail(w, 401, "请先登录管理员账户。")
		return
	}
	until := time.Now().Add(revealWindow).Unix()
	if _, err := s.store.db.Exec("UPDATE sessions SET reveal_until=? WHERE token_hash=?", until, hashToken(cookie.Value)); err != nil {
		internal(w)
		return
	}
	s.audit("sensitive.reveal.open", "session", "")
	respond(w, 200, map[string]any{"reveal_until": until, "seconds": int(revealWindow.Seconds())})
}

// sensitiveRevealOpen reports whether the current session may read stored
// sensitive values, and when that window closes.
func (s *Server) sensitiveRevealOpen(r *http.Request) (int64, bool) {
	cookie, err := r.Cookie("siagent_session")
	if err != nil {
		return 0, false
	}
	var until int64
	if err := s.store.db.QueryRow("SELECT reveal_until FROM sessions WHERE token_hash=? AND expires>?", hashToken(cookie.Value), time.Now().Unix()).Scan(&until); err != nil {
		return 0, false
	}
	if until <= time.Now().Unix() {
		return 0, false
	}
	return until, true
}

// revealAgentEnvironment returns the Agent's stored variable values. It is a
// separate, audited endpoint so editing the Agent never returns secrets, and it
// requires the short reveal window opened by confirmPassword.
func (s *Server) revealAgentEnvironment(w http.ResponseWriter, r *http.Request) {
	until, open := s.sensitiveRevealOpen(r)
	if !open {
		fail(w, 403, "查看已保存的敏感值需要重新验证管理员密码，且仅在 5 分钟内有效。")
		return
	}
	a, err := s.agentByID(agentPath(r))
	if err == sql.ErrNoRows {
		fail(w, 404, "助手不存在。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	environment, err := s.store.agentEnvironment(a.ID)
	if err != nil {
		internal(w)
		return
	}
	s.audit("agent.environment.reveal", a.ID, strings.Join(envKeysFor(environment), ","))
	respond(w, 200, map[string]any{"env": environment, "reveal_until": until})
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
