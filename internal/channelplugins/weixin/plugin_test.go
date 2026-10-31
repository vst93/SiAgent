package weixin

import (
	"context"
	"strings"
	"testing"

	protocol "siagent/internal/channelplugins/weixin/protocol"
	"siagent/internal/channels"
)

func TestDescriptorDeclaresPollingAndImages(t *testing.T) {
	provider := New()
	descriptor := provider.Descriptor()
	if descriptor.ID != "weixin" || descriptor.Transport != "polling" || !descriptor.Images || !descriptor.Login {
		t.Fatalf("unexpected descriptor: %+v", descriptor)
	}
	if len(descriptor.IdentityFields) != 1 || descriptor.IdentityFields[0] != "bot_id" {
		t.Fatalf("identity must stay the ilink bot id: %+v", descriptor.IdentityFields)
	}
	if descriptor.MaxTextBytes < 256 || descriptor.MaxTextBytes > 20000 {
		t.Fatalf("text limit outside the platform bound: %d", descriptor.MaxTextBytes)
	}
}

func TestValidateAcceptsOnlyReachableGatewayAddresses(t *testing.T) {
	provider := New().(*Provider)
	base := channels.Config{
		Values:  map[string]string{"bot_id": "bot-id"},
		Secrets: map[string]string{"token": "private-token"},
	}
	if err := provider.Validate(base); err != nil {
		t.Fatalf("minimal configuration rejected: %v", err)
	}
	for name, values := range map[string]map[string]string{
		"remote http":     {"bot_id": "bot-id", "base_url": "http://gateway.example"},
		"query":           {"bot_id": "bot-id", "base_url": "https://gateway.example?x=1"},
		"missing bot id":  {"bot_id": ""},
		"newline bot id":  {"bot_id": "bot\nid"},
		"cdn remote http": {"bot_id": "bot-id", "cdn_base_url": "http://cdn.example"},
		"route tag":       {"bot_id": "bot-id", "route_tag": "tag\nvalue"},
	} {
		config := channels.Config{Values: values, Secrets: map[string]string{"token": "private-token"}}
		if err := provider.Validate(config); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	loopback := channels.Config{
		Values:  map[string]string{"bot_id": "bot-id", "base_url": "http://127.0.0.1:9999", "cdn_base_url": "http://127.0.0.1:9999/c2c"},
		Secrets: map[string]string{"token": "private-token"},
	}
	if err := provider.Validate(loopback); err != nil {
		t.Fatalf("loopback fixture rejected: %v", err)
	}
}

func TestDescribeItemsFlattensTextVoiceAndQuotes(t *testing.T) {
	text, kind, _ := describeItems([]protocol.Item{{Type: protocol.ItemText, TextItem: &protocol.TextItem{Text: "  问题  "}}})
	if text != "问题" || kind != "text" {
		t.Fatalf("text flattening failed: %q %q", text, kind)
	}
	text, kind, _ = describeItems([]protocol.Item{{Type: protocol.ItemVoice, VoiceItem: &protocol.VoiceItem{Text: "语音转写"}}})
	if text != "语音转写" || kind != "text" {
		t.Fatalf("voice transcript failed: %q %q", text, kind)
	}
	text, kind, _ = describeItems([]protocol.Item{{Type: protocol.ItemVoice, VoiceItem: &protocol.VoiceItem{}}})
	if text != "" || kind != "voice" {
		t.Fatalf("untranscribed voice should keep its type: %q %q", text, kind)
	}
	text, _, _ = describeItems([]protocol.Item{{
		Type:     protocol.ItemText,
		TextItem: &protocol.TextItem{Text: "回答"},
		RefMsg: &protocol.RefMessage{
			Title:       "原消息",
			MessageItem: &protocol.Item{Type: protocol.ItemText, TextItem: &protocol.TextItem{Text: "被引用内容"}},
		},
	}})
	if !strings.Contains(text, "[引用: 原消息 | 被引用内容]") || !strings.HasSuffix(text, "回答") {
		t.Fatalf("quote not preserved: %q", text)
	}
	_, kind, images := describeItems([]protocol.Item{{Type: protocol.ItemImage, ImageItem: &protocol.ImageItem{Media: &protocol.Media{EncryptQueryParam: "p"}}}})
	if kind != "text" || len(images) != 1 {
		t.Fatalf("image not collected: %q %d", kind, len(images))
	}
	_, kind, _ = describeItems([]protocol.Item{{Type: protocol.ItemFile, FileItem: &protocol.FileItem{FileName: "a.pdf"}}})
	if kind != "file" {
		t.Fatalf("file notice not kept: %q", kind)
	}
}

func TestSendWithoutContextTokenFailsBeforeNetwork(t *testing.T) {
	provider := New().(*Provider)
	connection, err := provider.Open(t.Context(), channels.Config{
		ID:       "channel",
		Provider: "weixin",
		Values:   map[string]string{"bot_id": "bot-id", "base_url": "http://127.0.0.1:1"},
		Secrets:  map[string]string{"token": "private-token"},
	}, func(context.Context, channels.Incoming) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	result := connection.Send(t.Context(), channels.Delivery{Target: "member@im.wechat", Text: "answer"})
	if result.State != "failed" || !strings.Contains(result.Message, "上下文") {
		t.Fatalf("missing context token not refused: %+v", result)
	}
}
