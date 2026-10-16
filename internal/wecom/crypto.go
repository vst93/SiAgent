// Package wecom implements the internal application callback and message protocol.
package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid WeCom callback")

type Cryptor struct {
	token, corp string
	key         []byte
}

func NewCryptor(token, key, corp string) (*Cryptor, error) {
	decoded, err := base64.StdEncoding.DecodeString(key + "=")
	if err != nil || len(key) != 43 || len(decoded) != 32 || len(token) < 1 || len(token) > 256 || strings.ContainsAny(token, "\x00\r\n") || corp == "" {
		return nil, ErrInvalid
	}
	return &Cryptor{token: token, corp: corp, key: decoded}, nil
}
func Signature(token, timestamp, nonce, encrypted string) string {
	parts := []string{token, timestamp, nonce, encrypted}
	slices.Sort(parts)
	digest := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(digest[:])
}
func (c *Cryptor) Decrypt(signature, timestamp, nonce, encrypted string) ([]byte, error) {
	expected := Signature(c.token, timestamp, nonce, encrypted)
	if len(signature) != 40 || subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return nil, ErrInvalid
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 || len(data) > 256*1024 {
		return nil, ErrInvalid
	}
	block, _ := aes.NewCipher(c.key)
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, c.key[:aes.BlockSize]).CryptBlocks(plain, data)
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > 32 || padding > len(plain) {
		return nil, ErrInvalid
	}
	for _, value := range plain[len(plain)-padding:] {
		if int(value) != padding {
			return nil, ErrInvalid
		}
	}
	plain = plain[:len(plain)-padding]
	if len(plain) < 20 {
		return nil, ErrInvalid
	}
	size := uint64(binary.BigEndian.Uint32(plain[16:20]))
	if size > uint64(len(plain)-20) {
		return nil, ErrInvalid
	}
	end := 20 + int(size)
	if subtle.ConstantTimeCompare(plain[end:], []byte(c.corp)) != 1 || !utf8.Valid(plain[20:end]) {
		return nil, ErrInvalid
	}
	return plain[20:end], nil
}
func XML(data []byte, target any) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(target); err != nil {
		return ErrInvalid
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

type Envelope struct {
	XMLName   xml.Name `xml:"xml"`
	Encrypted string   `xml:"Encrypt"`
}
type Message struct {
	XMLName xml.Name `xml:"xml"`
	CorpID  string   `xml:"ToUserName"`
	Member  string   `xml:"FromUserName"`
	Created int64    `xml:"CreateTime"`
	Type    string   `xml:"MsgType"`
	Content string   `xml:"Content"`
	ID      string   `xml:"MsgId"`
	AppID   int64    `xml:"AgentID"`
	ChatID  string   `xml:"ChatId"`
}

func ValidMember(value string) bool {
	return value != "" && !strings.EqualFold(value, "@all") && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsAny(value, "|;\x00\r\n") && strings.TrimSpace(value) == value
}

// Split preserves UTF-8 boundaries and leaves room for part counters below 2048 bytes.
func Split(text string) []string {
	const budget = 1800
	parts := []string{}
	for len(text) > 0 && len(parts) < 64 {
		end := min(budget, len(text))
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
	if text != "" {
		parts[len(parts)-1] = "回复过长，剩余内容未发送。请联系管理员在工作台查看完整回复。"
	}
	return parts
}
