package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"warpgate/internal/authhash"
	"warpgate/internal/paramcrypt"
)

// globalFlags holds the flags common to every subcommand: where the server
// is and how to authenticate against it.
type globalFlags struct {
	url        string
	authKey    string
	insecure   bool
	paramCrypt bool
	certFile   string
	certKey    string
	tlsConfig  *tls.Config
}

// globalEnv maps each global flag to the environment variable used when the
// flag isn't given.
var globalEnv = map[string]string{
	"url":         "WGCLI_URL",
	"auth-key":    "WGCLI_AUTH_KEY",
	"insecure":    "WGCLI_INSECURE",
	"param-crypt": "WGCLI_PARAM_CRYPT",
	"cert":        "WGCLI_CERT",
	"cert-key":    "WGCLI_CERT_KEY",
}

func addGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	fs.StringVar(&g.url, "url", "http://localhost:8080", "warpgate server base URL (env WGCLI_URL)")
	fs.StringVar(&g.authKey, "auth-key", "", "raw auth key (env WGCLI_AUTH_KEY)")
	fs.BoolVar(&g.insecure, "insecure", false, "skip TLS certificate verification (self-signed test certs) (env WGCLI_INSECURE)")
	fs.BoolVar(&g.paramCrypt, "param-crypt", false, "encrypt request control-parameter JSON (and decrypt matching responses) using a key derived from -auth-key; see CLAUDE.md's Parameter Encryption section (env WGCLI_PARAM_CRYPT)")
	fs.StringVar(&g.certFile, "cert", "", "PEM client certificate (chain) for servers requiring TLS client auth; needs -cert-key (env WGCLI_CERT)")
	fs.StringVar(&g.certKey, "cert-key", "", "PEM private key for -cert (env WGCLI_CERT_KEY)")
	return g
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
	}
	if (g.certFile == "") != (g.certKey == "") {
		return fmt.Errorf("-cert and -cert-key must be given together")
	}
	g.tlsConfig = &tls.Config{InsecureSkipVerify: g.insecure}
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

// paramKeys reports whether parameter encryption is active for this
// invocation and, if so, the subkeys derived from -auth-key. Mirrors the
// frontend's paramEncryptEnabled(): the -param-crypt flag alone isn't
// enough — there's no shared secret to derive keys from without an auth
// key, matching how the browser's checkbox is disabled with no key set.
func (g *globalFlags) paramKeys() (paramcrypt.Keys, bool) {
	if !g.paramCrypt || g.authKey == "" {
		return paramcrypt.Keys{}, false
	}
	return paramcrypt.DeriveKeys(g.authKey), true
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
	return &http.Client{Transport: blankPageTransport{&http.Transport{TLSClientConfig: g.tlsConfig}}}
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
