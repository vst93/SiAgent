package platform

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"siagent/internal/channels"
)

// Interactive vendor logins (currently the personal-WeChat QR scan). The
// console starts a session, renders the code and stores the resulting secrets
// server-side; the browser only receives non-secret configuration values. A
// session is single-use and expires quickly, so a leaked id cannot become a
// reusable credential.

const (
	maxChannelLogins = 2
	channelLoginTTL  = 5 * time.Minute
)

type channelLogin struct {
	provider string
	session  channels.LoginSession
	expires  time.Time
}

func (s *Server) pruneChannelLoginsLocked(now time.Time) {
	for loginID, entry := range s.channelLogins {
		if now.After(entry.expires) {
			entry.session.Close()
			delete(s.channelLogins, loginID)
		}
	}
}

func (s *Server) startChannelLogin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
	}
	if !decode(w, r, &input) {
		return
	}
	provider, ok := s.channelProviders.Get(input.Provider)
	if !ok {
		fail(w, 404, "渠道适配器未安装。")
		return
	}
	loginProvider, ok := provider.(channels.LoginProvider)
	if !ok {
		fail(w, 400, "该渠道不支持扫码登录，请手动填写凭据。")
		return
	}
	now := time.Now()
	s.channelLoginsMu.Lock()
	s.pruneChannelLoginsLocked(now)
	if len(s.channelLogins) >= maxChannelLogins {
		s.channelLoginsMu.Unlock()
		fail(w, 409, "已有扫码登录进行中，请稍候或取消后重试。")
		return
	}
	// The session outlives this request: it owns its own lifetime and the
	// platform closes it on cancel, expiry or save.
	session, err := loginProvider.Login(context.Background())
	if err != nil {
		s.channelLoginsMu.Unlock()
		fail(w, 502, "无法开始扫码登录，请检查网络与网关地址。")
		return
	}
	loginID := id()
	s.channelLogins[loginID] = &channelLogin{provider: input.Provider, session: session, expires: now.Add(channelLoginTTL)}
	s.channelLoginsMu.Unlock()
	content, png := session.QRCode()
	status := session.Status()
	s.audit("channel.login.start", loginID, input.Provider)
	respond(w, 201, map[string]any{
		"id":         loginID,
		"state":      status.State,
		"message":    status.Message,
		"qr_url":     content,
		"qr_png":     base64.StdEncoding.EncodeToString(png),
		"expires_at": now.Add(channelLoginTTL).UnixMilli(),
	})
}

func (s *Server) channelLoginStatus(w http.ResponseWriter, r *http.Request) {
	s.channelLoginsMu.Lock()
	s.pruneChannelLoginsLocked(time.Now())
	entry, ok := s.channelLogins[r.PathValue("login")]
	s.channelLoginsMu.Unlock()
	if !ok {
		fail(w, 404, "扫码登录已失效，请重新发起。")
		return
	}
	status := entry.session.Status()
	payload := map[string]any{"state": status.State, "message": status.Message}
	if status.State == "confirmed" {
		// Only ordinary fields (bot id, gateway address) reach the browser; the
		// token stays in the session until the channel is saved.
		payload["config"] = status.Values
		payload["ready"] = true
	}
	respond(w, 200, payload)
}

func (s *Server) cancelChannelLogin(w http.ResponseWriter, r *http.Request) {
	s.channelLoginsMu.Lock()
	entry := s.channelLogins[r.PathValue("login")]
	delete(s.channelLogins, r.PathValue("login"))
	s.channelLoginsMu.Unlock()
	if entry != nil {
		entry.session.Close()
	}
	respond(w, 200, map[string]bool{"cancelled": true})
}

// peekChannelLogin resolves a login for a channel save without consuming it:
// validation errors must not force another scan.
func (s *Server) peekChannelLogin(loginID, provider string) (channels.LoginStatus, bool) {
	s.channelLoginsMu.Lock()
	defer s.channelLoginsMu.Unlock()
	s.pruneChannelLoginsLocked(time.Now())
	entry, ok := s.channelLogins[loginID]
	if !ok || entry.provider != provider {
		return channels.LoginStatus{}, false
	}
	return entry.session.Status(), true
}

// retireChannelLogin consumes one login after the channel row was written.
func (s *Server) retireChannelLogin(loginID string) {
	s.channelLoginsMu.Lock()
	entry := s.channelLogins[loginID]
	delete(s.channelLogins, loginID)
	s.channelLoginsMu.Unlock()
	if entry != nil {
		entry.session.Close()
	}
}

// applyChannelLogin merges the non-secret values and secrets of a confirmed
// login into the channel input. Explicit ordinary values win, so an operator can
// still adjust the gateway address.
func (s *Server) applyChannelLogin(input *channelInput) error {
	loginID := strings.TrimSpace(input.LoginID)
	if loginID == "" {
		return nil
	}
	status, ok := s.peekChannelLogin(loginID, input.Provider)
	if !ok {
		return loginError(400, "扫码登录已失效或不属于该渠道类型，请重新扫码。")
	}
	if status.State != "confirmed" {
		return loginError(400, "请先在手机上确认扫码登录。")
	}
	if input.Config == nil {
		input.Config = map[string]string{}
	}
	for key, value := range status.Values {
		if input.Config[key] == "" {
			input.Config[key] = value
		}
	}
	if input.Secrets == nil {
		input.Secrets = map[string]string{}
	}
	for key, value := range status.Secrets {
		input.Secrets[key] = value
	}
	input.LoginID = loginID
	return nil
}

func loginError(status int, message string) error {
	return &admissionError{Status: status, Message: message}
}
