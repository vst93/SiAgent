package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
	_ "time/tzdata"
)

type SessionPolicy struct {
	Types       string `json:"types"`
	GroupScope  string `json:"group_scope"`
	IdleMinutes int    `json:"idle_minutes"`
	AllowReset  bool   `json:"allow_reset"`
}
type ContextPolicy struct {
	Strategy         string `json:"strategy"`
	MaxTurns         int    `json:"max_turns"`
	MaxAgeMinutes    int    `json:"max_age_minutes"`
	SummaryThreshold int    `json:"summary_threshold"`
	KeepRecentTokens int    `json:"keep_recent_tokens"`
}
type OutputPolicy struct {
	Thinking bool   `json:"thinking"`
	Tools    string `json:"tools"`
	Sources  bool   `json:"sources"`
	Progress bool   `json:"progress"`
	// ThinkTags decides what happens to reasoning that some models inline in the
	// answer text as <think>/<thinking>/<reasoning> tags. "strip" removes it from
	// the answer and, when Thinking is on, keeps it as the reasoning summary;
	// "keep" leaves the model text untouched.
	ThinkTags string `json:"think_tags"`
	// ThinkingMaxChars bounds the reasoning summary returned with channel replies
	// so it cannot consume the vendor's per-message byte limit.
	ThinkingMaxChars int `json:"thinking_max_chars"`
	// Acknowledge sends an immediate channel notice, using the same stream
	// message as the final answer when the adapter supports in-place updates.
	Acknowledge bool `json:"acknowledge"`
	// Stream publishes evolving channel drafts while the model is answering.
	Stream bool `json:"stream"`
}
type AgentPolicy struct {
	// AdvancedTools enables read/write/edit/bash inside the Agent workspace.
	// It is off by default and cannot be enabled when the deployment disables it.
	AdvancedTools bool          `json:"advanced_tools"`
	Timezone      string        `json:"timezone"`
	Session       SessionPolicy `json:"session"`
	Context       ContextPolicy `json:"context"`
	Output        OutputPolicy  `json:"output"`
}

func defaultAgentPolicy() AgentPolicy {
	return AgentPolicy{
		Timezone: "Asia/Shanghai",
		Session:  SessionPolicy{Types: "all", GroupScope: "member", AllowReset: true},
		Context:  ContextPolicy{Strategy: "recent", MaxTurns: 20, MaxAgeMinutes: 1440, SummaryThreshold: 70, KeepRecentTokens: 4096},
		Output: OutputPolicy{
			Tools:            "summary",
			Sources:          true,
			Progress:         true,
			ThinkTags:        "strip",
			ThinkingMaxChars: 1200,
			Acknowledge:      true,
			Stream:           true,
		},
	}
}
func parseAgentPolicy(raw []byte, base AgentPolicy) (AgentPolicy, error) {
	if len(raw) > 0 {
		if string(raw) == "null" {
			return base, errors.New("Agent 策略不能为 null。")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&base); err != nil {
			return base, errors.New("Agent 策略格式无效。")
		}
	}
	if len(base.Timezone) > 128 || base.Timezone == "" || base.Timezone == "Local" {
		return base, errors.New("请填写有效的 IANA 时区，例如 Asia/Shanghai 或 UTC。")
	}
	if _, err := time.LoadLocation(base.Timezone); err != nil {
		return base, errors.New("时区无效，请使用 Asia/Shanghai、America/New_York 或 UTC 等 IANA 时区名称。")
	}
	if base.Session.Types != "all" && base.Session.Types != "private" && base.Session.Types != "group" {
		return base, errors.New("请选择私聊、群聊或两者都允许。")
	}
	if base.Session.GroupScope != "member" && base.Session.GroupScope != "shared" {
		return base, errors.New("群聊上下文范围无效。")
	}
	if base.Session.IdleMinutes < 0 || base.Session.IdleMinutes > 10080 {
		return base, errors.New("会话空闲重置须为 0–10080 分钟，0 表示不自动重置。")
	}
	c := base.Context
	if c.Strategy != "full" && c.Strategy != "recent" && c.Strategy != "time" && c.Strategy != "summary" {
		return base, errors.New("上下文策略无效。")
	}
	if c.MaxTurns < 1 || c.MaxTurns > 200 || c.MaxAgeMinutes < 1 || c.MaxAgeMinutes > 43200 || c.SummaryThreshold < 50 || c.SummaryThreshold > 90 || c.KeepRecentTokens < 128 || c.KeepRecentTokens > 32768 {
		return base, errors.New("上下文须保留 1–200 轮，时间窗口 1–43200 分钟，摘要阈值 50–90%，近期原文 128–32768 Token。")
	}
	if base.Output.Tools != "hidden" && base.Output.Tools != "summary" && base.Output.Tools != "detailed" {
		return base, errors.New("工具输出方式无效。")
	}
	if base.Output.ThinkTags != "strip" && base.Output.ThinkTags != "keep" {
		return base, errors.New("思考标签兼容方式无效，请选择剔除或保留。")
	}
	if base.Output.ThinkingMaxChars < 200 || base.Output.ThinkingMaxChars > 8000 {
		return base, errors.New("渠道思考摘要须为 200–8000 字符。")
	}
	return base, nil
}

// Old queued runs keep their original full-history behavior.
func (a Agent) effectivePolicy() AgentPolicy {
	p := a.Policy
	if p.Context.Strategy == "" {
		p = defaultAgentPolicy()
		p.Context.Strategy = "full"
	}
	// Snapshots written before the reasoning/channel compatibility fields existed
	// carry their zero value; treat it as the documented default.
	if p.Output.ThinkTags == "" {
		p.Output.ThinkTags = "strip"
	}
	if p.Output.ThinkingMaxChars == 0 {
		p.Output.ThinkingMaxChars = 1200
	}
	return p
}
func (s *Store) migrateAgentPolicies() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("ALTER TABLE agents ADD COLUMN policy TEXT NOT NULL DEFAULT '{}'; ALTER TABLE runs ADD COLUMN thinking TEXT NOT NULL DEFAULT ''; PRAGMA user_version=10;"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.migrateKnowledgeQuality()
}
