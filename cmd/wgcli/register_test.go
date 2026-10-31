package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeFirewall mimics warpgate's IP firewall: blank page until a POST to
// /reg, then a 303 to the UI and normal service.
func fakeFirewall(t *testing.T) *httptest.Server {
	registered := false
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/reg" && r.Method == http.MethodPost:
			registered = true
			http.Redirect(w, r, "/static/", http.StatusSeeOther)
		case registered:
			_, _ = w.Write([]byte("[]"))
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
		}
	}))
}

func TestRegisterAndBlankPage(t *testing.T) {
	ts := fakeFirewall(t)
	defer ts.Close()

	g := &globalFlags{url: ts.URL}
	get := func() error {
		req, _ := g.newRequest(http.MethodGet, "/api/files/ul-list", nil)
		resp, err := g.httpClient().Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(); err == nil || !strings.Contains(err.Error(), "blank page") {
		t.Fatalf("before registration: err = %v, want blank page hint", err)
	}
	if err := runRegister([]string{"-url", ts.URL}); err == nil {
		t.Fatal("register at the wrong URL: expected error")
	}
	if err := runRegister([]string{"-url", ts.URL, ts.URL + "/reg"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := get(); err != nil {
		t.Fatalf("after registration: %v", err)
	}
}
