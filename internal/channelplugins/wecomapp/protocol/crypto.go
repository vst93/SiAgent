// Package wecom implements the internal application callback and message protocol.
package wecom

import (
	"encoding/xml"
	"strings"
	"unicode/utf8"

	"siagent/internal/channelplugins/wxcrypt"
)

var ErrInvalid = wxcrypt.ErrInvalid

// Cryptor is the shared WXBizMsgCrypt implementation. WeCom binds the
// encrypted payload to the enterprise CorpID.
type Cryptor = wxcrypt.Cryptor

func NewCryptor(token, key, corp string) (*Cryptor, error) {
	return wxcrypt.New(token, key, corp)
}

func Signature(token, timestamp, nonce, encrypted string) string {
	return wxcrypt.Signature(token, timestamp, nonce, encrypted)
}

func XML(data []byte, target any) error {
	return wxcrypt.XML(data, target)
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
