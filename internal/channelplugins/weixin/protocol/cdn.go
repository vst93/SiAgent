package weixin

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const maxMediaBytes = 100 << 20

var hexKeyPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// DecryptMedia turns the encrypted CDN payload of one item into plaintext. The
// gateway uses AES-128-ECB with PKCS#7 padding and hands the key in one of three
// shapes: a raw 16-byte base64 value, base64-wrapped hex, or raw hex.
func DecryptMedia(ciphertext []byte, aesKey string) ([]byte, error) {
	key, err := parseAESKey(aesKey)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("weixin media: ciphertext length %d is not aligned", len(ciphertext))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(ciphertext))
	for offset := 0; offset < len(ciphertext); offset += aes.BlockSize {
		block.Decrypt(plain[offset:offset+aes.BlockSize], ciphertext[offset:offset+aes.BlockSize])
	}
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > aes.BlockSize || padding > len(plain) {
		return nil, fmt.Errorf("weixin media: invalid PKCS#7 padding")
	}
	for _, value := range plain[len(plain)-padding:] {
		if int(value) != padding {
			return nil, fmt.Errorf("weixin media: invalid PKCS#7 padding")
		}
	}
	return plain[:len(plain)-padding], nil
}

func parseAESKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("weixin media: missing aes key")
	}
	if hexKeyPattern.MatchString(value) {
		return hex.DecodeString(value)
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("weixin media: aes key base64: %w", err)
	}
	if len(decoded) == 16 {
		return decoded, nil
	}
	if len(decoded) == 32 && hexKeyPattern.MatchString(string(decoded)) {
		return hex.DecodeString(string(decoded))
	}
	return nil, fmt.Errorf("weixin media: aes key must be 16 raw bytes or 32 hex characters")
}

// Download fetches one encrypted CDN resource. An empty key means the gateway
// served the bytes unencrypted.
func (c *Client) Download(ctx context.Context, encryptedQueryParam, aesKey string) ([]byte, error) {
	endpoint := c.Account.CDNBaseURL + "/download?encrypted_query_param=" + url.QueryEscape(encryptedQueryParam)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMediaBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxMediaBytes {
		return nil, fmt.Errorf("weixin media: resource exceeds %d bytes", maxMediaBytes)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin media: CDN http %d", response.StatusCode)
	}
	if strings.TrimSpace(aesKey) == "" {
		return body, nil
	}
	return DecryptMedia(body, aesKey)
}
