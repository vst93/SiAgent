package platform

import (
	"encoding/json"
	"testing"
)

func TestBundledAdaptersExposeRecommendedOrderAndCapabilities(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	response := f.request(t, "GET", "/api/channel-providers", nil, true, nil)
	requireStatus(t, response, 200)
	var providers []struct {
		ID        string `json:"id"`
		Transport string `json:"transport"`
		Streaming bool   `json:"streaming"`
		Images    bool   `json:"images"`
		Legacy    bool   `json:"legacy"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &providers); err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct {
		ID        string `json:"id"`
		Transport string `json:"transport"`
		Streaming bool   `json:"streaming"`
		Images    bool   `json:"images"`
		Legacy    bool   `json:"legacy"`
	}{}
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	for _, id := range []string{"wecom-bot", "feishu-bot", "qqbot", "weixin", "wecom-app"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("bundled adapter %s missing: %+v", id, providers)
		}
	}
	if !byID["feishu-bot"].Streaming || !byID["feishu-bot"].Images || byID["feishu-bot"].Transport != "websocket" {
		t.Fatalf("Feishu capabilities incorrect: %+v", byID["feishu-bot"])
	}
	if byID["qqbot"].Transport != "websocket" || byID["qqbot"].Streaming || !byID["qqbot"].Images {
		t.Fatalf("QQ capabilities incorrect: %+v", byID["qqbot"])
	}
	if byID["weixin"].Transport != "polling" || byID["weixin"].Streaming || !byID["weixin"].Images {
		t.Fatalf("personal-WeChat capabilities incorrect: %+v", byID["weixin"])
	}
	// The recommended WeCom bot stays first so the console's default selection
	// and existing operator habits do not change.
	if providers[0].ID != "wecom-bot" {
		t.Fatalf("recommended adapter order changed: %+v", providers)
	}
}
