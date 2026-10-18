package main

import (
	"errors"
	"net/http"
	"strings"
)

func normalizeBasePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return "", nil
	}
	if !strings.HasPrefix(value, "/") {
		return "", errors.New("base path must start with /")
	}
	value = strings.TrimRight(value, "/")
	if value == "" || strings.ContainsAny(value, "?#") || strings.Contains(value, "//") {
		return "", errors.New("base path must be a single URL path prefix")
	}
	return value, nil
}

// mountBasePath exposes the service below a prefix. Health probes remain
// available at the root so self-update can always reach the local process.
func mountBasePath(prefix string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/health/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == prefix {
			target := prefix + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			http.NotFound(w, r)
			return
		}
		r = r.Clone(r.Context())
		r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		next.ServeHTTP(w, r)
	})
}
