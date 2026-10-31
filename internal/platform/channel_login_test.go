package platform

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ilinkLoginGateway answers the ilink login calls and immediately confirms the
// scan, so the console flow can be exercised without a phone.
func ilinkLoginGateway(t *testing.T, token string) *httptest.Server {
	t.Helper()
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			json.NewEncoder(w).Encode(map[string]any{
				"qrcode":             "qr-key",
				"qrcode_img_content": "https://ilinkai.weixin.qq.com/qr-login",
			})
		case "/ilink/bot/get_qrcode_status":
			json.NewEncoder(w).Encode(map[string]any{
				"status":       "confirmed",
				"bot_token":    token,
				"ilink_bot_id": "bot-id",
				"baseurl":      serverURL,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	serverURL = server.URL
	t.Cleanup(server.Close)
	return server
}

func TestChannelLoginFillsCredentialsWithoutLeakingTheToken(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "model-secret")
	gateway := ilinkLoginGateway(t, "private-bot-token")
	t.Setenv("SIAGENT_WEIXIN_API", gateway.URL)

	response := f.request(t, "POST", "/api/channel-logins", map[string]string{"provider": "weixin"}, true, nil)
	requireStatus(t, response, 201)
	var started struct {
		ID    string `json:"id"`
		State string `json:"state"`
		QRPNG string `json:"qr_png"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.ID == "" || started.State != "pending" {
		t.Fatalf("unexpected login start: %+v", started)
	}
	if png, err := base64.StdEncoding.DecodeString(started.QRPNG); err != nil || len(png) < 512 {
		t.Fatalf("login did not return a QR image: %d bytes", len(png))
	}
	if strings.Contains(response.Body.String(), "private-bot-token") {
		t.Fatal("login start response leaked the token")
	}

	var config map[string]string
	var state string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := f.request(t, "GET", "/api/channel-logins/"+started.ID, nil, true, nil)
		requireStatus(t, status, 200)
		if strings.Contains(status.Body.String(), "private-bot-token") {
			t.Fatal("login status response leaked the token")
		}
		var payload struct {
			State  string            `json:"state"`
			Config map[string]string `json:"config"`
		}
		json.Unmarshal(status.Body.Bytes(), &payload)
		state, config = payload.State, payload.Config
		if state == "confirmed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state != "confirmed" || config["bot_id"] != "bot-id" {
		t.Fatalf("login did not confirm: %s %+v", state, config)
	}

	// The browser submits the login id instead of a token; the platform merges
	// the secret server-side before validation.
	response = f.request(t, "POST", "/api/channels", map[string]any{
		"name":     "Personal WeChat",
		"provider": "weixin",
		"config":   map[string]string{},
		"secrets":  map[string]string{},
		"login_id": started.ID,
		"enabled":  false,
	}, true, nil)
	requireStatus(t, response, 201)
	var channel Channel
	if err := json.Unmarshal(response.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Body.String(), "private-bot-token") {
		t.Fatal("channel response leaked the token")
	}
	loaded, secrets, err := f.server.loadChannel(t.Context(), channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets["token"] != "private-bot-token" {
		t.Fatalf("login token not stored: %+v", secrets)
	}
	if loaded.Config["bot_id"] != "bot-id" || loaded.Config["base_url"] != gateway.URL {
		t.Fatalf("login values not merged: %+v", loaded.Config)
	}
	var stored string
	if err := f.store.db.QueryRow("SELECT secrets FROM channels WHERE id=?", channel.ID).Scan(&stored); err != nil || strings.Contains(stored, "private-bot-token") {
		t.Fatal("channel credential stored in plaintext", err)
	}

	// A login is single-use, so a stale browser tab cannot create a second
	// channel with the same confirmed scan.
	response = f.request(t, "POST", "/api/channels", map[string]any{
		"name": "Another", "provider": "weixin", "config": map[string]string{},
		"secrets": map[string]string{}, "login_id": started.ID, "enabled": false,
	}, true, nil)
	requireStatus(t, response, 400)

	// Without a login or a pasted token the save is refused with an actionable
	// error instead of an opaque provider failure.
	response = f.request(t, "POST", "/api/channels", map[string]any{
		"name": "Missing", "provider": "weixin", "config": map[string]string{},
		"secrets": map[string]string{}, "enabled": false,
	}, true, nil)
	requireStatus(t, response, 400)
}

func TestChannelLoginCanBeCancelled(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	t.Setenv("SIAGENT_WEIXIN_API", ilinkLoginGateway(t, "private-bot-token").URL)
	response := f.request(t, "POST", "/api/channel-logins", map[string]string{"provider": "weixin"}, true, nil)
	requireStatus(t, response, 201)
	var started struct {
		ID string `json:"id"`
	}
	json.Unmarshal(response.Body.Bytes(), &started)
	requireStatus(t, f.request(t, "DELETE", "/api/channel-logins/"+started.ID, nil, true, nil), 200)
	requireStatus(t, f.request(t, "GET", "/api/channel-logins/"+started.ID, nil, true, nil), 404)
	// Providers without the login capability report a clear refusal.
	requireStatus(t, f.request(t, "POST", "/api/channel-logins", map[string]string{"provider": "wecom-bot"}, true, nil), 400)
}
