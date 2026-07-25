package main

import (
	"crypto/tls"
	"flag"
	"io"
	"net/http"
	"strings"

	"warpgate/internal/authhash"
)

// globalFlags holds the flags common to every subcommand: where the server
// is and how to authenticate against it.
type globalFlags struct {
	url      string
	authKey  string
	insecure bool
}

func addGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	fs.StringVar(&g.url, "url", "http://localhost:8080", "warpgate server base URL")
	fs.StringVar(&g.authKey, "auth-key", "", "X-Auth header value")
	fs.BoolVar(&g.insecure, "insecure", false, "skip TLS certificate verification (self-signed test certs)")
	return g
}

// authToken derives the value actually sent to the server (X-Auth header or
// WS auth field) from -auth-key, so the raw key itself is never transmitted.
func (g *globalFlags) authToken() string {
	if g.authKey == "" {
		return ""
	}
	return authhash.Hash(g.authKey)
}

func (g *globalFlags) httpClient() *http.Client {
	tr := &http.Transport{}
	if g.insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{Transport: tr}
}

func (g *globalFlags) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, strings.TrimRight(g.url, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if token := g.authToken(); token != "" {
		req.Header.Set("X-Auth", token)
	}
	return req, nil
}
