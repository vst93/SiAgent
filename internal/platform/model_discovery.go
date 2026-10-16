package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// discoverModels probes the current editor values without changing a saved connection.
func (s *Server) discoverModels(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ModelID int64  `json:"model_id"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		API     string `json:"api"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ModelID < 0 {
		fail(w, 400, "模型连接无效。")
		return
	}
	if input.ModelID > 0 {
		saved, err := s.store.modelByID(input.ModelID)
		if err == sql.ErrNoRows {
			fail(w, 404, "模型连接不存在。")
			return
		}
		if err != nil {
			internal(w)
			return
		}
		if saved.Archived {
			fail(w, 409, "模型已归档，不能获取模型列表。")
			return
		}
		if input.APIKey == "" {
			input.APIKey = saved.APIKey
		}
	}
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if !validateModel(Model{BaseURL: input.BaseURL, Name: "discovery", APIKey: input.APIKey}) {
		fail(w, 400, "请填写有效的服务地址与 API 密钥；无鉴权的本地服务可填写其接受的占位密钥。")
		return
	}
	switch input.API {
	case "", "openai-completions", "openai-responses", "anthropic-messages":
	default:
		fail(w, 400, "不支持此接口协议。")
		return
	}
	endpoint := input.BaseURL + "/models"
	if input.API == "anthropic-messages" && !strings.HasSuffix(input.BaseURL, "/v1") {
		endpoint = input.BaseURL + "/v1/models"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		fail(w, 400, "服务地址无法使用，请检查地址。")
		return
	}
	request.Header.Set("Accept", "application/json")
	if input.API == "anthropic-messages" {
		request.Header.Set("x-api-key", input.APIKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	} else {
		request.Header.Set("Authorization", "Bearer "+input.APIKey)
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		fail(w, 502, "无法获取模型列表，请检查服务地址、网络和证书后重试。")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := "模型服务未返回列表，请检查服务地址或稍后重试。"
		switch response.StatusCode {
		case 401, 403:
			message = "模型密钥被拒绝，请检查密钥及模型列表访问权限。"
		case 404, 405:
			message = "当前服务不支持此模型列表接口，可继续手动填写模型 ID。"
		case 429:
			message = "模型服务限制请求，请稍后重新获取。"
		}
		fail(w, 502, message)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err != nil || len(body) > 2<<20 || json.Unmarshal(body, &result) != nil || result.Data == nil {
		fail(w, 502, "模型列表响应格式无效或过大，可继续手动填写模型 ID。")
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, model := range result.Data {
		name := strings.TrimSpace(model.ID)
		if name != "" && len(name) <= 200 && !strings.ContainsAny(name, "\r\n\x00") && !seen[name] {
			ids = append(ids, name)
			seen[name] = true
		}
	}
	sort.Strings(ids)
	w.Header().Set("Cache-Control", "no-store")
	respond(w, 200, map[string]any{"models": ids})
}
