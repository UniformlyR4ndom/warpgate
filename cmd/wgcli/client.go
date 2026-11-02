package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"warpgate/internal/authhash"
	"warpgate/internal/connecttoken"
	"warpgate/internal/paramcrypt"
)

// globalFlags holds the flags common to every subcommand: where the server
// is and how to authenticate against it.
type globalFlags struct {
	url        string
	authKey    string
	authK      string
	insecure   bool
	paramCrypt bool
	certFile   string
	certKey    string
	json       bool
	connect    string
	host       string
	token      *connecttoken.Token
	tlsConfig  *tls.Config

	keyResolved bool
	k           []byte
	keyErr      error
}

// globalEnv maps each global flag to the environment variable used when the
// flag isn't given.
var globalEnv = map[string]string{
	"url":         "WGCLI_URL",
	"auth-key":    "WGCLI_AUTH_KEY",
	"auth-k":      "WGCLI_AUTH_K",
	"insecure":    "WGCLI_INSECURE",
	"param-crypt": "WGCLI_PARAM_CRYPT",
	"cert":        "WGCLI_CERT",
	"cert-key":    "WGCLI_CERT_KEY",
	"json":        "WGCLI_JSON",
	"connect":     "WGCLI_CONNECT",
	"host":        "WGCLI_HOST",
}

func addGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	fs.StringVar(&g.url, "url", "http://localhost:8080", "warpgate server base URL (env WGCLI_URL)")
	fs.StringVar(&g.authKey, "auth-key", "", "auth passphrase; the key is derived with the server's salt, taking about a quarter second (env WGCLI_AUTH_KEY)")
	fs.StringVar(&g.authK, "auth-k", "", "derived key as printed by \"wgcli derive-key\"; overrides -auth-key (env WGCLI_AUTH_K)")
	fs.BoolVar(&g.insecure, "insecure", false, "skip TLS certificate verification (self-signed test certs) (env WGCLI_INSECURE)")
	fs.BoolVar(&g.paramCrypt, "param-crypt", false, "encrypt request parameters (paths, names, transform keys; not file data) and decrypt the matching responses, with keys derived from the auth key (env WGCLI_PARAM_CRYPT)")
	fs.StringVar(&g.certFile, "cert", "", "PEM client certificate (chain) for servers requiring TLS client auth; needs -cert-key (env WGCLI_CERT)")
	fs.StringVar(&g.certKey, "cert-key", "", "PEM private key for -cert (env WGCLI_CERT_KEY)")
	fs.BoolVar(&g.json, "json", false, "print the result as one JSON document on stdout (env WGCLI_JSON)")
	fs.StringVar(&g.connect, "connect", "", "connect token the server logs at startup; sets the URL and pins its certificate (env WGCLI_CONNECT)")
	fs.StringVar(&g.host, "host", "", "host or host:port replacing the connect token's (env WGCLI_HOST)")
	return g
}

// report prints v as one line of JSON with -json, otherwise the text line.
func (g *globalFlags) report(v any, format string, args ...any) {
	if g.json {
		printJSON(v)
		return
	}
	fmt.Printf(format+"\n", args...)
}

func printJSON(v any) {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "wgcli: encoding JSON output:", err)
	}
}

// parseFlags parses args, allowing flags after positional arguments ("--"
// ends flag parsing), then fills global flags not given from the
// environment and loads the client certificate.
func parseFlags(fs *flag.FlagSet, g *globalFlags, args []string) error {
	fs.Parse(permuteArgs(fs, args))
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for name, env := range globalEnv {
		v, ok := os.LookupEnv(env)
		if set[name] || !ok {
			continue
		}
		if err := fs.Set(name, v); err != nil {
			return fmt.Errorf("%s: %w", env, err)
		}
		set[name] = true
	}
	if (g.certFile == "") != (g.certKey == "") {
		return fmt.Errorf("-cert and -cert-key must be given together")
	}
	g.tlsConfig = &tls.Config{InsecureSkipVerify: g.insecure}
	if err := g.applyConnectToken(set["url"]); err != nil {
		return err
	}
	if g.certFile != "" {
		cert, err := tls.LoadX509KeyPair(g.certFile, g.certKey)
		if err != nil {
			return fmt.Errorf("loading client certificate: %w", err)
		}
		g.tlsConfig.Certificates = []tls.Certificate{cert}
	}
	return nil
}

// permuteArgs moves flags (and the values of non-boolean flags) ahead of the
// positional arguments, which follow a "--".
func permuteArgs(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(append(flags, "--"), pos...)
}

// key returns the derived key K, nil without -auth-k/-auth-key. Deriving it
// from -auth-key fetches the salt from the server; the result, or the error,
// is cached for the invocation.
func (g *globalFlags) key() ([]byte, error) {
	if !g.keyResolved {
		g.keyResolved = true
		g.k, g.keyErr = g.resolveKey()
	}
	return g.k, g.keyErr
}

// applyConnectToken sets the URL and certificate pin from -connect.
func (g *globalFlags) applyConnectToken(urlGiven bool) error {
	if g.connect == "" {
		if g.host != "" {
			return errors.New("-host needs -connect")
		}
		return nil
	}
	if urlGiven {
		return errors.New("-connect and -url are mutually exclusive (check WGCLI_URL and WGCLI_CONNECT)")
	}
	t, err := connecttoken.Decode(g.connect)
	if err != nil {
		return err
	}
	if g.url, err = t.BaseURL(g.host); err != nil {
		return err
	}
	if t.ClientCert && g.certFile == "" {
		return errors.New("the server requires a TLS client certificate: pass -cert and -cert-key")
	}
	if t.CertPin != nil {
		pin := t.CertPin
		g.tlsConfig.InsecureSkipVerify = true // the pin replaces CA verification
		g.tlsConfig.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 || !bytes.Equal(connecttoken.Pin(rawCerts[0]), pin) {
				return errors.New("server certificate doesn't match the connect token (renewed? get a new token)")
			}
			return nil
		}
	}
	g.token = &t
	return nil
}

func (g *globalFlags) resolveKey() ([]byte, error) {
	k, err := g.deriveKey()
	if err != nil || k == nil {
		return k, err
	}
	return k, g.checkKeyFP(k)
}

// checkKeyFP compares K with the connect token's key fingerprint, so a wrong
// key fails here instead of counting toward the server's failure limit.
func (g *globalFlags) checkKeyFP(k []byte) error {
	if g.token == nil || g.token.KeyFP == nil {
		return nil
	}
	if fp, want := authhash.Fingerprint(k), hex.EncodeToString(g.token.KeyFP); fp != want {
		return fmt.Errorf("the auth key doesn't match the connect token (key ID %s, expected %s): wrong passphrase?", fp, want)
	}
	return nil
}

func (g *globalFlags) deriveKey() ([]byte, error) {
	if g.authK != "" {
		k, err := hex.DecodeString(g.authK)
		if err != nil || len(k) != authhash.KeySize {
			return nil, fmt.Errorf("-auth-k must be %d hex-encoded bytes", authhash.KeySize)
		}
		return k, nil
	}
	if g.authKey == "" {
		return nil, nil
	}
	salt, err := g.fetchSalt()
	if err != nil {
		return nil, err
	}
	return authhash.DeriveKey(g.authKey, salt)
}

func (g *globalFlags) fetchSalt() ([]byte, error) {
	resp, err := g.httpClient().Get(strings.TrimRight(g.url, "/") + "/api/public/settings")
	if err != nil {
		return nil, fmt.Errorf("fetching KDF salt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching KDF salt: %s", resp.Status)
	}
	var v struct {
		KDFSalt string `json:"kdfSalt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("fetching KDF salt: %w", err)
	}
	salt, err := hex.DecodeString(v.KDFSalt)
	if err != nil || len(salt) == 0 {
		return nil, errors.New("fetching KDF salt: server sent no valid salt")
	}
	return salt, nil
}

// paramKeys reports whether parameter encryption is active (-param-crypt
// and a key) and, if so, the subkeys of K. A key error is reported by the
// next request instead.
func (g *globalFlags) paramKeys() (paramcrypt.Keys, bool) {
	k, err := g.key()
	if !g.paramCrypt || k == nil || err != nil {
		return paramcrypt.Keys{}, false
	}
	return paramcrypt.DeriveKeys(k), true
}

// authToken returns the value sent as X-Auth or WS auth, "" without a key.
func (g *globalFlags) authToken() string {
	k, err := g.key()
	if k == nil || err != nil {
		return ""
	}
	return authhash.Token(k)
}

// httpClient refuses to follow redirects: Go would resend X-Auth to any host,
// and the server never redirects API requests.
func (g *globalFlags) httpClient() *http.Client {
	return &http.Client{
		Transport: blankPageTransport{&http.Transport{TLSClientConfig: g.tlsConfig}},
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("refusing to follow redirect to %s", req.URL.Redacted())
		},
	}
}

func (g *globalFlags) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	if _, err := g.key(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, strings.TrimRight(g.url, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if token := g.authToken(); token != "" {
		req.Header.Set("X-Auth", token)
	}
	return req, nil
}
