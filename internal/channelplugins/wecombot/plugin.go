// Package wecombot implements the bundled WeCom bot channel plugin.
package wecombot

import (
	"path/filepath"
	"siagent/internal/channels"
)

func New(node, workerScript string) (channels.Provider, error) {
	script, err := filepath.Abs(workerScript)
	if err != nil {
		return nil, err
	}
	return &channels.ProcessProvider{Spec: descriptor(), Node: node, Script: script}, nil
}

func descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "wecom-bot", Name: "企业微信 · 智能机器人", Description: "推荐：API 长连接模式，仅需 BotID 与 Secret，无需公网回调地址。支持文本单聊和群内 @ 消息。", Documentation: "https://developer.work.weixin.qq.com/document/path/101463", Transport: "websocket", MaxTextBytes: 20000, IdentityFields: []string{"bot_id"}, Fields: []channels.Field{{Key: "bot_id", Label: "BotID", Required: true, Help: "在企微智能机器人中启用 API 模式并选择长连接。"}, {Key: "secret", Label: "机器人 Secret", Secret: true, Required: true, Help: "长连接专用密钥，不是应用 Secret 或回调 Token。"}}}
}
