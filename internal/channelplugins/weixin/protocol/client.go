package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultBaseURL    = "https://ilinkai.weixin.qq.com"
	DefaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"
	ChannelVersion    = "siagent-weixin/1.0"

	defaultPollTimeout = 35 * time.Second
	maxResponseBytes   = 64 << 20
)

// Account is one ilink bot login. The token is a long-lived Bearer credential
// produced by QR login; BotID is the gateway's stable bot identifier.
type Account struct {
	ID         string
	BotID      string
	Token      string
	BaseURL    string
	CDNBaseURL string
	RouteTag   string
}

// Error is one explicit gateway refusal. Status is 0 when the request never
// completed, which the caller must treat as an uncertain outcome.
type Error struct {
	Op      string
	Status  int
	Ret     int
	Errcode int
	Errmsg  string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("weixin %s: http %d ret=%d errcode=%d %s", e.Op, e.Status, e.Ret, e.Errcode, e.Errmsg)
	}
	return fmt.Sprintf("weixin %s: ret=%d errcode=%d %s", e.Op, e.Ret, e.Errcode, e.Errmsg)
}

type Client struct {
	HTTP    *http.Client
	Account Account
}

func NewClient(account Account) *Client {
	if strings.TrimSpace(account.BaseURL) == "" {
		account.BaseURL = DefaultBaseURL
	}
	if strings.TrimSpace(account.CDNBaseURL) == "" {
		account.CDNBaseURL = DefaultCDNBaseURL
	}
	account.BaseURL = strings.TrimRight(account.BaseURL, "/")
	account.CDNBaseURL = strings.TrimRight(account.CDNBaseURL, "/")
	return &Client{HTTP: &http.Client{Timeout: 15 * time.Second}, Account: account}
}

// randomUIN mirrors the X-WECHAT-UIN header the mobile client sends: base64 of
// a decimal 32-bit integer.
func randomUIN() string {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "MDAwMA=="
	}
	value := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", value)))
}

func (c *Client) post(ctx context.Context, path string, payload any, timeout time.Duration) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Account.BaseURL+"/"+strings.TrimPrefix(path, "/"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("AuthorizationType", "ilink_bot_token")
	request.Header.Set("X-WECHAT-UIN", randomUIN())
	if c.Account.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Account.Token)
	}
	if c.Account.RouteTag != "" {
		request.Header.Set("SKRouteTag", c.Account.RouteTag)
	}
	client := c.HTTP
	if timeout > 0 {
		// Long polling needs its own deadline; the shared client stays short for
		// sends and media downloads.
		clone := *client
		clone.Timeout = timeout
		client = &clone
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxResponseBytes {
		return nil, errors.New("weixin: response exceeds the size limit")
	}
	if response.StatusCode != http.StatusOK {
		return nil, &Error{Op: strings.TrimPrefix(path, "/"), Status: response.StatusCode, Errmsg: truncate(raw, 256)}
	}
	return raw, nil
}

// GetUpdates long-polls the gateway. A poll that times out without data is a
// normal empty batch: the server simply had nothing to deliver.
func (c *Client) GetUpdates(ctx context.Context, cursor string, timeout time.Duration) (*Updates, error) {
	if timeout <= 0 {
		timeout = defaultPollTimeout
	}
	raw, err := c.post(ctx, "ilink/bot/getupdates", UpdatesRequest{GetUpdatesBuf: cursor, BaseInfo: BaseInfo{ChannelVersion: ChannelVersion}}, timeout+5*time.Second)
	if err != nil {
		// A poll deadline (client timeout or a caller-supplied deadline) simply
		// means the gateway had nothing to deliver. A canceled context is a
		// shutdown and must be reported.
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return &Updates{GetUpdatesBuf: cursor}, nil
		}
		return nil, err
	}
	var updates Updates
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, fmt.Errorf("weixin getUpdates: invalid response: %w", err)
	}
	if updates.LongPollingTimeout <= 0 {
		updates.LongPollingTimeout = int(defaultPollTimeout / time.Millisecond)
	}
	return &updates, nil
}

// SendText posts one finished text message. The context token from the inbound
// message must be echoed; without it the gateway cannot associate the reply.
func (c *Client) SendText(ctx context.Context, to, text, contextToken, clientID string) error {
	if strings.TrimSpace(contextToken) == "" {
		return &Error{Op: "sendmessage", Errcode: ErrSessionExpired, Errmsg: "missing context token"}
	}
	request := SendRequest{
		Msg: OutboundMessage{
			ToUserID:     to,
			ClientID:     clientID,
			MessageType:  MessageTypeBot,
			MessageState: MessageStateFinish,
			ContextToken: contextToken,
			ItemList:     []Item{{Type: ItemText, TextItem: &TextItem{Text: text}}},
		},
		BaseInfo: BaseInfo{ChannelVersion: ChannelVersion},
	}
	raw, err := c.post(ctx, "ilink/bot/sendmessage", request, 0)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var response SendResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("weixin sendmessage: invalid response: %w", err)
	}
	if response.Ret != 0 {
		return &Error{Op: "sendmessage", Ret: response.Ret, Errcode: response.Errcode, Errmsg: response.Errmsg}
	}
	return nil
}

func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	var value timeout
	return errors.As(err, &value) && value.Timeout()
}

func truncate(raw []byte, limit int) string {
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + "…"
}

// loginURL joins the gateway base with one login path, keeping a custom port or
// path prefix intact.
func loginURL(baseURL, path string, query url.Values) string {
	base := strings.TrimRight(baseURL, "/") + "/"
	parsed, err := url.Parse(base)
	if err != nil {
		return base + path
	}
	parsed = parsed.JoinPath(path)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
