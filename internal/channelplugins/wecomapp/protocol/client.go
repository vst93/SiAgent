package wecom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const API = "https://qyapi.weixin.qq.com/cgi-bin"

type Application struct {
	ID, CorpID string
	AppID      int64
	Secret     string
}
type Result struct {
	State   string
	Code    int
	Message string
}
type token struct {
	fingerprint [32]byte
	value       string
	expires     time.Time
}
type Client struct {
	HTTP   *http.Client
	mu     sync.Mutex
	tokens map[string]token
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, tokens: map[string]token{}}
}
func (c *Client) access(ctx context.Context, a Application) (string, Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fingerprint := sha256.Sum256([]byte(a.CorpID + "\x00" + a.Secret))
	cached := c.tokens[a.ID]
	if cached.fingerprint == fingerprint && time.Now().Before(cached.expires) {
		return cached.value, Result{}
	}
	endpoint := API + "/gettoken?" + url.Values{"corpid": {a.CorpID}, "corpsecret": {a.Secret}}.Encode()
	request, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return "", Result{"retry", 0, "无法取得企微访问凭据，尚未发送消息。"}
	}
	defer response.Body.Close()
	var data struct {
		Code    *int   `json:"errcode"`
		Access  string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	err = decodeReply(response.Body, &data)
	if response.StatusCode != 200 || err != nil || data.Code == nil {
		return "", Result{"retry", 0, "企微凭据接口响应无效，尚未发送消息。"}
	}
	if *data.Code != 0 {
		state := "failed"
		if *data.Code == -1 || *data.Code == 45009 {
			state = "retry"
		}
		return "", Result{state, *data.Code, "企微未提供访问凭据，请检查 Secret、应用权限及可信 IP。"}
	}
	if len(data.Access) == 0 || len(data.Access) > 512 || data.Expires < 1 {
		return "", Result{"retry", 0, "企微返回了无效的访问凭据。"}
	}
	ttl := time.Duration(data.Expires) * time.Second
	if ttl > 2*time.Hour {
		ttl = 2 * time.Hour
	}
	c.tokens[a.ID] = token{fingerprint, data.Access, time.Now().Add(ttl - min(60*time.Second, ttl/10))}
	return data.Access, Result{}
}
func (c *Client) Test(ctx context.Context, a Application) Result {
	_, result := c.access(ctx, a)
	if result.State == "" {
		return Result{"sent", 0, "企微凭据接口已通过，不代表回调或成员收发已验收。"}
	}
	return result
}
func (c *Client) Send(ctx context.Context, a Application, member, text string) Result {
	if !ValidMember(member) || len(text) == 0 || len(text) > 2048 {
		return Result{"failed", 0, "接收成员或文本长度无效。"}
	}
	access, result := c.access(ctx, a)
	if result.State != "" {
		return result
	}
	body, _ := json.Marshal(map[string]any{"touser": member, "msgtype": "text", "agentid": a.AppID, "text": map[string]string{"content": text}, "safe": 0, "enable_duplicate_check": 0})
	request, _ := http.NewRequestWithContext(ctx, "POST", API+"/message/send?access_token="+url.QueryEscape(access), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return Result{"unknown", 0, "发送结果不明，请核对企微；不会自动重发。"}
	}
	defer response.Body.Close()
	var data struct {
		Code       *int   `json:"errcode"`
		Invalid    string `json:"invaliduser"`
		Unlicensed string `json:"unlicenseduser"`
	}
	err = decodeReply(response.Body, &data)
	if response.StatusCode != 200 || err != nil || data.Code == nil {
		return Result{"unknown", 0, "发送响应无法确认，请核对企微；不会自动重发。"}
	}
	if *data.Code == 0 {
		if data.Invalid != "" || data.Unlicensed != "" {
			return Result{"failed", 0, "成员无效、不在应用可见范围或没有接口权限。"}
		}
		return Result{"sent", 0, "企微已接受消息；不代表成员已读。"}
	}
	if *data.Code == 40014 || *data.Code == 42001 {
		c.mu.Lock()
		delete(c.tokens, a.ID)
		c.mu.Unlock()
		return Result{"retry", *data.Code, "访问凭据失效，稍后重新取得凭据再发送。"}
	}
	if *data.Code == -1 || *data.Code == 45009 {
		return Result{"retry", *data.Code, "企微明确拒绝本次发送，稍后有限重试。"}
	}
	return Result{"failed", *data.Code, fmt.Sprintf("企微拒绝发送（错误码 %d），请检查应用、成员权限及可信 IP。", *data.Code)}
}

func decodeReply(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, 65537))
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return fmt.Errorf("response exceeds limit")
	}
	return json.Unmarshal(data, target)
}
