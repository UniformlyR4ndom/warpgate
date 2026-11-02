package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"

	"warpgate/internal/connecttoken"
)

// connectToken builds the token clients use to reach this server, and says
// where its host came from: -public-url, the certificate, the listen address,
// or nowhere ("" host).
func connectToken(cfg Config, keyFP, regPath string) (connecttoken.Token, string, error) {
	t := connecttoken.Token{
		TLS:        cfg.Cert != "",
		ClientCert: cfg.ClientRoot != "" || cfg.ClientFP != "",
		Prefix:     strings.TrimPrefix(cfg.PathPrefix, "/"),
	}
	if regPath != "/" {
		t.RegPath = strings.TrimPrefix(regPath, "/")
	}
	if keyFP != "" {
		fp, err := hex.DecodeString(keyFP)
		if err != nil {
			return t, "", err
		}
		t.KeyFP = fp
	}
	var leaf *x509.Certificate
	if t.TLS {
		pair, err := tls.LoadX509KeyPair(cfg.Cert, cfg.CertKey)
		if err != nil {
			return t, "", fmt.Errorf("loading certificate: %w", err)
		}
		t.CertPin = connecttoken.Pin(pair.Certificate[0])
		if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return t, "", fmt.Errorf("parsing certificate: %w", err)
		}
	}
	listenHost, listenPort, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return t, "", err
	}
	port, err := strconv.ParseUint(listenPort, 10, 16)
	if err != nil {
		return t, "", fmt.Errorf("listen port: %w", err)
	}
	t.Port = uint16(port)

	if cfg.PublicURL != "" {
		u, err := parsePublicURL(cfg.PublicURL)
		if err != nil {
			return t, "", err
		}
		t.Host = u.Hostname()
		t.Port = 0
		if p := u.Port(); p != "" {
			n, _ := strconv.ParseUint(p, 10, 16)
			t.Port = uint16(n)
		}
		t.TLS = u.Scheme == "https"
		if !t.TLS {
			t.CertPin = nil
		}
		return t, "-public-url", nil
	}
	if leaf != nil {
		if h := certHost(leaf); h != "" {
			t.Host = h
			return t, "certificate name " + h, nil
		}
	}
	if ip := net.ParseIP(listenHost); listenHost != "" && (ip == nil || !ip.IsUnspecified()) {
		t.Host = listenHost
		return t, "listen address", nil
	}
	return t, "", nil
}

// parsePublicURL accepts http(s)://host[:port] with an empty path.
func parsePublicURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("-public-url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("-public-url must be http(s)://host[:port]")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("-public-url must not contain a path, query or user (-path-prefix is added)")
	}
	if p := u.Port(); p != "" {
		if _, err := strconv.ParseUint(p, 10, 16); err != nil {
			return nil, fmt.Errorf("-public-url: invalid port %q", p)
		}
	}
	return u, nil
}

// certHost returns the certificate's first non-wildcard DNS name, else its
// first IP address, else "".
func certHost(c *x509.Certificate) string {
	for _, n := range c.DNSNames {
		if !strings.HasPrefix(n, "*") {
			return n
		}
	}
	if len(c.IPAddresses) > 0 {
		return c.IPAddresses[0].String()
	}
	return ""
}

func logConnectToken(cfg Config, srv *Server) {
	regPath := ""
	if srv.ipFilter != nil {
		regPath = srv.ipFilter.regPath
	}
	t, source, err := connectToken(cfg, srv.keyFP, regPath)
	if err != nil {
		log.Fatalf("building the connect token: %v", err)
	}
	if source == "" {
		log.Printf("connect token: %s (no host: unknown public address; set -public-url, or clients pass -host)", t.Encode())
		return
	}
	log.Printf("connect token: %s (host from %s)", t.Encode(), source)
}
