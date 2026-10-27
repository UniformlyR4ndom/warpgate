package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var pathSegment = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// normalizePathSegment turns a flag value naming one path segment ("seg" or
// "/seg") into "/seg". An empty value stays empty.
func normalizePathSegment(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	seg := strings.TrimPrefix(p, "/")
	if !pathSegment.MatchString(seg) || seg == "." || seg == ".." {
		return "", fmt.Errorf("must be a single path segment of [A-Za-z0-9._~-]")
	}
	return "/" + seg, nil
}

// writeBlank is the response for every request the server refuses to
// acknowledge; it must look the same regardless of why it was refused.
func writeBlank(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
}

// withPathPrefix serves h only for paths under prefix (stripped before h sees
// them); every other request gets a blank page.
func withPathPrefix(prefix string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok || (rest != "" && rest[0] != '/') {
			writeBlank(w)
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
