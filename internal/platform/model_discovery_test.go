package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestModelDiscoveryUsesEditorValuesWithoutSaving(t *testing.T) {
	for _, api := range []string{"openai-completions", "openai-responses", "anthropic-messages"} {
		t.Run(api, func(t *testing.T) {
			headers := make(chan http.Header, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expected := "/gateway/models"
				if api == "anthropic-messages" {
					expected = "/gateway/v1/models"
				}
				if r.URL.Path != expected || r.Method != "GET" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				headers <- r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "z-model"}, {"id": "a-model"}, {"id": "a-model"}, {"id": " "}, {"id": strings.Repeat("x", 201)}}})
			}))
			defer upstream.Close()
			f := newFixture(t)
			f.setup(t)
			f.model(t, "original", "saved-secret")
			before, err := f.store.model()
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"", "editor-secret"} {
				response := f.request(t, "POST", "/api/models/discover", map[string]any{"model_id": before.ID, "api": api, "base_url": upstream.URL + "/gateway/", "api_key": key}, true, nil)
				requireStatus(t, response, 200)
				var result struct {
					Models []string `json:"models"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if strings.Join(result.Models, ",") != "a-model,z-model" {
					t.Fatal(result)
				}
				expected := key
				if key == "" {
					expected = "saved-secret"
				}
				received := <-headers
				if api == "anthropic-messages" {
					if received.Get("X-Api-Key") != expected || received.Get("Anthropic-Version") == "" {
						t.Fatal("missing Anthropic authentication")
					}
				} else if received.Get("Authorization") != "Bearer "+expected {
					t.Fatal("wrong Bearer credential")
				}
				if strings.Contains(response.Body.String(), "secret") {
					t.Fatal("credential exposed")
				}
			}
			after, err := f.store.model()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("discovery changed saved configuration", err)
			}
		})
	}
}
func TestModelDiscoveryHandlesInvalidResponsesAndDoesNotFollowRedirects(t *testing.T) {
	for _, body := range []string{`{"data":[]}`, `{"error":"upstream-secret"}`, `{"data":null}`, `not-json`, strings.Repeat("x", (2<<20)+1)} {
		t.Run("response", func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer upstream.Close()
			f := newFixture(t)
			f.setup(t)
			response := f.request(t, "POST", "/api/models/discover", map[string]any{"base_url": upstream.URL, "api_key": "key"}, true, nil)
			expected := 502
			if body == `{"data":[]}` {
				expected = 200
			}
			requireStatus(t, response, expected)
			if strings.Contains(response.Body.String(), "upstream-secret") {
				t.Fatal("upstream body exposed")
			}
		})
	}
	var redirected atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer origin.Close()
	f := newFixture(t)
	f.setup(t)
	requireStatus(t, f.request(t, "POST", "/api/models/discover", map[string]any{"base_url": origin.URL, "api_key": "secret"}, true, nil), 502)
	if redirected.Load() {
		t.Fatal("followed a credential-bearing redirect")
	}
	requireStatus(t, f.request(t, "POST", "/api/models/discover", map[string]any{"base_url": "file:///tmp", "api_key": "key"}, true, nil), 400)
	requireStatus(t, f.request(t, "POST", "/api/models/discover", map[string]any{"base_url": origin.URL, "api_key": ""}, true, nil), 400)
}
