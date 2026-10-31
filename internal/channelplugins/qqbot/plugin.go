// Package qqbot implements the bundled QQ official bot channel plugin. It wraps
// the QQ Open Platform Bot API v2 (WebSocket gateway + HTTP sends) in a Node
// process worker; no public callback is involved.
package qqbot

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"siagent/internal/channels"
)

// QQ bot AppIDs are numeric; the developer console shows them as decimal IDs.
var appIDPattern = regexp.MustCompile(`^[0-9]{4,20}$`)

func New(node, workerScript string) (channels.Provider, error) {
	script, err := filepath.Abs(workerScript)
	if err != nil {
		return nil, err
	}
	return &Provider{ProcessProvider: &channels.ProcessProvider{Spec: descriptor(), Node: node, Script: script}}, nil
}

type Provider struct{ *channels.ProcessProvider }

func (p *Provider) Validate(c channels.Config) error {
	if _, err := channels.ValidateFields(p.Spec, c); err != nil {
		return err
	}
	if !appIDPattern.MatchString(c.Values["app_id"]) {
		return errors.New("请填写有效的 QQ 机器人 AppID（数字）。")
	}
	switch strings.TrimSpace(c.Values["environment"]) {
	case "", "production", "sandbox":
	default:
		return errors.New("环境仅支持留空、production 或 sandbox。")
	}
	return nil
}

func descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "qqbot", Name: "QQ · 官方机器人", Description: "QQ 开放平台机器人 API v2（WebSocket 网关），仅需 AppID 与 AppSecret，无需公网回调；群聊仅在 @ 机器人时触发。", Documentation: "https://bot.q.qq.com/wiki/", Transport: "websocket", MaxTextBytes: 2000, Images: true, Priority: 70, IdentityFields: []string{"app_id"}, Fields: []channels.Field{
		{Key: "app_id", Label: "AppID", Required: true, Help: "QQ 开放平台“开发设置”中的机器人 AppID。"},
		{Key: "app_secret", Label: "AppSecret", Secret: true, Required: true},
		{Key: "environment", Label: "环境", Help: "留空或 production 使用正式环境；sandbox 使用沙箱环境。"},
	}}
}
