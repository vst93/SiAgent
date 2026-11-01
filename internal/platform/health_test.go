package platform

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The browser reads readiness from the API namespace so a mounted prefix is
// resolved exactly like every other request. Machine probes keep /health/ready.
func TestAPIHealthReportsReadiness(t *testing.T) {
	for _, ready := range []bool{true, false} {
		fixture := newFixture(t)
		fixture.server.ready = func() bool { return ready }
		recorder := fixture.request(t, http.MethodGet, "/api/health", nil, false, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status %d, want 200", recorder.Code)
		}
		var body struct {
			Status string `json:"status"`
			Ready  bool   `json:"ready"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Ready != ready {
			t.Fatalf("ready = %v, want %v", body.Ready, ready)
		}
		want := "starting"
		if ready {
			want = "ready"
		}
		if body.Status != want {
			t.Fatalf("status = %q, want %q", body.Status, want)
		}
	}
}

// The callback address shown in the channel form is reached from the platform,
// not from the browser, so it follows the configured public address including
// any mount prefix.
func TestAuthStatusExposesPublicAddress(t *testing.T) {
	fixture := newFixture(t)
	fixture.server.publicBase = "https://example.com/si"
	recorder := fixture.request(t, http.MethodGet, "/api/auth/status", nil, false, nil)
	var body struct {
		PublicURL string `json:"public_url"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.PublicURL != "https://example.com/si" {
		t.Fatalf("public_url = %q", body.PublicURL)
	}
}

// The published address may carry the mount prefix, because that is where the
// administrator serves the site; only a query or fragment is meaningless.
func TestPublicAddressAcceptsMountPrefix(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := New(store, &fakeRuntime{}, func() bool { return true }, "https://example.com/si/")
	if err != nil {
		t.Fatalf("a public address with a prefix must be accepted: %v", err)
	}
	if server.publicURL != "https://example.com" || server.publicBase != "https://example.com/si" {
		t.Fatalf("origin %q, base %q", server.publicURL, server.publicBase)
	}
	if !server.secure {
		t.Fatal("https must enable secure cookies")
	}
	for _, rejected := range []string{"https://example.com/si?x=1", "https://example.com/si#top", "ftp://example.com"} {
		if _, err := New(store, &fakeRuntime{}, func() bool { return true }, rejected); err == nil {
			t.Fatalf("%q must be rejected", rejected)
		}
	}
}
