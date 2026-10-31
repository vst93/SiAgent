package weixin

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"rsc.io/qr"

	protocol "siagent/internal/channelplugins/weixin/protocol"
	"siagent/internal/channels"
)

// The console login mirrors `siagent weixin-login`: the plugin fetches the QR
// code itself, polls the gateway and hands the resulting bot credentials to the
// platform. The operator never copies a token by hand, and the token is never
// sent back to the browser.

const (
	loginSessionTTL = 5 * time.Minute
	loginMaxRefresh = 3
)

func (p *Provider) Login(context.Context) (channels.LoginSession, error) {
	baseURL := strings.TrimSpace(os.Getenv("SIAGENT_WEIXIN_API"))
	if baseURL == "" {
		baseURL = protocol.DefaultBaseURL
	}
	routeTag := strings.TrimSpace(os.Getenv("SIAGENT_WEIXIN_ROUTE_TAG"))
	fetchCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	code, err := protocol.FetchQRCode(fetchCtx, baseURL, "", routeTag)
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	session := &loginSession{
		baseURL:  baseURL,
		routeTag: routeTag,
		qrCode:   code.Qrcode,
		cancel:   stop,
		done:     make(chan struct{}),
	}
	session.applyQR(code.QrcodeImgContent)
	go session.run(ctx)
	return session, nil
}

type loginSession struct {
	baseURL  string
	routeTag string

	mu     sync.Mutex
	qrCode string
	qrURL  string
	qrPNG  []byte
	status channels.LoginStatus

	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}
}

func (s *loginSession) applyQR(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.qrURL = url
	s.qrPNG = renderQR(url)
	s.status = channels.LoginStatus{State: "pending", Message: "等待扫码"}
}

func (s *loginSession) QRCode() (string, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.qrURL, append([]byte(nil), s.qrPNG...)
}

func (s *loginSession) Status() channels.LoginStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	status.Values = cloneMap(s.status.Values)
	status.Secrets = cloneMap(s.status.Secrets)
	return status
}

func (s *loginSession) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
		}
	})
}

func (s *loginSession) set(state, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = channels.LoginStatus{State: state, Message: message}
}

func (s *loginSession) run(ctx context.Context) {
	defer close(s.done)
	deadline := time.Now().Add(loginSessionTTL)
	refreshes := 0
	for {
		if ctx.Err() != nil {
			return
		}
		if time.Now().After(deadline) {
			s.set("expired", "扫码登录已超时，请重新发起。")
			return
		}
		s.mu.Lock()
		code := s.qrCode
		s.mu.Unlock()
		status, err := protocol.PollQRStatus(ctx, s.baseURL, code, s.routeTag)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.set("failed", "无法查询扫码状态，请稍后重试。")
			return
		}
		switch status.Status {
		case "", "wait":
			s.set("pending", "等待扫码")
		case "scaned":
			s.set("scanned", "已扫码，请在手机上确认登录。")
		case "expired":
			refreshes++
			if refreshes > loginMaxRefresh {
				s.set("expired", "二维码多次过期，请重新发起扫码登录。")
				return
			}
			refreshed, err := protocol.FetchQRCode(ctx, s.baseURL, "", s.routeTag)
			if err != nil {
				s.set("failed", "刷新二维码失败，请重新发起扫码登录。")
				return
			}
			s.mu.Lock()
			s.qrCode = refreshed.Qrcode
			s.mu.Unlock()
			s.applyQR(refreshed.QrcodeImgContent)
		case "confirmed":
			token := strings.TrimSpace(status.BotToken)
			botID := strings.TrimSpace(status.IlinkBotID)
			if token == "" || botID == "" {
				s.set("failed", "网关没有返回登录凭据，请重新扫码。")
				return
			}
			values := map[string]string{"bot_id": botID}
			if base := strings.TrimSpace(status.BaseURL); base != "" {
				values["base_url"] = strings.TrimRight(base, "/")
			}
			s.mu.Lock()
			s.status = channels.LoginStatus{
				State:   "confirmed",
				Message: "微信登录成功，机器人 ID 与令牌已获取。",
				Values:  values,
				Secrets: map[string]string{"token": token},
			}
			s.mu.Unlock()
			return
		default:
			time.Sleep(time.Second)
		}
	}
}

func renderQR(content string) []byte {
	code, err := qr.Encode(content, qr.M)
	if err != nil {
		return nil
	}
	code.Scale = 6
	return code.PNG()
}

func cloneMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	values := make(map[string]string, len(source))
	for key, value := range source {
		values[key] = value
	}
	return values
}
