package platform

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// ModelOptions is frozen with each admitted run, independently of credentials.
type ModelOptions struct {
	DisplayName    string   `json:"display_name"`
	API            string   `json:"api"`
	ContextWindow  int      `json:"context_window"`
	MaxTokens      int      `json:"max_tokens"`
	Reasoning      bool     `json:"reasoning"`
	ThinkingLevel  string   `json:"thinking_level"`
	Temperature    *float64 `json:"temperature"`
	TopP           *float64 `json:"top_p"`
	MaxTokensField string   `json:"max_tokens_field"`
}

func defaultModelOptions() ModelOptions {
	return ModelOptions{API: "openai-completions", ContextWindow: 32768, MaxTokens: 4096, ThinkingLevel: "off"}
}
func parseModelOptions(raw []byte, base ModelOptions) (ModelOptions, error) {
	if len(raw) > 0 {
		if string(raw) == "null" {
			return base, errors.New("模型参数不能为 null。")
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&base); err != nil {
			return base, errors.New("模型参数格式无效或包含未知配置项。")
		}
	}
	base.DisplayName = strings.TrimSpace(base.DisplayName)
	if utf8.RuneCountInString(base.DisplayName) > 100 || (base.API != "openai-completions" && base.API != "openai-responses" && base.API != "anthropic-messages") {
		return base, errors.New("请选择支持的接口协议，连接名称不能超过 100 个字符。")
	}
	if base.ContextWindow < 256 || base.ContextWindow > 10000000 || base.MaxTokens < 16 || base.MaxTokens >= base.ContextWindow {
		return base, errors.New("上下文窗口须为 256–10000000 Token，最大输出须至少 16 Token 且小于上下文窗口。")
	}
	if base.ThinkingLevel != "off" && base.ThinkingLevel != "minimal" && base.ThinkingLevel != "low" && base.ThinkingLevel != "medium" && base.ThinkingLevel != "high" {
		return base, errors.New("推理强度无效。")
	}
	if !base.Reasoning && base.ThinkingLevel != "off" {
		return base, errors.New("请先启用推理能力，再选择推理强度。")
	}
	if base.Temperature != nil && (*base.Temperature < 0 || *base.Temperature > 2) {
		return base, errors.New("Temperature 须在 0–2 之间。")
	}
	if base.TopP != nil && (*base.TopP <= 0 || *base.TopP > 1) {
		return base, errors.New("Top P 须大于 0 且不超过 1。")
	}
	if base.API == "anthropic-messages" && (base.TopP != nil || (base.Temperature != nil && (*base.Temperature > 1 || base.ThinkingLevel != "off"))) {
		return base, errors.New("Anthropic 不使用此处的 Top P；Temperature 须为 0–1，启用推理时请留空。")
	}
	if base.API == "anthropic-messages" && base.ThinkingLevel != "off" && base.MaxTokens < 2048 {
		return base, errors.New("Anthropic 推理模式的最大输出须至少为 2048 Token，为推理和回答保留空间。")
	}
	if base.MaxTokensField != "" && base.MaxTokensField != "max_tokens" && base.MaxTokensField != "max_completion_tokens" {
		return base, errors.New("输出长度字段无效。")
	}
	if base.API != "openai-completions" && base.MaxTokensField != "" {
		return base, errors.New("输出长度字段覆盖仅适用于 Chat Completions。")
	}
	return base, nil
}
func (s *Store) migrateModelOptions() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("ALTER TABLE models ADD COLUMN options TEXT NOT NULL DEFAULT '{}'; PRAGMA user_version=8;")
	if err != nil {
		return err
	}
	return tx.Commit()
}
