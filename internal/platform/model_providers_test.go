package platform

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderPacksGateOfficialPresets(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	response := f.request(t, "GET", "/api/model-providers", nil, true, nil)
	requireStatus(t, response, 200)
	var list struct {
		Presets []ProviderPreset `json:"presets"`
		Packs   []ProviderPack   `json:"packs"`
	}
	json.Unmarshal(response.Body.Bytes(), &list)
	if len(list.Presets) == 0 || len(list.Packs) != 2 {
		t.Fatalf("unexpected provider catalog: %s", response.Body.String())
	}
	for _, preset := range list.Presets {
		if preset.Group == "official" && preset.Available {
			t.Fatalf("official preset available before install: %+v", preset)
		}
		if preset.Group == "base" && !preset.Available {
			t.Fatalf("base preset unavailable: %+v", preset)
		}
	}

	response = f.request(t, "POST", "/api/model-providers/packs/pi-providers/install", nil, true, nil)
	requireStatus(t, response, 200)
	json.Unmarshal(response.Body.Bytes(), &list)
	available := 0
	for _, preset := range list.Presets {
		if preset.Group == "official" && preset.Available {
			available++
		}
	}
	if available == 0 {
		t.Fatal("official presets still locked after install")
	}

	// A saved connection can now use an official provider.
	response = f.request(t, "POST", "/api/models", map[string]any{
		"base_url": "https://api.deepseek.com/v1",
		"name":     "deepseek-chat",
		"api_key":  "secret",
		"provider": "deepseek",
		"options":  map[string]any{"api": "openai-completions"},
	}, true, nil)
	requireStatus(t, response, 201)

	// The pack cannot be removed while a connection still uses it.
	requireStatus(t, f.request(t, "DELETE", "/api/model-providers/packs/pi-providers", nil, true, nil), 409)
}

func TestSaveModelRejectsUnavailableOrMismatchedProvider(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	requireStatus(t, f.request(t, "POST", "/api/models", map[string]any{
		"base_url": "https://api.deepseek.com/v1",
		"name":     "deepseek-chat",
		"api_key":  "secret",
		"provider": "deepseek",
		"options":  map[string]any{"api": "openai-completions"},
	}, true, nil), 409)
	requireStatus(t, f.request(t, "POST", "/api/models", map[string]any{
		"base_url": "https://api.anthropic.com",
		"name":     "claude",
		"api_key":  "secret",
		"provider": "anthropic",
		"options":  map[string]any{"api": "openai-completions"},
	}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/models", map[string]any{
		"base_url": "https://api.openai.com/v1",
		"name":     "gpt",
		"api_key":  "secret",
		"provider": "unknown",
		"options":  map[string]any{"api": "openai-completions"},
	}, true, nil), 400)
}

func TestDispatchRejectsUnloadedProvider(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	requireStatus(t, f.request(t, "POST", "/api/model-providers/packs/pi-providers/install", nil, true, nil), 200)
	response := f.request(t, "POST", "/api/models", map[string]any{
		"base_url": "http://localhost:9999/v1",
		"name":     "local",
		"api_key":  "secret",
		"provider": "ollama",
		"options":  map[string]any{"api": "openai-completions"},
	}, true, nil)
	requireStatus(t, response, 201)
	var model Model
	json.Unmarshal(response.Body.Bytes(), &model)
	if model.Provider != "ollama" {
		t.Fatalf("provider not persisted: %+v", model)
	}
	// Point the default agent at it, then unload the pack behind the scenes.
	if _, err := f.store.db.Exec("UPDATE agents SET model_id=? WHERE id='default'", model.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec("DELETE FROM provider_packs"); err != nil {
		t.Fatal(err)
	}
	conversation := f.conversation(t)
	response = f.request(t, "POST", "/api/conversations/"+conversation+"/runs", map[string]string{"input": "hello"}, true, nil)
	requireStatus(t, response, 409)
	if !strings.Contains(response.Body.String(), "供应商扩展未加载") {
		t.Fatalf("unexpected message: %s", response.Body.String())
	}
}

func TestProviderCatalogRequiresAuth(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	requireStatus(t, f.request(t, "GET", "/api/model-providers", nil, false, nil), 401)
	requireStatus(t, f.request(t, "POST", "/api/model-providers/packs/pi-providers/install", nil, false, nil), 401)
}

func TestModelListExposesProvider(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "key")
	response := f.request(t, "GET", "/api/models", nil, true, nil)
	requireStatus(t, response, 200)
	var models []Model
	json.Unmarshal(response.Body.Bytes(), &models)
	if len(models) != 1 || models[0].Provider != "openai" {
		t.Fatalf("provider missing from list: %s", response.Body.String())
	}
}
