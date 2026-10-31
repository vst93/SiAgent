package feishubot

import (
	"strings"
	"testing"

	"siagent/internal/channels"
)

func TestValidateRejectsUnusableCredentialsBeforeConnecting(t *testing.T) {
	provider, err := New("node", "runtime/dist/plugins/feishu-bot/worker.js")
	if err != nil {
		t.Fatal(err)
	}
	valid := channels.Config{
		Values:  map[string]string{"app_id": "cli_0123456789abcdef"},
		Secrets: map[string]string{"app_secret": "app-secret"},
	}
	if err := provider.Validate(valid); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	lark := channels.Config{
		Values:  map[string]string{"app_id": "cli_0123456789abcdef", "domain": "https://open.larksuite.com"},
		Secrets: map[string]string{"app_secret": "app-secret"},
	}
	if err := provider.Validate(lark); err != nil {
		t.Fatalf("Lark domain rejected: %v", err)
	}
	for name, test := range map[string]channels.Config{
		// The official SDK refuses these before opening a connection, so the
		// console reports them while the administrator is still editing.
		"missing app id": {Secrets: map[string]string{"app_secret": "app-secret"}},
		"short app id":   {Values: map[string]string{"app_id": "cli_1234"}, Secrets: map[string]string{"app_secret": "app-secret"}},
		"non-hex app id": {Values: map[string]string{"app_id": "cli_0123456789abcdeg"}, Secrets: map[string]string{"app_secret": "app-secret"}},
		"missing secret": {Values: map[string]string{"app_id": "cli_0123456789abcdef"}},
		"foreign domain": {Values: map[string]string{"app_id": "cli_0123456789abcdef", "domain": "https://example.invalid"}, Secrets: map[string]string{"app_secret": "app-secret"}},
		"unknown field":  {Values: map[string]string{"app_id": "cli_0123456789abcdef", "extra": "x"}, Secrets: map[string]string{"app_secret": "app-secret"}},
	} {
		if err := provider.Validate(test); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestDescriptorDeclaresLongConnectionCapabilities(t *testing.T) {
	provider, err := New("node", "runtime/dist/plugins/feishu-bot/worker.js")
	if err != nil {
		t.Fatal(err)
	}
	descriptor := provider.Descriptor()
	if descriptor.ID != "feishu-bot" || descriptor.Transport != "websocket" || !descriptor.Streaming || !descriptor.Images {
		t.Fatalf("unexpected descriptor: %+v", descriptor)
	}
	if len(descriptor.IdentityFields) != 1 || descriptor.IdentityFields[0] != "app_id" {
		t.Fatalf("identity must stay the App ID: %+v", descriptor.IdentityFields)
	}
	if descriptor.MaxTextBytes < 256 || descriptor.MaxTextBytes > 20000 {
		t.Fatalf("text limit outside the platform bound: %d", descriptor.MaxTextBytes)
	}
	if !strings.HasSuffix(provider.(*Provider).Script, "/plugins/feishu-bot/worker.js") {
		t.Fatalf("worker entry not resolved beside the channel worker: %s", provider.(*Provider).Script)
	}
}

func TestWorkerEntryFollowsTheBundledChannelDirectory(t *testing.T) {
	options := channels.PluginOptions{Node: "node", WorkerScript: "/opt/siagent/runtime/dist/channel-wecom.js"}
	if got := options.Worker("feishu-bot"); got != "/opt/siagent/runtime/dist/plugins/feishu-bot/worker.js" {
		t.Fatalf("unexpected bundled worker path: %s", got)
	}
	if got := (channels.PluginOptions{}).Worker("feishu-bot"); got != "" {
		t.Fatalf("empty channel worker must not invent a path: %s", got)
	}
}
