package platform

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func validateModel(m Model) bool {
	parsed, err := url.Parse(m.BaseURL)
	return err == nil && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Scheme == "http" || parsed.Scheme == "https") && len(m.BaseURL) < 2048 && strings.TrimSpace(m.Name) != "" && len(m.Name) <= 200 && len(m.APIKey) > 0 && len(m.APIKey) <= 8192 && !strings.ContainsAny(m.APIKey, "\r\n")
}
func (s *Server) getModel(w http.ResponseWriter, r *http.Request) {
	model, err := s.store.model()
	if err == sql.ErrNoRows {
		respond(w, 200, nil)
		return
	}
	if err != nil {
		internal(w)
		return
	}
	model.APIKey = ""
	respond(w, 200, model)
}
func (s *Server) saveModel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		BaseURL  string          `json:"base_url"`
		Name     string          `json:"name"`
		APIKey   string          `json:"api_key"`
		Provider string          `json:"provider"`
		Options  json.RawMessage `json:"options"`
	}
	if !decode(w, r, &input) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	modelID, ok := requestedModel(w, r)
	if !ok {
		return
	}
	old, err := s.store.modelByID(modelID)
	if err != nil && err != sql.ErrNoRows {
		internal(w)
		return
	}
	if old.Archived {
		fail(w, 409, "模型已归档，不能修改。")
		return
	}
	if err == sql.ErrNoRows && modelID != 1 && r.Method != "POST" {
		fail(w, 404, "模型不存在。")
		return
	}
	model := Model{BaseURL: strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"), Name: strings.TrimSpace(input.Name), APIKey: input.APIKey}
	if model.APIKey == "" {
		model.APIKey = old.APIKey
	}
	options := old.Options
	if options.API == "" {
		options = defaultModelOptions()
	}
	model.Options, err = parseModelOptions(input.Options, options)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	model.Provider = strings.TrimSpace(input.Provider)
	if model.Provider == "" {
		// Legacy clients omit the provider. Keep the saved one when it still
		// matches the requested protocol; otherwise derive it from the protocol.
		model.Provider = old.Provider
		if model.Provider == "" || !providerAllowsAPI(model.Provider, model.Options.API) {
			model.Provider = resolveProvider("", model.Options.API)
		}
	}
	preset, known := presetByID(model.Provider)
	if !known {
		fail(w, 400, "请选择支持的模型供应商。")
		return
	}
	if !providerAllowsAPI(preset.ID, model.Options.API) {
		fail(w, 400, "供应商与接口协议不匹配，请重新选择。")
		return
	}
	if preset.Pack != "" && !s.store.providerAvailable(preset.ID) {
		fail(w, 409, "该供应商来自未加载的扩展，请先在“模型连接”中安装供应商扩展。")
		return
	}
	rawOptions, _ := json.Marshal(model.Options)
	if !validateModel(model) {
		fail(w, 400, "请填写有效的 HTTP(S) 服务地址、模型名称和密钥。使用无密钥的本地服务时，可填入该服务接受的占位密钥。")
		return
	}
	tx, err := s.store.db.Begin()
	if err != nil {
		internal(w)
		return
	}
	defer tx.Rollback()
	if r.Method == "POST" {
		var result sql.Result
		result, err = tx.Exec("INSERT INTO models(base_url,name,api_key,version,options,provider) VALUES(?,?,?,1,?,?)", model.BaseURL, model.Name, s.store.encrypt(model.APIKey), string(rawOptions), model.Provider)
		if err == nil {
			modelID, err = result.LastInsertId()
		}
	} else {
		_, err = tx.Exec("INSERT INTO models(id,base_url,name,api_key,version,options,provider) VALUES(?,?,?,?,1,?,?) ON CONFLICT(id) DO UPDATE SET base_url=excluded.base_url,name=excluded.name,api_key=excluded.api_key,version=models.version+1,options=excluded.options,provider=excluded.provider", modelID, model.BaseURL, model.Name, s.store.encrypt(model.APIKey), string(rawOptions), model.Provider)
	}
	if err == nil {
		_, err = tx.Exec("INSERT OR IGNORE INTO agents(id,name,instructions,enabled) VALUES('default','默认助手','你是一位简洁、可靠的助手。请使用用户的语言回答。',1)")
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		internal(w)
		return
	}
	model.ID = modelID
	model.APIKey = ""
	model.HasKey = true
	model.Version = old.Version + 1
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	respond(w, status, model)
}
func (s *Server) testModel(w http.ResponseWriter, r *http.Request) {
	modelID, ok := requestedModel(w, r)
	if !ok {
		return
	}
	model, err := s.store.modelByID(modelID)
	if err == sql.ErrNoRows {
		fail(w, 409, "请先保存模型配置。")
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if model.Archived {
		fail(w, 409, "模型已归档，不能测试连接。")
		return
	}
	payload := map[string]any{"model": model.Name, "messages": []map[string]string{{"role": "user", "content": "Reply with OK."}}, "stream": false}
	endpoint := "/chat/completions"
	field := "max_completion_tokens"
	for _, host := range []string{"chutes.ai", "deepseek.com", "api.moonshot.", "gateway.ai.cloudflare.com", "api.together.ai", "api.together.xyz", "integrate.api.nvidia.com", "api.ant-ling.com", "api.z.ai", "open.bigmodel.cn"} {
		if strings.Contains(model.BaseURL, host) {
			field = "max_tokens"
			break
		}
	}
	if model.Options.MaxTokensField != "" {
		field = model.Options.MaxTokensField
	}
	payload[field] = 16
	if model.Options.API == "openai-responses" {
		endpoint = "/responses"
		payload = map[string]any{"model": model.Name, "input": "Reply with OK.", "max_output_tokens": 16, "stream": false}
	}
	if model.Options.API == "anthropic-messages" {
		delete(payload, field)
		payload["max_tokens"] = 16
		endpoint = "/v1/messages"
		if strings.HasSuffix(model.BaseURL, "/v1") {
			endpoint = "/messages"
		}
	}
	body, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, model.BaseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		fail(w, 400, "服务地址无法使用，请检查地址。")
		return
	}
	if model.Options.API == "anthropic-messages" {
		request.Header.Set("x-api-key", model.APIKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	} else {
		request.Header.Set("Authorization", "Bearer "+model.APIKey)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		fail(w, 502, "无法连接模型服务，请检查地址、网络和证书后重试。")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := "模型服务未接受请求，请检查服务地址和模型名称。"
		switch response.StatusCode {
		case 401, 403:
			message = "模型密钥被拒绝，请更新密钥并确认该密钥有权访问此模型。"
		case 404:
			message = "模型接口不存在，请检查服务地址是否包含正确的路径前缀，以及模型名称。"
		case 429:
			message = "模型服务限制请求或余额不足，请检查额度后稍后重试。"
		}
		fail(w, 502, message)
		return
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Content []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		} `json:"content"`
		Output []struct {
			Content []struct {
				Type string  `json:"type"`
				Text *string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	valid := false
	if readErr == nil && len(raw) <= 1<<20 && json.Unmarshal(raw, &result) == nil {
		switch model.Options.API {
		case "openai-completions":
			valid = len(result.Choices) > 0 && result.Choices[0].Message.Content != nil
		case "anthropic-messages":
			for _, part := range result.Content {
				if part.Type == "text" && part.Text != nil {
					valid = true
				}
			}
		case "openai-responses":
			for _, output := range result.Output {
				for _, part := range output.Content {
					if part.Type == "output_text" && part.Text != nil {
						valid = true
					}
				}
			}
		}
	}
	if !valid {
		fail(w, 502, "模型服务返回了无法识别的文本响应，请检查所选协议及服务地址。")
		return
	}

	respond(w, 200, map[string]string{"message": "连接成功，模型已返回文本响应。"})
}

func requestedModel(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if r.Method == "POST" && r.URL.Path == "/api/models" {
		return 0, true
	}
	raw := r.PathValue("model")
	if raw == "" {
		return 1, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		fail(w, 400, "模型标识无效。")
		return 0, false
	}
	return value, true
}
func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id,base_url,name,version,archived,options,provider FROM models ORDER BY id")
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()
	models := []Model{}
	for rows.Next() {
		var m Model
		var options string
		if err := rows.Scan(&m.ID, &m.BaseURL, &m.Name, &m.Version, &m.Archived, &options, &m.Provider); err != nil {
			internal(w)
			return
		}
		m.Options, err = parseModelOptions([]byte(options), defaultModelOptions())
		if err != nil {
			internal(w)
			return
		}
		m.HasKey = true
		m.Provider = resolveProvider(m.Provider, m.Options.API)
		models = append(models, m)
	}
	if rows.Err() != nil {
		internal(w)
		return
	}
	respond(w, 200, models)
}
func (s *Server) archiveModel(w http.ResponseWriter, r *http.Request) {
	modelID, ok := requestedModel(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if modelID == 1 {
		fail(w, 409, "默认模型保留用于首次使用；可修改其配置。")
		return
	}
	var references int
	if err := s.store.db.QueryRow("SELECT count(*) FROM agents WHERE model_id=? AND archived=0", modelID).Scan(&references); err != nil {
		internal(w)
		return
	}
	if references > 0 {
		fail(w, 409, "请先为关联助手选择其他模型或归档助手。")
		return
	}
	result, err := s.store.db.Exec("UPDATE models SET archived=1 WHERE id=?", modelID)
	if err != nil {
		internal(w)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		fail(w, 404, "模型不存在。")
		return
	}
	respond(w, 200, map[string]bool{"archived": true})
}
