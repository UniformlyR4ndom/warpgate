package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"warpgate/internal/authhash"
)

type statusOut struct {
	URL        string `json:"url"`
	TLS        bool   `json:"tls"`
	Pinned     bool   `json:"pinned"`
	Registered bool   `json:"registered"`
	Uploads    bool   `json:"uploads"`
	UlFlat     bool   `json:"ulFlat"`
	Auth       string `json:"auth"`
	KeyID      string `json:"keyId,omitempty"`
}

// runStatus checks, in order, that the server is reachable (registering with
// the IP firewall if it blocks), what it offers, and that the key works.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	out := statusOut{
		URL:    g.url,
		TLS:    strings.HasPrefix(g.url, "https://"),
		Pinned: g.token != nil && g.token.CertPin != nil,
	}

	settings, err := g.fetchSettings()
	if err != nil {
		return fmt.Errorf("reaching the server: %w", err)
	}
	out.Uploads, out.UlFlat, out.Registered = settings.Uploads, settings.UlFlat, g.registered

	k, err := g.key()
	if err != nil {
		return err
	}
	if k != nil {
		out.KeyID = authhash.Fingerprint(k)
	}
	if out.Auth, err = g.checkAuth(k != nil); err != nil {
		return err
	}

	if g.json {
		printJSON(out)
		return nil
	}
	tls := "plain HTTP"
	if out.TLS {
		tls = "TLS"
		if out.Pinned {
			tls += ", certificate pinned"
		}
	}
	fmt.Printf("server:   %s (%s)\n", out.URL, tls)
	if out.Registered {
		fmt.Println("firewall: this IP was registered now")
	}
	switch {
	case !out.Uploads:
		fmt.Println("uploads:  disabled")
	case out.UlFlat:
		fmt.Println("uploads:  enabled, flat (no subdirectories)")
	default:
		fmt.Println("uploads:  enabled")
	}
	if out.Auth == "ok" {
		fmt.Printf("auth:     ok (key ID %s)\n", out.KeyID)
	} else {
		fmt.Println("auth:     disabled on the server")
	}
	return nil
}

type serverSettings struct {
	Uploads bool `json:"uploads"`
	UlFlat  bool `json:"ulFlat"`
}

func (g *globalFlags) fetchSettings() (serverSettings, error) {
	var s serverSettings
	resp, err := g.httpClient().Get(strings.TrimRight(g.url, "/") + "/api/public/settings")
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("settings: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return s, fmt.Errorf("settings: %w (is -url a warpgate server?)", err)
	}
	return s, nil
}

// checkAuth lists the download root: "ok" when the key is accepted,
// "disabled" when the server needs none.
func (g *globalFlags) checkAuth(haveKey bool) (string, error) {
	body, err := g.encodeBody(map[string]string{"path": ""})
	if err != nil {
		return "", err
	}
	req, err := g.newRequest(http.MethodPost, "/api/files/list", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusOK && haveKey:
		return "ok", nil
	case resp.StatusCode == http.StatusOK:
		return "disabled", nil
	case resp.StatusCode == http.StatusUnauthorized && haveKey:
		return "", errors.New("the server rejected the key: wrong passphrase, or a key for another server")
	case resp.StatusCode == http.StatusUnauthorized:
		return "", errors.New("the server requires a key: set WGCLI_AUTH_K (see derive-key) or -auth-key")
	default:
		return "", fmt.Errorf("checking the key: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
}
