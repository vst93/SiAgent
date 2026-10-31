// Package feishubot implements the bundled Feishu (Lark) bot channel plugin.
package feishubot

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"siagent/internal/channels"
)

// Feishu app IDs are `cli_` followed by 16 hexadecimal characters; the official
// SDK refuses anything else before it opens a connection, so the console
// reports it while the administrator is still editing.
var appIDPattern = regexp.MustCompile(`^cli_[0-9a-fA-F]{16}$`)

var supportedDomains = map[string]bool{
	"":                           true,
	"https://open.feishu.cn":     true,
	"https://open.larksuite.com": true,
}

func New(node, workerScript string) (channels.Provider, error) {
	script, err := filepath.Abs(workerScript)
	if err != nil {
		return nil, err
	}
	return &Provider{ProcessProvider: &channels.ProcessProvider{Spec: descriptor(), Node: node, Script: script}}, nil
}

// Provider adds console-time validation on top of the process transport.
type Provider struct{ *channels.ProcessProvider }

func (p *Provider) Validate(c channels.Config) error {
	if _, err := channels.ValidateFields(p.Spec, c); err != nil {
		return err
	}
	if !appIDPattern.MatchString(c.Values["app_id"]) {
		return errors.New("请填写有效的飞书 App ID（cli_ 开头，后接 16 位十六进制字符）。")
	}
	if !supportedDomains[strings.TrimSpace(c.Values["domain"])] {
		return errors.New("开放平台域名仅支持留空、https://open.feishu.cn 或 https://open.larksuite.com。")
	}
	return nil
}

func descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "feishu-bot", Name: "飞书 · 自建应用机器人", Description: "官方 SDK 长连接模式，仅需 App ID 与 App Secret，无需公网回调地址或内网穿透。支持文字、图片、图文混排及语音转写文本的单聊与群聊 @ 消息。", Documentation: "https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot", Transport: "websocket", MaxTextBytes: 20000, Streaming: true, Images: true, Priority: 90, IdentityFields: []string{"app_id"}, Fields: []channels.Field{
		{Key: "app_id", Label: "App ID", Required: true, Help: "开发者后台“凭证与基础信息”中的 App ID，形如 cli_ 开头的 16 位十六进制。"},
		{Key: "app_secret", Label: "App Secret", Secret: true, Required: true},
		{Key: "domain", Label: "开放平台域名", Help: "留空使用飞书 open.feishu.cn；Lark 国际版填写 https://open.larksuite.com。"},
	}}
}
