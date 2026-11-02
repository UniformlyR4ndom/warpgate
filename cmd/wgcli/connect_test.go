package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"warpgate/internal/authhash"
	"warpgate/internal/connecttoken"
)

func parseGlobal(t *testing.T, args ...string) (*globalFlags, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	return g, parseFlags(fs, g, args)
}

func tlsTokenServer(t *testing.T) (*httptest.Server, connecttoken.Token) {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)
	host, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return ts, connecttoken.Token{TLS: true, Host: host, Port: uint16(p), CertPin: connecttoken.Pin(ts.Certificate().Raw)}
}

func TestConnectTokenPin(t *testing.T) {
	_, tok := tlsTokenServer(t)
	get := func(tok connecttoken.Token) error {
		g, err := parseGlobal(t, "-connect", tok.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp, err := g.httpClient().Get(g.url + "/api/public/settings")
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(tok); err != nil {
		t.Fatalf("matching pin: %v", err)
	}
	tok.CertPin = bytes.Repeat([]byte{1}, connecttoken.PinLen)
	if err := get(tok); err == nil || !strings.Contains(err.Error(), "doesn't match the connect token") {
		t.Fatalf("wrong pin: err = %v", err)
	}
}

func TestConnectTokenKeyCheck(t *testing.T) {
	k := bytes.Repeat([]byte{7}, authhash.KeySize)
	fp, _ := hex.DecodeString(authhash.Fingerprint(k))
	for _, c := range []struct {
		keyFP []byte
		ok    bool
	}{{fp, true}, {[]byte{fp[0] ^ 1, fp[1]}, false}, {nil, true}} {
		tok := connecttoken.Token{Host: "h", KeyFP: c.keyFP}
		g, err := parseGlobal(t, "-connect", tok.Encode(), "-auth-k", hex.EncodeToString(k))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.key(); (err == nil) != c.ok {
			t.Errorf("key fingerprint %x: err = %v, want ok %v", c.keyFP, err, c.ok)
		}
	}
}

func TestConnectTokenFlags(t *testing.T) {
	hostless := connecttoken.Token{TLS: true, Port: 8443, Prefix: "p", RegPath: "join"}.Encode()
	if _, err := parseGlobal(t, "-connect", hostless); err == nil || !strings.Contains(err.Error(), "-host") {
		t.Errorf("host-less token without -host: err = %v", err)
	}
	g, err := parseGlobal(t, "-connect", hostless, "-host", "files.example")
	if err != nil || g.url != "https://files.example:8443/p" || g.token.RegistrationURL(g.url) != g.url+"/join" {
		t.Errorf("with -host: url %q, err %v", g.url, err)
	}
	if _, err := parseGlobal(t, "-connect", hostless, "-host", "h", "-url", "http://x"); err == nil {
		t.Error("-connect with -url: expected error")
	}
	if _, err := parseGlobal(t, "-host", "h"); err == nil {
		t.Error("-host without -connect: expected error")
	}
	mtls := connecttoken.Token{Host: "h", ClientCert: true}.Encode()
	if _, err := parseGlobal(t, "-connect", mtls); err == nil || !strings.Contains(err.Error(), "client certificate") {
		t.Errorf("client cert required, none given: err = %v", err)
	}
}
