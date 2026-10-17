// Package wecomapp implements the bundled WeCom application channel plugin.
package wecomapp

import (
	"context"
	"errors"
	"strconv"

	wecom "siagent/internal/channelplugins/wecomapp/protocol"
	"siagent/internal/channels"
)

func New() channels.Provider { return &Provider{Client: wecom.NewClient()} }

type Provider struct{ Client *wecom.Client }

func (p *Provider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "wecom-app", Name: "企业微信 · 自建应用", Description: "兼容原有企业内部应用。需要公网 HTTPS 回调及应用凭据，与智能机器人不同。", Documentation: "https://developer.work.weixin.qq.com/document/path/90239", Transport: "webhook", Legacy: true, MaxTextBytes: 1800, IdentityFields: []string{"corp_id", "app_id"}, Fields: []channels.Field{
		{Key: "corp_id", Label: "企业 CorpID", Required: true}, {Key: "app_id", Label: "应用 AgentID", Required: true, Type: "number"}, {Key: "secret", Label: "应用 Secret", Secret: true, Required: true}, {Key: "token", Label: "回调 Token", Secret: true, Required: true}, {Key: "aes_key", Label: "EncodingAESKey", Secret: true, Required: true},
	}}
}
func (p *Provider) Validate(c channels.Config) error {
	app, err := strconv.ParseInt(c.Values["app_id"], 10, 64)
	if err != nil || app < 1 || app > 2147483647 || len(c.Values["corp_id"]) > 128 {
		return errors.New("请填写有效的 CorpID 和应用 AgentID。")
	}
	_, err = wecom.NewCryptor(c.Secrets["token"], c.Secrets["aes_key"], c.Values["corp_id"])
	if err != nil {
		return errors.New("回调 Token 或 EncodingAESKey 无效，AES Key 须为 43 位 Base64 字符。")
	}
	return nil
}
func (p *Provider) CallbackPath(id string) string { return "/callbacks/wecom/" + id }
func (p *Provider) Test(ctx context.Context, c channels.Config) channels.Result {
	app, _ := strconv.ParseInt(c.Values["app_id"], 10, 64)
	r := p.Client.Test(ctx, wecom.Application{ID: c.ID, CorpID: c.Values["corp_id"], AppID: app, Secret: c.Secrets["secret"]})
	return channels.Result{State: r.State, Code: r.Code, Message: r.Message}
}
func (p *Provider) Open(_ context.Context, c channels.Config, _ channels.Receive) (channels.Connection, error) {
	app, _ := strconv.ParseInt(c.Values["app_id"], 10, 64)
	return &connection{p.Client, wecom.Application{ID: c.ID, CorpID: c.Values["corp_id"], AppID: app, Secret: c.Secrets["secret"]}}, nil
}

type connection struct {
	client *wecom.Client
	app    wecom.Application
}

func (c *connection) Send(ctx context.Context, d channels.Delivery) channels.Result {
	r := c.client.Send(ctx, c.app, d.Target, d.Text)
	return channels.Result{State: r.State, Code: r.Code, Message: r.Message}
}
func (c *connection) Status() channels.Status {
	return channels.Status{State: "webhook", Message: "使用签名加密回调接收消息。"}
}
func (c *connection) Close() {}
