package weixin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"siagent/internal/channels"
)

// loginGateway answers the two ilink login calls. The status sequence can be
// scripted so both intermediate and final states are covered.
type loginGateway struct {
	statuses []map[string]any
	calls    int
}

func (g *loginGateway) handler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ilink/bot/get_bot_qrcode":
		json.NewEncoder(w).Encode(map[string]any{
			"qrcode":             "qr-key",
			"qrcode_img_content": "https://ilinkai.weixin.qq.com/qr-login",
		})
	case "/ilink/bot/get_qrcode_status":
		index := g.calls
		if index >= len(g.statuses) {
			index = len(g.statuses) - 1
		}
		g.calls++
		json.NewEncoder(w).Encode(g.statuses[index])
	default:
		http.NotFound(w, r)
	}
}

func TestLoginSessionFetchesCredentialsAndExposesQRCode(t *testing.T) {
	gateway := &loginGateway{statuses: []map[string]any{
		{"status": "wait"},
		{"status": "scaned"},
		{"status": "confirmed", "bot_token": "bot-token", "ilink_bot_id": "bot-id", "baseurl": "https://gateway.example/"},
	}}
	server := httptest.NewServer(http.HandlerFunc(gateway.handler))
	defer server.Close()
	t.Setenv("SIAGENT_WEIXIN_API", server.URL)
	provider := New().(channels.LoginProvider)
	session, err := provider.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	content, png := session.QRCode()
	if content != "https://ilinkai.weixin.qq.com/qr-login" {
		t.Fatalf("unexpected QR content: %q", content)
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) || len(png) < 512 {
		t.Fatalf("expected a rendered PNG QR code, got %d bytes", len(png))
	}
	deadline := time.Now().Add(5 * time.Second)
	var status channels.LoginStatus
	for time.Now().Before(deadline) {
		status = session.Status()
		if status.State == "confirmed" || status.State == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.State != "confirmed" {
		t.Fatalf("login did not confirm: %+v", status)
	}
	if status.Secrets["token"] != "bot-token" {
		t.Fatalf("token not captured: %+v", status.Secrets)
	}
	if status.Values["bot_id"] != "bot-id" || status.Values["base_url"] != "https://gateway.example" {
		t.Fatalf("unexpected login values: %+v", status.Values)
	}
}

func TestLoginSessionRejectsIncompleteConfirmation(t *testing.T) {
	gateway := &loginGateway{statuses: []map[string]any{
		{"status": "confirmed", "ilink_bot_id": "bot-id"},
	}}
	server := httptest.NewServer(http.HandlerFunc(gateway.handler))
	defer server.Close()
	t.Setenv("SIAGENT_WEIXIN_API", server.URL)
	provider := New().(channels.LoginProvider)
	session, err := provider.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if status := session.Status(); status.State == "failed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("login without a token was not rejected")
}
