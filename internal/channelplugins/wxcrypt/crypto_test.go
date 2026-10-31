package wxcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

func encrypt(t *testing.T, key []byte, receiver, text string) string {
	t.Helper()
	data := append(bytes.Repeat([]byte{7}, 16), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(data[16:], uint32(len(text)))
	data = append(data, []byte(text+receiver)...)
	pad := 32 - len(data)%32
	data = append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(data, data)
	return base64.StdEncoding.EncodeToString(data)
}

func TestCryptorBindsSignatureTimestampAndReceiver(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	encodedKey := base64.RawStdEncoding.EncodeToString(key)
	c, err := New("callback-token", encodedKey, "wx-app")
	if err != nil {
		t.Fatal(err)
	}
	text := "<xml><Content>中文 &amp; text</Content></xml>"
	encrypted := encrypt(t, key, "wx-app", text)
	signature := Signature("callback-token", "1700000000", "nonce", encrypted)
	plain, err := c.Decrypt(signature, "1700000000", "nonce", encrypted)
	if err != nil || string(plain) != text {
		t.Fatal("valid callback rejected")
	}
	if _, err := c.Decrypt(signature, "1700000001", "nonce", encrypted); err == nil {
		t.Fatal("signature bound to another timestamp accepted")
	}
	// A payload encrypted for another AppID must not be replayed into this one.
	foreign := encrypt(t, key, "wx-other", text)
	if _, err := c.Decrypt(Signature("callback-token", "1", "n", foreign), "1", "n", foreign); err == nil {
		t.Fatal("payload for another receiver accepted")
	}
	bad := base64.StdEncoding.EncodeToString([]byte("short"))
	if _, err := c.Decrypt(Signature("callback-token", "1", "n", bad), "1", "n", bad); err == nil {
		t.Fatal("malformed CBC frame accepted")
	}
}

func TestPlaintextSignatureVerification(t *testing.T) {
	c, err := New("callback-token", strings.Repeat("A", 43), "wx-app")
	if err != nil {
		t.Fatal(err)
	}
	// WeChat's plaintext mode signs only token, timestamp and nonce.
	if !c.Verify(Signature("callback-token", "1700000000", "nonce"), "1700000000", "nonce") {
		t.Fatal("valid plaintext signature rejected")
	}
	if c.Verify(Signature("callback-token", "1700000000", "nonce"), "1700000001", "nonce") {
		t.Fatal("plaintext signature bound to another timestamp accepted")
	}
	if c.Verify("", "1700000000", "nonce") {
		t.Fatal("empty signature accepted")
	}
}

func TestCredentialBounds(t *testing.T) {
	key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	for name, test := range map[string]struct{ token, key, receiver string }{
		"short key":     {"token", "short", "app"},
		"empty token":   {"", key, "app"},
		"empty receive": {"token", key, ""},
		"control token": {"tok\nen", key, "app"},
	} {
		if _, err := New(test.token, test.key, test.receiver); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestXMLRejectsTrailingRoot(t *testing.T) {
	var target struct {
		XMLName struct{} `xml:"xml"`
		Value   string   `xml:"Value"`
	}
	if err := XML([]byte("<xml><Value>one</Value></xml><xml/>"), &target); err == nil {
		t.Fatal("extra XML root accepted")
	}
	if err := XML([]byte("<xml><Value>one</Value></xml>"), &target); err != nil || target.Value != "one" {
		t.Fatalf("valid XML rejected: %v", err)
	}
}
