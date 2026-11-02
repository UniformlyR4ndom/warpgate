package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"warpgate/internal/connecttoken"
)

// writeTestCert writes a self-signed certificate with the given names and
// returns the cert and key paths and the DER bytes.
func writeTestCert(t *testing.T, dnsNames []string, ips []net.IP) (string, string, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath, der
}

func TestConnectTokenHost(t *testing.T) {
	cert, key, der := writeTestCert(t, []string{"*.example.com", "files.example.com"}, []net.IP{net.ParseIP("10.0.0.1")})
	ipCert, ipKey, _ := writeTestCert(t, nil, []net.IP{net.ParseIP("10.0.0.1")})
	cases := []struct {
		name       string
		cfg        Config
		host       string
		port       uint16
		tls, pin   bool
		fromSource string
	}{
		{"public url", Config{Addr: "0.0.0.0:8443", Cert: cert, CertKey: key, PublicURL: "https://pub.example:9443"}, "pub.example", 9443, true, true, "-public-url"},
		{"public url default port", Config{Addr: "0.0.0.0:8080", PublicURL: "https://pub.example"}, "pub.example", 0, true, false, "-public-url"},
		{"public http drops pin", Config{Addr: "0.0.0.0:8443", Cert: cert, CertKey: key, PublicURL: "http://pub.example"}, "pub.example", 0, false, false, "-public-url"},
		{"cert dns name", Config{Addr: "0.0.0.0:8443", Cert: cert, CertKey: key}, "files.example.com", 8443, true, true, "certificate name files.example.com"},
		{"cert ip", Config{Addr: "0.0.0.0:8443", Cert: ipCert, CertKey: ipKey}, "10.0.0.1", 8443, true, true, "certificate name 10.0.0.1"},
		{"listen address", Config{Addr: "127.0.0.1:8090"}, "127.0.0.1", 8090, false, false, "listen address"},
		{"unknown", Config{Addr: "0.0.0.0:8080"}, "", 8080, false, false, ""},
		{"unknown v6", Config{Addr: "[::]:8080"}, "", 8080, false, false, ""},
	}
	for _, c := range cases {
		tok, source, err := connectToken(c.cfg, "", "")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if tok.Host != c.host || tok.Port != c.port || tok.TLS != c.tls || (tok.CertPin != nil) != c.pin || source != c.fromSource {
			t.Errorf("%s: got host %q port %d tls %v pin %v source %q", c.name, tok.Host, tok.Port, tok.TLS, tok.CertPin != nil, source)
		}
		if c.pin && c.cfg.Cert == cert && !bytes.Equal(tok.CertPin, connecttoken.Pin(der)) {
			t.Errorf("%s: pin doesn't match the certificate", c.name)
		}
	}
}

func TestConnectTokenFields(t *testing.T) {
	cfg := Config{Addr: "127.0.0.1:8090", PathPrefix: "/pfx", ClientFP: "00"}
	tok, _, err := connectToken(cfg, "a506", "/join")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Prefix != "pfx" || tok.RegPath != "join" || !tok.ClientCert || !bytes.Equal(tok.KeyFP, []byte{0xa5, 0x06}) {
		t.Errorf("got %+v", tok)
	}
	if tok, _, _ = connectToken(cfg, "", "/"); tok.RegPath != "" || tok.KeyFP != nil {
		t.Errorf("base-path registration or no auth: got %+v", tok)
	}
}

func TestParsePublicURL(t *testing.T) {
	for _, bad := range []string{"ftp://h", "https://", "https://h/path", "https://h?x=1", "https://u@h", "h:8080", "https://h:99999"} {
		if _, err := parsePublicURL(bad); err == nil {
			t.Errorf("parsePublicURL(%q) succeeded", bad)
		}
	}
	for _, good := range []string{"https://h", "http://h:8080/", "https://[::1]:8443"} {
		if _, err := parsePublicURL(good); err != nil {
			t.Errorf("parsePublicURL(%q): %v", good, err)
		}
	}
}
