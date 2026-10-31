// Package wxcrypt implements the WXBizMsgCrypt callback scheme used by WeCom
// applications: a SHA1 signature over the sorted token/timestamp/nonce plus the
// encrypted payload, and AES-256-CBC with a 43-character EncodingAESKey. The
// receiver identifier (the enterprise CorpID) is bound into the decrypted
// payload, so a callback for another enterprise cannot be replayed here.
package wxcrypt

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

var ErrInvalid = errors.New("invalid callback crypto")

type Cryptor struct {
	token, receiver string
	key             []byte
}

// New validates the callback credentials. The key must be the 43-character
// EncodingAESKey shown by the vendor console; the receiver is the CorpID or
// AppID that the encrypted payload must end with.
func New(token, encodingAESKey, receiver string) (*Cryptor, error) {
	decoded, err := base64.StdEncoding.DecodeString(encodingAESKey + "=")
	if err != nil || len(encodingAESKey) != 43 || len(decoded) != 32 || len(token) < 1 || len(token) > 256 || strings.ContainsAny(token, "\x00\r\n") || receiver == "" {
		return nil, ErrInvalid
	}
	return &Cryptor{token: token, receiver: receiver, key: decoded}, nil
}

// Signature returns the SHA1 digest over the sorted parts. WeCom passes the
// encrypted payload as the fourth part; WeChat's plaintext mode passes none.
func Signature(token, timestamp, nonce string, encrypted ...string) string {
	parts := append([]string{token, timestamp, nonce}, encrypted...)
	slices.Sort(parts)
	digest := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(digest[:])
}

// Verify checks a plaintext-mode signature that covers only token, timestamp
// and nonce. It is package-level because plaintext callbacks need no AES key.
func Verify(token, signature, timestamp, nonce string) bool {
	expected := Signature(token, timestamp, nonce)
	return len(signature) == 40 && subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

// Verify checks a plaintext-mode signature with this cryptor's token.
func (c *Cryptor) Verify(signature, timestamp, nonce string) bool {
	return Verify(c.token, signature, timestamp, nonce)
}

// Decrypt verifies the signature and decrypts one callback payload. The
// plaintext layout is random(16) | message length(4) | message | receiver.
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
	if subtle.ConstantTimeCompare(plain[end:], []byte(c.receiver)) != 1 || !utf8.Valid(plain[20:end]) {
		return nil, ErrInvalid
	}
	return plain[20:end], nil
}

// XML decodes exactly one XML document, rejecting trailing roots so a vendor
// payload cannot smuggle a second message past the parser.
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
