// Package channels defines the transport boundary. Providers never access platform
// databases, model credentials or the Agent runtime.
package channels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
	Type     string `json:"type,omitempty"`
	Help     string `json:"help,omitempty"`
}
type Descriptor struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Documentation  string   `json:"documentation,omitempty"`
	Transport      string   `json:"transport"`
	Fields         []Field  `json:"fields"`
	IdentityFields []string `json:"identity_fields"`
	MaxTextBytes   int      `json:"max_text_bytes"`
	Legacy         bool     `json:"legacy,omitempty"`
	// Streaming reports that the same outbound message can be updated in place
	// under a stable stream identifier. It lets the platform acknowledge a
	// message, publish drafts, and finish the answer without extra messages.
	Streaming bool `json:"streaming,omitempty"`
	// Images reports that the adapter can download and supply inbound images.
	// Without it the platform rejects image messages with a text-only notice
	// instead of promising a capability the transport does not have.
	Images bool `json:"images,omitempty"`
}
type Config struct {
	ID       string            `json:"id"`
	Provider string            `json:"provider"`
	Version  int               `json:"version"`
	Values   map[string]string `json:"config"`
	Secrets  map[string]string `json:"secrets"`
	// MediaDir is a private staging directory the platform owns for this channel.
	// A process adapter writes downloaded inbound media there and declares only
	// the base file name, so images never travel inside a protocol frame.
	MediaDir string `json:"media_dir,omitempty"`
}

// IncomingImage is one image the adapter downloaded and decrypted for a single
// inbound message. It carries either inline base64 data or the base name of a
// file staged in Config.MediaDir. The platform verifies the bytes, stores them
// as ordinary chat attachments and deletes a staged file.
type IncomingImage struct {
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
	File string `json:"file,omitempty"`
}
type Incoming struct {
	ChatType     string            `json:"chat_type,omitempty"`
	SenderID     string            `json:"sender_id,omitempty"`
	GroupID      string            `json:"group_id,omitempty"`
	Fingerprint  string            `json:"-"`
	ID           string            `json:"id"`
	Session      string            `json:"session"`
	Kind         string            `json:"kind"`
	Text         string            `json:"text"`
	Images       []IncomingImage   `json:"images,omitempty"`
	ReplyContext map[string]string `json:"reply_context"`
}
type Delivery struct {
	ID           string            `json:"id"`
	InboxID      string            `json:"inbox_id"`
	Target       string            `json:"target"`
	Text         string            `json:"text"`
	Kind         string            `json:"kind"`
	Part         int               `json:"part"`
	ReplyContext map[string]string `json:"reply_context"`
}
type Result struct {
	State   string `json:"state"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type Status struct {
	State   string `json:"state"`
	Message string `json:"message"`
}
type Receive func(context.Context, Incoming) error

// Open must return promptly. Connection establishment runs independently of the
// Agent scheduler. Send reports only accepted, definite failure, or uncertainty.
type Provider interface {
	Descriptor() Descriptor
	Validate(Config) error
	Open(context.Context, Config, Receive) (Connection, error)
}

// Plugin creates a bundled provider without access to platform storage or runtime.
type Plugin func(PluginOptions) (Provider, error)
type PluginOptions struct {
	Node         string
	WorkerScript string
}

type CallbackProvider interface{ CallbackPath(string) string }

// WebhookProvider authenticates vendor callbacks before invoking receive or
// verified. The host persists messages and verification with the config version.
type WebhookProvider interface {
	CallbackProvider
	ServeCallback(http.ResponseWriter, *http.Request, Config, Receive, func(context.Context) error)
}
type CredentialTester interface {
	Test(context.Context, Config) Result
}
type Connection interface {
	Send(context.Context, Delivery) Result
	Status() Status
	Close()
}
type Registry struct{ providers map[string]Provider }

func NewRegistry() *Registry { return &Registry{providers: map[string]Provider{}} }
func (r *Registry) Register(p Provider) error {
	d := p.Descriptor()
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`).MatchString(d.ID) || len(d.Fields) > 32 || len(d.IdentityFields) < 1 || d.Name == "" || len(d.Name) > 256 || d.MaxTextBytes < 256 || d.MaxTextBytes > 20000 {
		return errors.New("invalid channel provider")
	}
	if _, ok := r.providers[d.ID]; ok {
		return fmt.Errorf("duplicate channel provider %s", d.ID)
	}
	seen := map[string]bool{}
	for _, f := range d.Fields {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`).MatchString(f.Key) || seen[f.Key] {
			return errors.New("invalid provider fields")
		}
		seen[f.Key] = true
	}
	for _, key := range d.IdentityFields {
		found := false
		for _, field := range d.Fields {
			if field.Key == key && !field.Secret && field.Required {
				found = true
			}
		}
		if !found {
			return errors.New("provider identity must use required ordinary fields")
		}
	}
	r.providers[d.ID] = p
	return nil
}
func (r *Registry) Get(id string) (Provider, bool) { p, ok := r.providers[id]; return p, ok }
func (r *Registry) Descriptors() []Descriptor {
	out := []Descriptor{}
	for _, p := range r.providers {
		out = append(out, p.Descriptor())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Legacy != out[j].Legacy {
			return !out[i].Legacy
		}
		return out[i].ID < out[j].ID
	})
	return out
}
func ValidateFields(d Descriptor, c Config) (string, error) {
	if c.Values == nil {
		c.Values = map[string]string{}
	}
	if c.Secrets == nil {
		c.Secrets = map[string]string{}
	}
	allowed := map[string]Field{}
	for _, f := range d.Fields {
		allowed[f.Key] = f
		value := c.Values[f.Key]
		if f.Secret {
			value = c.Secrets[f.Key]
		}
		if (f.Required && strings.TrimSpace(value) == "") || len(value) > 8192 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return "", fmt.Errorf("请填写有效的%s。", f.Label)
		}
	}
	for key := range c.Values {
		f, ok := allowed[key]
		if !ok || f.Secret {
			return "", errors.New("渠道包含不支持的普通配置字段。")
		}
	}
	for key := range c.Secrets {
		f, ok := allowed[key]
		if !ok || !f.Secret {
			return "", errors.New("渠道包含不支持的密钥字段。")
		}
	}
	for _, field := range d.Fields {
		if field.Type == "number" && !field.Secret && c.Values[field.Key] != "" {
			n, err := strconv.ParseInt(c.Values[field.Key], 10, 64)
			if err != nil {
				return "", fmt.Errorf("%s须为整数。", field.Label)
			}
			c.Values[field.Key] = strconv.FormatInt(n, 10)
		}
	}
	identity := []string{}
	for _, key := range d.IdentityFields {
		f, ok := allowed[key]
		if !ok || f.Secret || c.Values[key] == "" {
			return "", errors.New("渠道标识无效。")
		}
		identity = append(identity, c.Values[key])
	}
	if len(identity) == 0 {
		return "", errors.New("渠道适配器未声明连接标识。")
	}
	return strings.Join(identity, "\x1f"), nil
}

// SplitText bounds the transport payload without splitting a UTF-8 code point.
func SplitText(text string, limit int) []string {
	parts := []string{}
	for len(text) > 0 && len(parts) < 64 {
		end := min(len(text), limit)
		for end < len(text) && end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		if end == 0 {
			break
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
	if len(parts) == 0 {
		parts = append(parts, "任务已完成，但未产生文本回复。")
	}
	if text != "" {
		parts[len(parts)-1] = "回复过长，剩余内容未发送，请在管理后台查看完整回答。"
	}
	return parts
}
