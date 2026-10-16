package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func encrypted(key []byte, corp, text string) string {
	data := append(bytes.Repeat([]byte{7}, 16), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(data[16:], uint32(len(text)))
	data = append(data, []byte(text+corp)...)
	pad := 32 - len(data)%32
	data = append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(data, data)
	return base64.StdEncoding.EncodeToString(data)
}
func TestCallbackCryptoBoundsIdentityAndXML(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	c, err := NewCryptor("token", base64.RawStdEncoding.EncodeToString(key), "wx-corp")
	if err != nil {
		t.Fatal(err)
	}
	text := "<xml><Content>中文 &amp; text</Content></xml>"
	encoded := encrypted(key, "wx-corp", text)
	signature := Signature("token", "1234", "nonce", encoded)
	plain, err := c.Decrypt(signature, "1234", "nonce", encoded)
	if err != nil || string(plain) != text {
		t.Fatal("valid callback rejected")
	}
	if _, err := c.Decrypt(signature, "1234", "changed", encoded); err == nil {
		t.Fatal("invalid signature accepted")
	}
	wrong := encrypted(key, "other-corp", text)
	if _, err := c.Decrypt(Signature("token", "1", "n", wrong), "1", "n", wrong); err == nil {
		t.Fatal("other receiver accepted")
	}
	bad := base64.StdEncoding.EncodeToString([]byte("short"))
	if _, err := c.Decrypt(Signature("token", "1", "n", bad), "1", "n", bad); err == nil {
		t.Fatal("bad CBC frame accepted")
	}
	if XML([]byte(text+"<xml/>"), &Envelope{}) == nil {
		t.Fatal("extra XML root accepted")
	}
	if ValidMember("@all") || ValidMember("one|two") || !ValidMember("member.1") {
		t.Fatal("recipient bounds invalid")
	}
}

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
func TestHTTPClientOutcomeAndTokenCache(t *testing.T) {
	mode := "ok"
	tokens, sends := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			tokens++
			if r.URL.Query().Get("corpsecret") != "private-secret" {
				t.Error("wrong secret")
			}
			w.Write([]byte(`{"errcode":0,"access_token":"private-access","expires_in":7200}`))
			return
		}
		sends++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["touser"] != "member" || body["enable_duplicate_check"] != float64(0) {
			t.Error("wrong outgoing scope")
		}
		switch mode {
		case "ok":
			w.Write([]byte(`{"errcode":0}`))
		case "rate":
			w.Write([]byte(`{"errcode":45009}`))
		case "token":
			w.Write([]byte(`{"errcode":42001}`))
		case "invalid":
			w.Write([]byte(`{"errcode":0,"invaliduser":"member"}`))
		case "bad":
			w.Write([]byte(`{}`))
		case "http":
			w.WriteHeader(502)
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client := NewClient()
	client.HTTP.Transport = redirectTransport{endpoint, http.DefaultTransport}
	a := Application{"channel", "wx-corp", 7, "private-secret"}
	for _, test := range []struct{ mode, state string }{{"ok", "sent"}, {"rate", "retry"}, {"invalid", "failed"}, {"bad", "unknown"}, {"http", "unknown"}, {"token", "retry"}, {"ok", "sent"}} {
		mode = test.mode
		result := client.Send(context.Background(), a, "member", "hello")
		if result.State != test.state {
			t.Fatalf("%s: %+v", mode, result)
		}
		if strings.Contains(result.Message, "private-") {
			t.Fatal("credential leaked")
		}
	}
	if tokens != 2 || sends != 7 {
		t.Fatalf("token cache/refresh incorrect: %d/%d", tokens, sends)
	}
	if result := client.Send(context.Background(), a, "@all", "bad"); result.State != "failed" || sends != 7 {
		t.Fatal("broadcast recipient reached API")
	}
}
func TestMessageSplitUTF8BoundedAndTruncationExplicit(t *testing.T) {
	text := strings.Repeat("中文🙂", 2000)
	parts := Split(text)
	if strings.Join(parts, "") != text {
		t.Fatal("split lost text")
	}
	for _, part := range parts {
		if len(part) > 1800 || !utf8.ValidString(part) {
			t.Fatal("invalid split")
		}
	}
	huge := Split(strings.Repeat("x", 200000))
	if len(huge) != 64 || !strings.Contains(huge[63], "剩余内容未发送") {
		t.Fatal("unbounded or silent truncation")
	}
}
func TestTokenRequestCancellationIsSafeToRetry(t *testing.T) {
	client := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if result := client.Send(ctx, Application{"id", "corp", 1, "secret"}, "member", "text"); result.State != "retry" {
		t.Fatal(result)
	}
}
