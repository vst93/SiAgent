package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"siagent/internal/channels"
	"siagent/internal/wecom"
)

type applicationProvider struct{ client *wecom.Client }

func (p *applicationProvider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "wecom-app", Name: "企业微信 · 自建应用", Description: "兼容原有企业内部应用。需要公网 HTTPS 回调及应用凭据，与智能机器人不同。", Documentation: "https://developer.work.weixin.qq.com/document/path/90239", Transport: "webhook", Legacy: true, MaxTextBytes: 1800, IdentityFields: []string{"corp_id", "app_id"}, Fields: []channels.Field{
		{Key: "corp_id", Label: "企业 CorpID", Required: true}, {Key: "app_id", Label: "应用 AgentID", Required: true, Type: "number"}, {Key: "secret", Label: "应用 Secret", Secret: true, Required: true}, {Key: "token", Label: "回调 Token", Secret: true, Required: true}, {Key: "aes_key", Label: "EncodingAESKey", Secret: true, Required: true},
	}}
}
func (p *applicationProvider) Validate(c channels.Config) error {
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
func (p *applicationProvider) CallbackPath(id string) string { return "/callbacks/wecom/" + id }
func (p *applicationProvider) Test(ctx context.Context, c channels.Config) channels.Result {
	app, _ := strconv.ParseInt(c.Values["app_id"], 10, 64)
	r := p.client.Test(ctx, wecom.Application{ID: c.ID, CorpID: c.Values["corp_id"], AppID: app, Secret: c.Secrets["secret"]})
	return channels.Result{State: r.State, Code: r.Code, Message: r.Message}
}
func (p *applicationProvider) Open(_ context.Context, c channels.Config, _ channels.Receive) (channels.Connection, error) {
	app, _ := strconv.ParseInt(c.Values["app_id"], 10, 64)
	return &applicationConnection{p.client, wecom.Application{ID: c.ID, CorpID: c.Values["corp_id"], AppID: app, Secret: c.Secrets["secret"]}}, nil
}

type applicationConnection struct {
	client *wecom.Client
	app    wecom.Application
}

func (c *applicationConnection) Send(ctx context.Context, d channels.Delivery) channels.Result {
	r := c.client.Send(ctx, c.app, d.Target, d.Text)
	return channels.Result{State: r.State, Code: r.Code, Message: r.Message}
}
func (c *applicationConnection) Status() channels.Status {
	return channels.Status{State: "webhook", Message: "使用签名加密回调接收消息。"}
}
func (c *applicationConnection) Close() {}
func botDescriptor() channels.Descriptor {
	return channels.Descriptor{ID: "wecom-bot", Name: "企业微信 · 智能机器人", Description: "推荐：API 长连接模式，仅需 BotID 与 Secret，无需公网回调地址。支持文本单聊和群内 @ 消息。", Documentation: "https://developer.work.weixin.qq.com/document/path/101463", Transport: "websocket", MaxTextBytes: 20000, IdentityFields: []string{"bot_id"}, Fields: []channels.Field{{Key: "bot_id", Label: "BotID", Required: true, Help: "在企微智能机器人中启用 API 模式并选择长连接。"}, {Key: "secret", Label: "机器人 Secret", Secret: true, Required: true, Help: "长连接专用密钥，不是应用 Secret 或回调 Token。"}}}
}

// ConfigureChannelAdapters is called before starting workers. Additional trusted
// Node packages can implement channel protocol v1 without changes to this core.
func (s *Server) ConfigureChannelAdapters(node, workerScript string) error {
	registry := channels.NewRegistry()
	_ = registry.Register(&applicationProvider{s.wecomClient})
	script, err := filepath.Abs(workerScript)
	if err != nil {
		return err
	}
	if err := registry.Register(&channels.ProcessProvider{Spec: botDescriptor(), Node: node, Script: script}); err != nil {
		return err
	}
	root := filepath.Join(s.store.directory, "channel-adapters")
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		slog.Warn("channel adapter discovery unavailable")
		entries = nil
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		count++
		if count > 32 {
			slog.Warn("channel adapter limit reached")
			break
		}
		provider, err := loadProcessAdapter(filepath.Join(root, entry.Name()), node)
		if err == nil {
			err = registry.Register(provider)
		}
		if err != nil {
			slog.Warn("channel adapter unavailable", "package", entry.Name(), "reason", err.Error())
		}
	}
	s.channelProviders = registry
	return nil
}

func loadProcessAdapter(directory, node string) (channels.Provider, error) {
	manifestPath := filepath.Join(directory, "adapter.json")
	info, err := os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64000 {
		return nil, errors.New("渠道适配器清单无效。")
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, errors.New("无法读取渠道适配器清单。")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 64001))
	file.Close()
	if readErr != nil || len(raw) > 64000 {
		return nil, errors.New("渠道适配器清单过大或无法读取。")
	}
	var manifest struct {
		V        int                 `json:"v"`
		Entry    string              `json:"entry"`
		Provider channels.Descriptor `json:"provider"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.V != 1 || manifest.Entry == "" || filepath.IsAbs(manifest.Entry) || strings.ContainsAny(manifest.Entry, "\\\x00") || strings.Contains(manifest.Entry, "..") || len(manifest.Entry) > 512 {
		return nil, errors.New("渠道适配器协议版本或入口无效。")
	}
	worker := filepath.Join(directory, manifest.Entry)
	info, err = os.Lstat(worker)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("渠道适配器入口必须是普通文件。")
	}
	canonicalRoot, rootErr := filepath.EvalSymlinks(directory)
	canonicalWorker, workerErr := filepath.EvalSymlinks(worker)
	if rootErr != nil || workerErr != nil {
		return nil, errors.New("无法解析渠道适配器入口。")
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalWorker)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("渠道适配器入口必须位于安装包内。")
	}
	return &channels.ProcessProvider{Spec: manifest.Provider, Node: node, Script: canonicalWorker, Directory: canonicalRoot}, nil
}

type channelConnection struct {
	version    int
	connection channels.Connection
}
type channelConnections struct {
	sync.Mutex
	values map[string]channelConnection
}

func (s *Server) syncChannelConnections(ctx context.Context) error {
	rows, err := s.store.db.QueryContext(ctx, "SELECT id FROM channels WHERE enabled=1 AND archived=0")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	active := map[string]bool{}
	s.channelConnections.Lock()
	defer s.channelConnections.Unlock()
	for _, id := range ids {
		active[id] = true
		c, secret, err := s.loadChannel(ctx, id)
		if err != nil {
			return err
		}
		if old, ok := s.channelConnections.values[id]; ok {
			if old.version == c.Version {
				continue
			}
			old.connection.Close()
			delete(s.channelConnections.values, id)
		}
		provider, ok := s.channelProviders.Get(c.Provider)
		if !ok {
			continue
		}
		connection, err := provider.Open(ctx, channels.Config{ID: id, Provider: c.Provider, Version: c.Version, Values: c.Config, Secrets: secret}, func(ctx context.Context, in channels.Incoming) error { return s.receiveChannel(ctx, id, c.Version, in) })
		if err != nil {
			connection = &unavailableChannel{}
		}
		s.channelConnections.values[id] = channelConnection{c.Version, connection}
	}
	for id, old := range s.channelConnections.values {
		if !active[id] {
			old.connection.Close()
			delete(s.channelConnections.values, id)
		}
	}
	return nil
}
func (s *Server) closeChannelConnections() {
	s.channelConnections.Lock()
	defer s.channelConnections.Unlock()
	for id, entry := range s.channelConnections.values {
		entry.connection.Close()
		delete(s.channelConnections.values, id)
	}
}
func (s *Server) channelConnectionStatus(id string) channels.Status {
	s.channelConnections.Lock()
	defer s.channelConnections.Unlock()
	if entry, ok := s.channelConnections.values[id]; ok {
		return entry.connection.Status()
	}
	return channels.Status{State: "stopped", Message: "渠道已停用或尚未启动。"}
}

type unavailableChannel struct{}

func (*unavailableChannel) Send(context.Context, channels.Delivery) channels.Result {
	return channels.Result{State: "failed", Message: "无法启动已安装的渠道适配器，请检查安装文件。"}
}
func (*unavailableChannel) Status() channels.Status {
	return channels.Status{State: "failed", Message: "无法启动渠道适配器。"}
}
func (*unavailableChannel) Close() {}
