// Package weixin implements the bundled personal-WeChat channel plugin on top of
// Tencent's ilink bot gateway: QR login, HTTP long polling for inbound messages
// and HTTP sends. No public callback or public IP is involved.
package weixin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	protocol "siagent/internal/channelplugins/weixin/protocol"
	"siagent/internal/channels"
)

const maxTextBytes = 3800

// inboundMediaTimeout bounds the CDN work for one message so a slow download
// cannot stall the long-poll loop forever.
const inboundMediaTimeout = 20 * time.Second

func New() channels.Provider { return &Provider{} }

type Provider struct{}

func (p *Provider) Descriptor() channels.Descriptor {
	return channels.Descriptor{
		ID:             "weixin",
		Name:           "微信 · 个人号",
		Description:    "腾讯 ilink 机器人网关（HTTP 长轮询），用手机微信扫码登录；无需公网回调。支持文字、引用、语音转写和图片。",
		Transport:      "polling",
		MaxTextBytes:   maxTextBytes,
		Images:         true,
		Priority:       80,
		IdentityFields: []string{"bot_id"},
		Fields: []channels.Field{
			{Key: "bot_id", Label: "机器人 ID", Required: true, Help: "运行 siagent weixin-login 扫码后输出；也用于区分多账号。"},
			{Key: "token", Label: "登录令牌", Secret: true, Required: true, Help: "扫码登录输出的 bot_token；过期或被撤销后需重新扫码。"},
			{Key: "base_url", Label: "网关地址", Help: "留空使用 https://ilinkai.weixin.qq.com；网关返回其他地址时填写。"},
			{Key: "cdn_base_url", Label: "媒体 CDN 地址", Help: "留空使用 https://novac2c.cdn.weixin.qq.com/c2c。"},
			{Key: "route_tag", Label: "路由标签", Help: "运营商要求时填写；留空表示不发送 SKRouteTag。"},
		},
	}
}

func (p *Provider) Validate(c channels.Config) error {
	if err := validateBotID(c.Values["bot_id"]); err != nil {
		return err
	}
	if err := validateEndpoint("网关地址", c.Values["base_url"]); err != nil {
		return err
	}
	if err := validateEndpoint("媒体 CDN 地址", c.Values["cdn_base_url"]); err != nil {
		return err
	}
	if tag := c.Values["route_tag"]; len(tag) > 128 || strings.ContainsAny(tag, "\x00\r\n") || strings.TrimSpace(tag) != tag {
		return errors.New("路由标签须为不超过 128 字节的单行文本。")
	}
	return nil
}

func validateBotID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("请填写有效的机器人 ID（扫码登录输出的 ilink_bot_id）。")
	}
	return nil
}

// validateEndpoint accepts HTTPS, or plain HTTP only for a loopback host so a
// local test fixture can be used without weakening production deployments.
func validateEndpoint(label, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s须为不含查询参数的 URL。", label)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("%s必须使用 HTTPS（本机测试可使用 http://127.0.0.1）。", label)
}

func (p *Provider) Test(ctx context.Context, c channels.Config) channels.Result {
	client := protocol.NewClient(account(c))
	// The gateway authenticates before it holds the long poll, so an invalid
	// token is refused immediately while a valid one simply has nothing to send.
	probe, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	updates, err := client.GetUpdates(probe, "", 0)
	if err == nil && updates != nil && updates.Ret != 0 {
		if updates.Ret == protocol.ErrSessionExpired || updates.Errcode == protocol.ErrSessionExpired {
			return channels.Result{State: "failed", Code: updates.Ret, Message: "微信登录已失效，请重新扫码登录。"}
		}
		return channels.Result{State: "failed", Code: updates.Ret, Message: "微信网关拒绝了长轮询请求，请检查凭据与网关状态。"}
	}
	if probe.Err() != nil {
		// The gateway holds a valid long poll; an invalid token is refused at
		// once, so a timed-out probe means the credential was accepted.
		return channels.Result{State: "sent", Message: "微信网关已接受长轮询请求（6 秒内没有消息返回）；实际收发仍需成员发送消息验证。"}
	}
	if err != nil {
		var refusal *protocol.Error
		if errors.As(err, &refusal) {
			return channels.Result{State: "failed", Code: refusal.Status, Message: "微信网关拒绝了凭据，请检查令牌或重新扫码登录。"}
		}
		return channels.Result{State: "retry", Message: "无法连接微信网关，请检查网络与网关地址。"}
	}
	return channels.Result{State: "sent", Message: "微信网关已返回长轮询响应；实际收发仍需成员发送消息验证。"}
}

func (p *Provider) Open(ctx context.Context, c channels.Config, receive channels.Receive) (channels.Connection, error) {
	connCtx, cancel := context.WithCancel(ctx)
	conn := &connection{
		client:   protocol.NewClient(account(c)),
		receive:  receive,
		mediaDir: c.MediaDir,
		cancel:   cancel,
		done:     make(chan struct{}),
		status:   channels.Status{State: "connecting", Message: "正在连接微信网关。"},
	}
	go conn.run(connCtx)
	return conn, nil
}

func account(c channels.Config) protocol.Account {
	return protocol.Account{
		ID:         c.ID,
		BotID:      strings.TrimSpace(c.Values["bot_id"]),
		Token:      c.Secrets["token"],
		BaseURL:    strings.TrimSpace(c.Values["base_url"]),
		CDNBaseURL: strings.TrimSpace(c.Values["cdn_base_url"]),
		RouteTag:   strings.TrimSpace(c.Values["route_tag"]),
	}
}

type connection struct {
	client   *protocol.Client
	receive  channels.Receive
	mediaDir string
	cancel   context.CancelFunc
	done     chan struct{}

	mu     sync.Mutex
	status channels.Status
}

func (c *connection) setStatus(state, message string) {
	c.mu.Lock()
	c.status = channels.Status{State: state, Message: message}
	c.mu.Unlock()
}

func (c *connection) Status() channels.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *connection) Close() {
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
}

// run owns the long-poll loop. The cursor advances only after a batch was
// persisted, so a rejected admission is retried by the next poll; the platform
// deduplicates by message ID, which the gateway itself can retransmit.
func (c *connection) run(ctx context.Context) {
	defer close(c.done)
	cursor := ""
	pollTimeout := 35 * time.Second
	backoff := time.Second
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		// Successful authentication is proven by the gateway holding the poll
		// instead of refusing it immediately; report ready after a short grace.
		var readyTimer *time.Timer
		if first {
			readyTimer = time.AfterFunc(2*time.Second, func() {
				c.setStatus("connected", "微信网关已接受长轮询连接。")
			})
		}
		updates, err := c.client.GetUpdates(ctx, cursor, pollTimeout)
		if readyTimer != nil {
			readyTimer.Stop()
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var refusal *protocol.Error
			if errors.As(err, &refusal) && (refusal.Status == 401 || refusal.Status == 403 || refusal.Ret == protocol.ErrSessionExpired) {
				c.setStatus("failed", "微信网关拒绝了登录凭据，请重新扫码登录。")
				return
			}
			c.setStatus("reconnecting", "微信网关连接中断，正在重试。")
			if !sleep(ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		if first {
			first = false
			c.setStatus("connected", "微信网关长轮询已连接。")
		}
		if updates.Ret != 0 {
			if updates.Ret == protocol.ErrSessionExpired || updates.Errcode == protocol.ErrSessionExpired {
				c.setStatus("failed", "微信登录已失效，请重新扫码。")
				return
			}
			c.setStatus("reconnecting", "微信网关返回异常状态，正在重试。")
			if !sleep(ctx, backoff) {
				return
			}
			continue
		}
		if updates.LongPollingTimeout > 0 {
			pollTimeout = time.Duration(updates.LongPollingTimeout) * time.Millisecond
			if pollTimeout < 5*time.Second || pollTimeout > 2*time.Minute {
				pollTimeout = 35 * time.Second
			}
		}
		delivered := true
		for _, message := range updates.Msgs {
			if err := c.handle(ctx, message); err != nil {
				delivered = false
				break
			}
		}
		if !delivered {
			// Keep the cursor so the batch is retried; the platform turns the
			// replays into no-ops.
			if !sleep(ctx, 2*time.Second) {
				return
			}
			continue
		}
		if updates.GetUpdatesBuf != "" {
			cursor = updates.GetUpdatesBuf
		}
	}
}

func (c *connection) handle(ctx context.Context, message protocol.Message) error {
	if message.MessageType != protocol.MessageTypeUser || message.MessageID == 0 {
		return nil
	}
	from := strings.TrimSpace(message.FromUserID)
	if !validMember(from) {
		return nil
	}
	text, kind, mediaItems := describeItems(message.ItemList)
	var images []channels.IncomingImage
	if len(mediaItems) > 0 {
		images = c.stageImages(ctx, message.MessageID, mediaItems)
		// Even a failed download reports an image message: the platform then
		// answers with an explicit notice instead of an empty question.
		kind = "image"
	}
	incoming := channels.Incoming{
		ID:       strconv.FormatInt(message.MessageID, 10),
		ChatType: "single",
		SenderID: from,
		Kind:     kind,
		Text:     text,
		Images:   images,
		ReplyContext: map[string]string{
			"context_token": message.ContextToken,
			"from_user":     from,
			"received_at":   strconv.FormatInt(time.Now().UnixMilli(), 10),
		},
	}
	if c.receive == nil {
		return nil
	}
	// Bound admission so a stuck platform write cannot block the long poll; the
	// cursor stays put and the batch is retried (the platform deduplicates).
	receiveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.receive(receiveCtx, incoming)
}

// describeItems flattens one message's items into text plus the media the
// platform may accept. Quoted messages keep their title/text as an explicit
// reference, voice messages use the gateway's transcript, and a message with
// text plus media is treated as a text question that carries an attachment.
func describeItems(items []protocol.Item) (string, string, []protocol.Item) {
	parts := []string{}
	images := []protocol.Item{}
	kind := "text"
	for _, item := range items {
		switch item.Type {
		case protocol.ItemText:
			if item.TextItem == nil {
				continue
			}
			text := strings.TrimSpace(item.TextItem.Text)
			if text == "" {
				continue
			}
			if item.RefMsg != nil {
				quoted := []string{}
				if item.RefMsg.Title != "" {
					quoted = append(quoted, item.RefMsg.Title)
				}
				if item.RefMsg.MessageItem != nil {
					if body, _, _ := describeItems([]protocol.Item{*item.RefMsg.MessageItem}); body != "" {
						quoted = append(quoted, body)
					}
				}
				if len(quoted) > 0 {
					text = "[引用: " + strings.Join(quoted, " | ") + "]\n" + text
				}
			}
			parts = append(parts, text)
		case protocol.ItemVoice:
			if item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "" {
				parts = append(parts, strings.TrimSpace(item.VoiceItem.Text))
			} else if kind == "text" {
				kind = "voice"
			}
		case protocol.ItemImage:
			if item.ImageItem != nil {
				images = append(images, item)
			}
		case protocol.ItemFile:
			if len(parts) == 0 && len(images) == 0 {
				kind = "file"
			}
		case protocol.ItemVideo:
			if len(parts) == 0 && len(images) == 0 {
				kind = "video"
			}
		}
	}
	text := strings.Join(parts, "\n")
	if text != "" {
		kind = "text"
	}
	return text, kind, images
}

// stageImages downloads and decrypts each picture into the platform staging
// directory, so image bytes never travel inside a protocol frame. The platform
// re-validates content and applies its own limits.
func (c *connection) stageImages(ctx context.Context, messageID int64, items []protocol.Item) []channels.IncomingImage {
	if c.mediaDir == "" {
		return nil
	}
	mediaCtx, cancel := context.WithTimeout(ctx, inboundMediaTimeout)
	defer cancel()
	staged := make([]channels.IncomingImage, 0, len(items))
	for index, item := range items {
		if len(staged) >= 8 {
			break
		}
		image := item.ImageItem
		if image == nil || image.Media == nil || strings.TrimSpace(image.Media.EncryptQueryParam) == "" {
			continue
		}
		key := strings.TrimSpace(image.AESKeyHex)
		if key == "" {
			key = strings.TrimSpace(image.Media.AESKey)
		}
		data, err := c.client.Download(mediaCtx, image.Media.EncryptQueryParam, key)
		if err != nil || len(data) == 0 || len(data) > 16<<20 {
			continue
		}
		name := fmt.Sprintf("weixin-%d-%d-%s.bin", messageID, index, randomSuffix())
		if err := os.WriteFile(filepath.Join(c.mediaDir, name), data, 0600); err != nil {
			continue
		}
		staged = append(staged, channels.IncomingImage{Name: fmt.Sprintf("图片%d", len(staged)+1), File: name})
	}
	return staged
}

func (c *connection) Send(ctx context.Context, delivery channels.Delivery) channels.Result {
	target := strings.TrimSpace(delivery.ReplyContext["from_user"])
	contextToken := strings.TrimSpace(delivery.ReplyContext["context_token"])
	if !validMember(target) || contextToken == "" {
		return channels.Result{State: "failed", Message: "缺少成员或会话上下文，请成员重新发送一条消息。"}
	}
	if len(delivery.Text) == 0 || len(delivery.Text) > maxTextBytes {
		return channels.Result{State: "failed", Message: "回复内容为空或超过单条上限。"}
	}
	err := c.client.SendText(ctx, target, delivery.Text, contextToken, "siagent-"+randomSuffix())
	if err == nil {
		return channels.Result{State: "sent", Message: "微信网关已接受消息。"}
	}
	var refusal *protocol.Error
	if !errors.As(err, &refusal) {
		return channels.Result{State: "unknown", Message: "发送结果不明，请核对微信；不会自动重发。"}
	}
	switch {
	case refusal.Status == 429:
		return channels.Result{State: "retry", Code: refusal.Status, Message: "微信网关限流，稍后有限重试。"}
	case refusal.Status == 401 || refusal.Status == 403:
		return channels.Result{State: "failed", Code: refusal.Status, Message: "微信登录凭据被拒绝，请重新扫码登录。"}
	case refusal.Ret == protocol.ErrThrottled || refusal.Errcode == protocol.ErrThrottled:
		return channels.Result{State: "failed", Code: refusal.Ret, Message: "微信网关限制了主动消息频率；请减少提醒或稍后再试。"}
	case refusal.Ret == protocol.ErrSessionExpired || refusal.Errcode == protocol.ErrSessionExpired:
		return channels.Result{State: "failed", Code: refusal.Ret, Message: "会话上下文已过期，请成员重新发送一条消息。"}
	case refusal.Status >= 500:
		return channels.Result{State: "unknown", Code: refusal.Status, Message: "网关响应异常，结果不明，请人工核对。"}
	}
	return channels.Result{State: "failed", Code: refusal.Errcode, Message: fmt.Sprintf("微信网关拒绝发送（ret=%d errcode=%d），请检查网关状态。", refusal.Ret, refusal.Errcode)}
}

func validMember(value string) bool {
	return value != "" && len(value) <= 256 && !strings.HasPrefix(value, "@") && !strings.ContainsAny(value, "\x00\r\n ") && strings.TrimSpace(value) == value
}

func randomSuffix() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(raw[:])
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
