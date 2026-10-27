package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestParseIPList(t *testing.T) {
	got, err := parseIPList("198.51.100.7, 203.0.113.9/24,2001:db8::/32,::ffff:192.0.2.1,::ffff:192.0.2.0/120")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"198.51.100.7/32", "203.0.113.0/24", "2001:db8::/32", "192.0.2.1/32", "192.0.2.0/24"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("entry %d: got %s, want %s", i, got[i], want[i])
		}
	}
	for _, bad := range []string{"1.2.3", "1.2.3.4/33", "a,b", "1.2.3.4,", "1.2.3.4:80"} {
		if _, err := parseIPList(bad); err == nil {
			t.Errorf("parseIPList(%q): expected error", bad)
		}
	}
}

func TestClientIP(t *testing.T) {
	trusted, _ := parseIPList("10.0.0.0/8")
	tests := []struct {
		remote string
		xff    []string
		want   string
	}{
		{"198.51.100.7:1234", nil, "198.51.100.7"},
		{"198.51.100.7:1234", []string{"203.0.113.1"}, "198.51.100.7"}, // untrusted peer: header ignored
		{"[::ffff:198.51.100.7]:1234", nil, "198.51.100.7"},
		{"10.0.0.1:1234", nil, "10.0.0.1"},
		{"10.0.0.1:1234", []string{"203.0.113.1"}, "203.0.113.1"},
		{"10.0.0.1:1234", []string{"6.6.6.6, 203.0.113.1, 10.0.0.2"}, "203.0.113.1"},
		{"10.0.0.1:1234", []string{"6.6.6.6", "203.0.113.1"}, "203.0.113.1"},
		{"10.0.0.1:1234", []string{"garbage, 10.0.0.2"}, "10.0.0.1"},
		{"10.0.0.1:1234", []string{"10.0.0.3, 10.0.0.2"}, "10.0.0.1"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tt.remote
		for _, v := range tt.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := clientIP(r, trusted).String(); got != tt.want {
			t.Errorf("remote %s xff %v: got %s, want %s", tt.remote, tt.xff, got, tt.want)
		}
	}
}

func TestNewIPFilterConfig(t *testing.T) {
	bad := []Config{
		{IPRegMax: 2},                       // base-path registration needs a prefix
		{IPRegWindow: time.Minute},          // same
		{IPRegPath: "static"},               // collides with UI
		{IPRegPath: "a/b"},                  // not a single segment
		{IPAllow: "1.2.3.4/40"},             // bad CIDR
		{IPRegPath: "reg", IPRegMax: -1},    // negative
		{IPRegPath: "reg", IPRegWindow: -1}, // negative
	}
	for _, cfg := range bad {
		if _, err := newIPFilter(cfg); err == nil {
			t.Errorf("newIPFilter(%+v): expected error", cfg)
		}
	}
	if f, err := newIPFilter(Config{}); err != nil || f != nil {
		t.Errorf("empty config: got %v, %v; want disabled", f, err)
	}
	if f, err := newIPFilter(Config{PathPrefix: "/pfx", IPRegMax: 1}); err != nil || f.regPath != "/" {
		t.Errorf("base-path registration: got %+v, %v", f, err)
	}
}

type fwClient struct {
	t *testing.T
	h http.Handler
}

func (c fwClient) do(method, path, ip string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = net.JoinHostPort(ip, "40000")
	c.h.ServeHTTP(rec, r)
	return rec
}

func (c fwClient) expectBlank(method, path, ip string) {
	c.t.Helper()
	rec := c.do(method, path, ip)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || len(rec.Header()) != 1 ||
		rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		c.t.Errorf("%s %s from %s: got %d %q headers=%v; want blank", method, path, ip, rec.Code, rec.Body.String(), rec.Header())
	}
}

func (c fwClient) expectRegPage(path, ip string) {
	c.t.Helper()
	rec := c.do(http.MethodGet, path, ip)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<form method="post">`) {
		c.t.Errorf("GET %s from %s: got %d %q; want registration page", path, ip, rec.Code, rec.Body.String())
	}
}

func (c fwClient) expectRedirect(method, path, ip, to string) {
	c.t.Helper()
	rec := c.do(method, path, ip)
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
		c.t.Errorf("%s %s from %s: got %d Location=%q; want redirect to %s", method, path, ip, rec.Code, rec.Header().Get("Location"), to)
	}
}

func (c fwClient) expectServed(path, ip string) {
	c.t.Helper()
	rec := c.do(http.MethodGet, path, ip)
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		c.t.Errorf("GET %s from %s: got %d, %d bytes; want content", path, ip, rec.Code, rec.Body.Len())
	}
}

func TestIPFilterRegistration(t *testing.T) {
	s, err := NewServer(Config{
		Root: t.TempDir(), NoAuth: true, PathPrefix: "/pfx",
		IPAllow: "192.0.2.0/24", IPRegPath: "reg", IPRegMax: 2, IPRegWindow: time.Minute,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.ipFilter.now = func() time.Time { return now }
	s.ipFilter.regUntil = now.Add(time.Minute)
	c := fwClient{t, s.handler()}

	const a, b, d = "198.51.100.1", "198.51.100.2", "198.51.100.3"

	c.expectServed("/pfx/static/app.js", "192.0.2.77") // allowlisted
	c.expectBlank(http.MethodGet, "/pfx/static/app.js", a)
	c.expectBlank(http.MethodGet, "/pfx/api/public/settings", a)
	c.expectBlank(http.MethodGet, "/pfx", a)
	c.expectBlank(http.MethodGet, "/reg", a) // registration path is under the prefix
	c.expectBlank(http.MethodPut, "/pfx/reg", a)

	c.expectRegPage("/pfx/reg", a)
	c.expectBlank(http.MethodGet, "/pfx/static/app.js", a) // GET alone doesn't register
	c.expectRedirect(http.MethodPost, "/pfx/reg", a, "/pfx/static/")
	c.expectServed("/pfx/static/app.js", a)
	c.expectServed("/pfx/api/public/settings", a)
	c.expectRedirect(http.MethodGet, "/pfx", a, "/pfx/static/")
	c.expectRedirect(http.MethodGet, "/pfx/reg", a, "/pfx/static/")
	c.expectRedirect(http.MethodPost, "/pfx/reg", a, "/pfx/static/") // re-registration doesn't use a slot

	c.expectRedirect(http.MethodPost, "/pfx/reg", b, "/pfx/static/")
	c.expectBlank(http.MethodGet, "/pfx/reg", d) // limit reached
	c.expectBlank(http.MethodPost, "/pfx/reg", d)
	c.expectBlank(http.MethodGet, "/pfx/static/app.js", d)
	c.expectServed("/pfx/static/app.js", b)

	// Window expiry: with a free slot, registration still closes.
	delete(s.ipFilter.registered, netip.MustParseAddr(b))
	c.expectRegPage("/pfx/reg", d)
	now = now.Add(time.Minute)
	c.expectBlank(http.MethodGet, "/pfx/reg", d)
	c.expectBlank(http.MethodPost, "/pfx/reg", d)
	c.expectServed("/pfx/static/app.js", a) // existing registrations survive the window
}

func TestIPFilterBasePathRegistration(t *testing.T) {
	s, err := NewServer(Config{Root: t.TempDir(), NoAuth: true, PathPrefix: "/pfx", IPRegMax: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := fwClient{t, s.handler()}
	const a, b, d = "198.51.100.1", "2001:db8::1", "198.51.100.3"

	c.expectBlank(http.MethodGet, "/", a)
	c.expectRegPage("/pfx", a)
	c.expectRegPage("/pfx/", a)
	c.expectRegPage("/pfx/static/", a)
	c.expectBlank(http.MethodGet, "/pfx/static", a)
	c.expectBlank(http.MethodGet, "/pfx/static/app.js", a)
	c.expectBlank(http.MethodGet, "/pfx/static/index.html", a)
	c.expectRedirect(http.MethodPost, "/pfx/", a, "/pfx/static/")
	c.expectRedirect(http.MethodGet, "/pfx", a, "/pfx/static/")
	c.expectServed("/pfx/static/", a)
	c.expectServed("/pfx/static/app.js", a)

	c.expectRedirect(http.MethodPost, "/pfx/static/", b, "/pfx/static/")
	c.expectServed("/pfx/static/", b)

	c.expectBlank(http.MethodGet, "/pfx/static/", d) // limit reached
	c.expectBlank(http.MethodPost, "/pfx/static/", d)
	c.expectBlank(http.MethodGet, "/pfx", d)
}

func TestIPFilterDedicatedPathOnly(t *testing.T) {
	s, err := NewServer(Config{Root: t.TempDir(), NoAuth: true, PathPrefix: "/pfx", IPRegPath: "reg"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := fwClient{t, s.handler()}
	const a = "198.51.100.1"

	c.expectBlank(http.MethodGet, "/pfx", a)
	c.expectBlank(http.MethodGet, "/pfx/static/", a)
	c.expectBlank(http.MethodPost, "/pfx/static/", a)
	c.expectRegPage("/pfx/reg", a)
	c.expectRedirect(http.MethodPost, "/pfx/reg", a, "/pfx/static/")
	c.expectServed("/pfx/static/", a)
}
