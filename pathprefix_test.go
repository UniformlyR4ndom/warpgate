package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizePathPrefix(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"secret", "/secret", false},
		{"/s3cr3t-._~", "/s3cr3t-._~", false},
		{"/", "", true},
		{"a/b", "", true},
		{"/a/", "", true},
		{"..", "", true},
		{"a b", "", true},
		{"a%20b", "", true},
	}
	for _, tt := range tests {
		got, err := normalizePathPrefix(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("normalizePathPrefix(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestPathPrefixRouting(t *testing.T) {
	s, err := NewServer(Config{Root: t.TempDir(), PathPrefix: "/pfx", NoAuth: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := s.handler()

	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	for _, p := range []string{"/", "/static/", "/static/app.js", "/api/public/settings", "/api/files/list", "/pfxx", "/pfxx/static/", "/x/pfx/static/"} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			rec := do(m, p)
			if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Location") != "" ||
				rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
				t.Errorf("%s %s: got %d %q headers=%v; want blank 200", m, p, rec.Code, rec.Body.String(), rec.Header())
			}
		}
	}

	for _, p := range []string{"/pfx", "/pfx/", "/pfx/static"} {
		rec := do(http.MethodGet, p)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/pfx/static/" {
			t.Errorf("GET %s: got %d Location=%q; want 301 to /pfx/static/", p, rec.Code, rec.Header().Get("Location"))
		}
	}

	if rec := do(http.MethodGet, "/pfx/api/public/settings"); rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Errorf("GET /pfx/api/public/settings: got %d %q", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/pfx/static/app.js"); rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Errorf("GET /pfx/static/app.js: got %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if rec := do(http.MethodGet, "/pfx/%73tatic/app.js"); rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Errorf("GET /pfx/%%73tatic/app.js: got %d, %d bytes", rec.Code, rec.Body.Len())
	}
}
