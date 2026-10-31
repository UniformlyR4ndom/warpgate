package main

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestFlagSet() (*flag.FlagSet, *globalFlags, *string) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	out := fs.String("o", "", "")
	return fs, g, out
}

func TestParseFlagsInterspersed(t *testing.T) {
	fs, g, out := newTestFlagSet()
	if err := parseFlags(fs, g, []string{"remote/file", "-o", "x.bin", "-insecure", "-url=http://h:1", "--", "-dash"}); err != nil {
		t.Fatal(err)
	}
	if *out != "x.bin" || !g.insecure || g.url != "http://h:1" {
		t.Errorf("flags: o=%q insecure=%v url=%q", *out, g.insecure, g.url)
	}
	if got := fs.Args(); !slices.Equal(got, []string{"remote/file", "-dash"}) {
		t.Errorf("positional args = %q", got)
	}
}

func TestParseFlagsEnvFallback(t *testing.T) {
	t.Setenv("WGCLI_URL", "http://env:2")
	t.Setenv("WGCLI_AUTH_KEY", "envkey")
	t.Setenv("WGCLI_PARAM_CRYPT", "true")

	fs, g, _ := newTestFlagSet()
	if err := parseFlags(fs, g, []string{"-url", "http://flag:3"}); err != nil {
		t.Fatal(err)
	}
	if g.url != "http://flag:3" || g.authKey != "envkey" || !g.paramCrypt {
		t.Errorf("got url=%q authKey=%q paramCrypt=%v", g.url, g.authKey, g.paramCrypt)
	}

	t.Setenv("WGCLI_INSECURE", "maybe")
	fs, g, _ = newTestFlagSet()
	if err := parseFlags(fs, g, nil); err == nil {
		t.Error("invalid WGCLI_INSECURE: expected error")
	}
}

func TestParseFlagsCertNeedsKey(t *testing.T) {
	fs, g, _ := newTestFlagSet()
	if err := parseFlags(fs, g, []string{"-cert", "c.pem"}); err == nil {
		t.Error("-cert without -cert-key: expected error")
	}
}

func TestBuildTransformArgsRandomKeys(t *testing.T) {
	a, err := buildTransformArgs("aes-ctr", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := buildTransformArgs("aes-ctr", "", "", "", "")
	if string(a) == string(b) {
		t.Errorf("random keys repeat: %s", a)
	}
	fixed, _ := buildTransformArgs("xor", "00ff", "", "", "")
	if string(fixed) != `{"key":"00ff"}` {
		t.Errorf("explicit key: %s", fixed)
	}
}

func TestClipMatches(t *testing.T) {
	n := clipNoteMeta{Title: "Login Findings", Attachments: []clipAttachmentMeta{{Caption: "Reset Page"}}}
	cases := []struct {
		q, scope string
		want     bool
	}{
		{"login", "all", true},
		{"login", "content", false},
		{"reset page", "content", true},
		{"reset page", "title", false},
		{"TOKEN", "all", true},
		{"missing", "all", false},
	}
	for _, c := range cases {
		if got := clipMatches(n, "session token here", c.q, c.scope); got != c.want {
			t.Errorf("clipMatches(%q, %s) = %v, want %v", c.q, c.scope, got, c.want)
		}
	}
}

func TestAttachCaptionFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	var images []clipImage
	fs.Var(attachFlag{&images}, "attach", "")
	fs.Var(captionFlag{&images}, "caption", "")
	err := parseFlags(fs, g, []string{"-attach", "a.png", "text", "-caption", "first", "-attach", "b.png", "-attach", "c.png", "-caption", "third"})
	if err != nil {
		t.Fatal(err)
	}
	want := []clipImage{{"a.png", "first"}, {"b.png", ""}, {"c.png", "third"}}
	if !slices.Equal(images, want) || !slices.Equal(fs.Args(), []string{"text"}) {
		t.Errorf("images = %+v, args = %q", images, fs.Args())
	}
}

func TestHTTPClientRefusesRedirect(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(true)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	g := &globalFlags{url: origin.URL, authKey: "k"}
	req, err := g.newRequest(http.MethodPost, "/api/files/list", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := g.httpClient().Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect followed, want error")
	}
	if leaked.Load() {
		t.Error("request reached the redirect target")
	}
}
