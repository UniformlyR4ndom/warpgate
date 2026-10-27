package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var pathPrefixSegment = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// normalizePathPrefix turns a -path-prefix value ("secret" or "/secret") into
// "/secret". An empty value disables the prefix.
func normalizePathPrefix(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	seg := strings.TrimPrefix(p, "/")
	if !pathPrefixSegment.MatchString(seg) || seg == "." || seg == ".." {
		return "", fmt.Errorf("must be a single path segment of [A-Za-z0-9._~-]")
	}
	return "/" + seg, nil
}

// withPathPrefix serves h only for paths under prefix (stripped before h sees
// them); every other request gets the same empty 200 so the server is hard
// to fingerprint.
func withPathPrefix(prefix string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok || (rest != "" && rest[0] != '/') {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		if rest == "" {
			rest = "/"
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = rest
		r2.URL.RawPath = ""
		h.ServeHTTP(w, r2)
	})
}
