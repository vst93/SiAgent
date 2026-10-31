package qqbot

import (
	"strings"
	"testing"

	"siagent/internal/channels"
)

func TestDescriptorDeclaresOfficialQQLongConnection(t *testing.T) {
	provider, err := New("node", "runtime/dist/plugins/qqbot/worker.js")
	if err != nil {
		t.Fatal(err)
	}
	descriptor := provider.Descriptor()
	if descriptor.ID != "qqbot" || descriptor.Transport != "websocket" || !descriptor.Images || descriptor.Streaming {
		t.Fatalf("unexpected descriptor: %+v", descriptor)
	}
	if len(descriptor.IdentityFields) != 1 || descriptor.IdentityFields[0] != "app_id" {
		t.Fatalf("identity must stay the AppID: %+v", descriptor.IdentityFields)
	}
	if !strings.HasSuffix(provider.(*Provider).Script, "/plugins/qqbot/worker.js") {
		t.Fatalf("worker entry not resolved beside the channel worker: %s", provider.(*Provider).Script)
	}
}

func TestValidateRejectsUnusableApplications(t *testing.T) {
	provider, err := New("node", "runtime/dist/plugins/qqbot/worker.js")
	if err != nil {
		t.Fatal(err)
	}
	valid := channels.Config{
		Values:  map[string]string{"app_id": "1020304050"},
		Secrets: map[string]string{"app_secret": "private-secret"},
	}
	if err := provider.Validate(valid); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}
	sandbox := channels.Config{
		Values:  map[string]string{"app_id": "1020304050", "environment": "sandbox"},
		Secrets: map[string]string{"app_secret": "private-secret"},
	}
	if err := provider.Validate(sandbox); err != nil {
		t.Fatalf("sandbox environment rejected: %v", err)
	}
	for name, config := range map[string]channels.Config{
		"missing app id": {Secrets: map[string]string{"app_secret": "private-secret"}},
		"non-numeric id": {Values: map[string]string{"app_id": "cli_abc"}, Secrets: map[string]string{"app_secret": "private-secret"}},
		"missing secret": {Values: map[string]string{"app_id": "1020304050"}},
		"unknown env":    {Values: map[string]string{"app_id": "1020304050", "environment": "staging"}, Secrets: map[string]string{"app_secret": "private-secret"}},
		"unknown field":  {Values: map[string]string{"app_id": "1020304050", "extra": "x"}, Secrets: map[string]string{"app_secret": "private-secret"}},
		"newline in app": {Values: map[string]string{"app_id": "1020\n304050"}, Secrets: map[string]string{"app_secret": "private-secret"}},
	} {
		if err := provider.Validate(config); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
