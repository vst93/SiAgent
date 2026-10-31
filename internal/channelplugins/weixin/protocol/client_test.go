package weixin

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type redirectTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (r redirectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = r.target.Scheme
	clone.URL.Host = r.target.Host
	return r.inner.RoundTrip(clone)
}

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *[]*http.Request) {
	t.Helper()
	requests := []*http.Request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		requests = append(requests, r)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	client := NewClient(Account{ID: "channel", BotID: "bot", Token: "private-token"})
	client.HTTP.Transport = redirectTransport{endpoint, http.DefaultTransport}
	client.Account.BaseURL = server.URL
	client.Account.CDNBaseURL = server.URL + "/cdn"
	return client, &requests
}

func TestLongPollSendsGatewayHeadersAndParsesMessages(t *testing.T) {
	client, requests := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/getupdates" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("AuthorizationType") != "ilink_bot_token" || r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("missing bot authorization")
		}
		if decoded, err := base64.StdEncoding.DecodeString(r.Header.Get("X-WECHAT-UIN")); err != nil || len(decoded) == 0 {
			t.Error("missing base64 X-WECHAT-UIN")
		}
		var body struct {
			GetUpdatesBuf string `json:"get_updates_buf"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.GetUpdatesBuf != "cursor-0" {
			t.Errorf("cursor not echoed: %q", body.GetUpdatesBuf)
		}
		w.Write([]byte(`{"ret":0,"get_updates_buf":"cursor-1","longpolling_timeout_ms":35000,"msgs":[{"message_id":7,"from_user_id":"member@im.wechat","message_type":1,"context_token":"ctx","item_list":[{"type":1,"text_item":{"text":"hello"}}]}]}`))
	})
	updates, err := client.GetUpdates(context.Background(), "cursor-0", 0)
	if err != nil {
		t.Fatal(err)
	}
	if updates.GetUpdatesBuf != "cursor-1" || len(updates.Msgs) != 1 {
		t.Fatalf("unexpected updates: %+v", updates)
	}
	message := updates.Msgs[0]
	if message.MessageID != 7 || message.FromUserID != "member@im.wechat" || message.MessageType != MessageTypeUser || message.ContextToken != "ctx" {
		t.Fatalf("unexpected message: %+v", message)
	}
	if len(message.ItemList) != 1 || message.ItemList[0].Type != ItemText || message.ItemList[0].TextItem.Text != "hello" {
		t.Fatalf("unexpected items: %+v", message.ItemList)
	}
	if len(*requests) != 1 {
		t.Fatal("expected one long-poll request")
	}
}

func TestLongPollTimeoutIsAnEmptyBatch(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Hang until the caller's context expires; a long-poll with no messages
		// behaves exactly like this.
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	updates, err := client.GetUpdates(ctx, "cursor", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates.Msgs) != 0 || updates.GetUpdatesBuf != "cursor" {
		t.Fatalf("unexpected updates: %+v", updates)
	}
}

func TestSendTextCarriesContextTokenAndClassifiesRefusal(t *testing.T) {
	mode := "ok"
	client, requests := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Msg struct {
				ToUserID     string `json:"to_user_id"`
				ClientID     string `json:"client_id"`
				MessageType  int    `json:"message_type"`
				MessageState int    `json:"message_state"`
				ContextToken string `json:"context_token"`
				ItemList     []struct {
					Type     int `json:"type"`
					TextItem struct {
						Text string `json:"text"`
					} `json:"text_item"`
				} `json:"item_list"`
			} `json:"msg"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Msg.ToUserID != "member@im.wechat" || payload.Msg.ContextToken != "ctx" || payload.Msg.MessageType != MessageTypeBot || payload.Msg.MessageState != MessageStateFinish || payload.Msg.ClientID == "" {
			t.Errorf("unexpected outbound shape: %+v", payload.Msg)
		}
		if len(payload.Msg.ItemList) != 1 || payload.Msg.ItemList[0].Type != ItemText || payload.Msg.ItemList[0].TextItem.Text != "answer" {
			t.Errorf("unexpected items: %+v", payload.Msg.ItemList)
		}
		switch mode {
		case "ok":
			w.Write([]byte(`{"ret":0}`))
		case "throttled":
			w.Write([]byte(`{"ret":-2,"errcode":-2,"errmsg":"prepare failed"}`))
		}
	})
	if err := client.SendText(context.Background(), "member@im.wechat", "answer", "ctx", "client-1"); err != nil {
		t.Fatal(err)
	}
	mode = "throttled"
	err := client.SendText(context.Background(), "member@im.wechat", "answer", "ctx", "client-2")
	var refusal *Error
	if err == nil || !errorsAs(err, &refusal) || refusal.Ret != ErrThrottled {
		t.Fatalf("throttle not classified: %v", err)
	}
	if len(*requests) != 2 {
		t.Fatal("unexpected request count")
	}
	// A send without a context token cannot be associated with a conversation.
	if err := client.SendText(context.Background(), "member@im.wechat", "answer", "", "client-3"); err == nil {
		t.Fatal("missing context token accepted")
	}
	if len(*requests) != 2 {
		t.Fatal("invalid request reached the gateway")
	}
}

func errorsAs(err error, target **Error) bool {
	value, ok := err.(*Error)
	if ok {
		*target = value
	}
	return ok
}

func TestMediaDownloadAndAESDecryption(t *testing.T) {
	key := bytes.Repeat([]byte{5}, 16)
	encrypted := encryptECB(t, key, []byte("picture-bytes"))
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn/download" || r.URL.Query().Get("encrypted_query_param") != "param+with space" {
			t.Errorf("unexpected download url: %s", r.URL.String())
		}
		w.Write(encrypted)
	})
	hexKey := hex.EncodeToString(key)
	data, err := client.Download(context.Background(), "param+with space", hexKey)
	if err != nil || string(data) != "picture-bytes" {
		t.Fatalf("hex key download failed: %v %q", err, data)
	}
	base64Key := base64.StdEncoding.EncodeToString(key)
	data, err = client.Download(context.Background(), "param+with space", base64Key)
	if err != nil || string(data) != "picture-bytes" {
		t.Fatalf("base64 key download failed: %v %q", err, data)
	}
	// Plain (unencrypted) resources are passed through when no key is present.
	if data, err := client.Download(context.Background(), "param+with space", ""); err != nil || !bytes.Equal(data, encrypted) {
		t.Fatalf("plain download failed: %v", err)
	}
	if _, err := DecryptMedia(encrypted, "not-a-key"); err == nil {
		t.Fatal("invalid key accepted")
	}
}

func encryptECB(t *testing.T, key, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	for offset := 0; offset < len(padded); offset += aes.BlockSize {
		block.Encrypt(out[offset:offset+aes.BlockSize], padded[offset:offset+aes.BlockSize])
	}
	return out
}

func TestLoginFlowParsesQRCodeAndStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			if r.URL.Query().Get("bot_type") != DefaultBotType {
				t.Error("missing bot_type")
			}
			w.Write([]byte(`{"qrcode":"qr-key","qrcode_img_content":"https://example.invalid/qr"}`))
		case "/ilink/bot/get_qrcode_status":
			if r.URL.Query().Get("qrcode") != "qr-key" {
				t.Error("qrcode not echoed")
			}
			w.Write([]byte(`{"status":"confirmed","bot_token":"bot-token","ilink_bot_id":"bot-id","baseurl":"https://gateway.example"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	code, err := FetchQRCode(context.Background(), server.URL, "", "")
	if err != nil || code.Qrcode != "qr-key" || !strings.Contains(code.QrcodeImgContent, "qr") {
		t.Fatalf("unexpected QR code: %+v %v", code, err)
	}
	status, err := PollQRStatus(context.Background(), server.URL, code.Qrcode, "")
	if err != nil || status.Status != "confirmed" || status.BotToken != "bot-token" || status.IlinkBotID != "bot-id" || status.BaseURL == "" {
		t.Fatalf("unexpected login status: %+v %v", status, err)
	}
}
