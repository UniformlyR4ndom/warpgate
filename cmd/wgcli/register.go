package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
)

// blankPageHint explains the empty page warpgate sends for a wrong path
// prefix or a client the IP firewall doesn't admit.
const blankPageHint = "server returned its blank page: -url has the wrong path prefix, or the IP firewall " +
	"doesn't admit this client (see \"wgcli register\")"

var errBlankPage = errors.New(blankPageHint)

func isBlankPage(resp *http.Response) bool {
	return resp.StatusCode == http.StatusOK && resp.ContentLength == 0 &&
		resp.Header.Get("Content-Type") == "text/html; charset=utf-8"
}

// blankPageTransport turns warpgate's blank page into an error, so commands
// don't fail later with a confusing decode error.
type blankPageTransport struct{ base http.RoundTripper }

func (t blankPageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && isBlankPage(resp) {
		resp.Body.Close()
		return nil, errBlankPage
	}
	return resp, err
}

// runRegister does what the browser's registration page does: an empty form
// POST to the registration URL, which answers with a 303 to the UI.
func runRegister(args []string) error {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	target := g.registrationURL()
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}
	needed, err := g.register(target)
	if err != nil {
		return err
	}
	if needed {
		g.report(map[string]any{"url": target, "needed": true}, "registered (or already admitted) at %s", target)
	} else {
		g.report(map[string]any{"url": target, "needed": false},
			"no registration needed: this client already reaches the server at %s", g.url)
	}
	return nil
}

// registrationURL is the token's registration URL, else -url.
func (g *globalFlags) registrationURL() string {
	if g.token != nil && g.token.RegPath != "" {
		return g.token.RegistrationURL(g.url)
	}
	return g.url
}

// register POSTs to target; needed is false when the client reaches the
// server without registering.
func (g *globalFlags) register(target string) (needed bool, err error) {
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(""))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{
		Transport:     &http.Transport{TLSClientConfig: g.tlsConfig},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusSeeOther {
		return true, nil
	}
	if g.reachesServer() {
		return false, nil
	}
	if isBlankPage(resp) {
		return false, fmt.Errorf("not registered: wrong registration URL, or registration is closed (window expired or limit reached)")
	}
	return false, fmt.Errorf("not registered: unexpected response %s", resp.Status)
}

// reachesServer reports whether -url serves this client, i.e. there is no IP
// firewall or it already admits the client.
func (g *globalFlags) reachesServer() bool {
	resp, err := g.httpClient().Get(strings.TrimRight(g.url, "/") + "/api/public/settings")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
