package main

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"warpgate/internal/authhash"
)

// fakeServer blocks with the blank page until a POST to "/", then serves
// settings and accepts list requests carrying the token for k.
func fakeServer(t *testing.T, k []byte) *httptest.Server {
	registered := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !registered && r.URL.Path == "/" && r.Method == http.MethodPost:
			registered = true
			http.Redirect(w, r, "/", http.StatusSeeOther)
		case !registered:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/public/settings":
			_, _ = w.Write([]byte(`{"uploads":true,"ulFlat":true}`))
		case r.URL.Path == "/api/files/list" && r.Header.Get("X-Auth") == authhash.Token(k):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestStatus(t *testing.T) {
	k := bytes.Repeat([]byte{3}, authhash.KeySize)
	ts := fakeServer(t, k)
	if err := runStatus([]string{"-url", ts.URL}); err == nil || !strings.Contains(err.Error(), "requires a key") {
		t.Errorf("without a key: err = %v", err)
	}
	wrong := bytes.Repeat([]byte{4}, authhash.KeySize)
	if err := runStatus([]string{"-url", ts.URL, "-auth-k", hex.EncodeToString(wrong)}); err == nil || !strings.Contains(err.Error(), "rejected the key") {
		t.Errorf("wrong key: err = %v", err)
	}
	if err := runStatus([]string{"-url", ts.URL, "-auth-k", hex.EncodeToString(k)}); err != nil {
		t.Errorf("right key: %v", err)
	}
}
