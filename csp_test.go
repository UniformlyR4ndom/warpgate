package main

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestCSPHeaderOnUIAndAPI(t *testing.T) {
	s, err := NewServer(Config{Root: t.TempDir(), NoAuth: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := s.handler()
	for _, p := range []string{"/static/", "/static/app/main.js", "/api/public/settings"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Errorf("GET %s: CSP = %q", p, got)
		}
	}
}

// The registration page's inline script must match the hash its CSP allows.
func TestRegPageCSPAllowsItsScript(t *testing.T) {
	m := regexp.MustCompile(`<script>(.*?)</script>`).FindStringSubmatch(regPageHTML)
	if m == nil {
		t.Fatal("no inline script in regPageHTML")
	}
	sum := sha256.Sum256([]byte(m[1]))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(regPageCSP, want) {
		t.Errorf("regPageCSP %q lacks %s", regPageCSP, want)
	}
}
