package main

import (
	"io"
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
			http.Redirect(w, r, "/", http.StatusSeeOther)
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

func TestRegisterNotNeeded(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/public/settings" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	}))
	defer ts.Close()

	if err := runRegister([]string{"-url", ts.URL}); err != nil {
		t.Fatalf("register without a firewall: %v", err)
	}
}

func TestAutoRegisterResendsRequest(t *testing.T) {
	registered := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" && r.Method == http.MethodPost:
			registered = true
			http.Redirect(w, r, "/", http.StatusSeeOther)
		case registered:
			_, _ = io.Copy(w, r.Body)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ts.Close()

	g := &globalFlags{url: ts.URL}
	req, _ := g.newRequest(http.MethodPost, "/api/clip/new", strings.NewReader("payload"))
	resp, err := g.httpClient().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "payload" || !g.registered {
		t.Fatalf("got body %q, registered %v", body, g.registered)
	}
}
