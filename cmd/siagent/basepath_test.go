package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeBasePath(t *testing.T) {
	for input, want := range map[string]string{"": "", "/": "", "/siagent": "/siagent", "/siagent/": "/siagent"} {
		got, err := normalizeBasePath(input)
		if err != nil || got != want {
			t.Fatalf("normalizeBasePath(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"siagent", "/siagent//ui", "/siagent?x=1"} {
		if _, err := normalizeBasePath(input); err == nil {
			t.Fatalf("normalizeBasePath(%q) should fail", input)
		}
	}
}

func TestMountBasePath(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Path", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	handler := mountBasePath("/siagent", next)
	for _, tc := range []struct {
		path, wantPath string
		status         int
	}{
		{"/siagent", "/siagent/", http.StatusMovedPermanently},
		{"/siagent/", "/", http.StatusNoContent},
		{"/siagent/api/models", "/api/models", http.StatusNoContent},
		{"/health/ready", "/health/ready", http.StatusNoContent},
		{"/", "", http.StatusNotFound},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if recorder.Code != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.path, recorder.Code, tc.status)
		}
		if tc.status == http.StatusNoContent && recorder.Header().Get("X-Seen-Path") != tc.wantPath {
			t.Fatalf("%s: path %q, want %q", tc.path, recorder.Header().Get("X-Seen-Path"), tc.wantPath)
		}
	}
}
