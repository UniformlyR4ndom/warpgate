package main

import (
	"net/http"
	"testing"

	"warpgate/internal/authhash"
)

func TestAuthCheck(t *testing.T) {
	ts := newSimTestServer(t, Config{Root: t.TempDir(), Key: "secret"})
	good := authhash.Token(testK("secret"))

	for _, tc := range []struct {
		name, method, token string
		want                int
	}{
		{"valid", http.MethodPost, good, http.StatusNoContent},
		{"wrong", http.MethodPost, authhash.Token(testK("other")), http.StatusUnauthorized},
		{"missing", http.MethodPost, "", http.StatusUnauthorized},
		{"GET", http.MethodGet, good, http.StatusMethodNotAllowed},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+"/api/auth/check", nil)
		if tc.token != "" {
			req.Header.Set("X-Auth", tc.token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, res.StatusCode, tc.want)
		}
	}
}

func TestAuthCheckNoAuth(t *testing.T) {
	ts := newSimTestServer(t, Config{Root: t.TempDir(), NoAuth: true})
	res, err := http.Post(ts.URL+"/api/auth/check", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("status %d, want 204", res.StatusCode)
	}
}
