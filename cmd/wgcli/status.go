package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"warpgate/internal/authhash"
	"warpgate/internal/bytesize"
)

type statusOut struct {
	URL    string `json:"url"`
	TLS    bool   `json:"tls"`
	Pinned bool   `json:"pinned"`
	// Transfer holds what transfers use unless their flags say otherwise.
	Transfer transferSettings `json:"transfer"`
	KeyID    string           `json:"keyId,omitempty"`
	// Reachable and below are set once the server answers.
	Reachable  bool   `json:"reachable"`
	Registered bool   `json:"registered"`
	Uploads    bool   `json:"uploads"`
	UlFlat     bool   `json:"ulFlat"`
	Auth       string `json:"auth"`
	Error      string `json:"error,omitempty"`
}

type transferSettings struct {
	Transform  string `json:"transform"`
	Backend    string `json:"backend"`
	FrameSize  int64  `json:"frameSize"` // 0: the server's maximum (download) or 1 MiB (upload)
	ChunkSize  int64  `json:"chunkSize"` // 0: each command's default
	ParamCrypt bool   `json:"paramCrypt"`
}

// envTransferSettings reports the transfer defaults set by WGCLI_* variables,
// rejecting values the transfer commands would reject.
func envTransferSettings(g *globalFlags) (transferSettings, error) {
	t := transferSettings{Transform: "id", Backend: "http"}
	_, t.ParamCrypt = g.paramKeys()
	if v, ok := os.LookupEnv(transferEnv["transform"]); ok {
		if _, err := buildTransformArgs(v, "", "", "", ""); err != nil {
			return t, fmt.Errorf("%s=%q: %w", transferEnv["transform"], v, err)
		}
		t.Transform = v
	}
	if v, ok := os.LookupEnv(transferEnv["backend"]); ok {
		if v != "http" && v != "ws" {
			return t, fmt.Errorf("%s=%q: must be http or ws", transferEnv["backend"], v)
		}
		t.Backend = v
	}
	for name, dst := range map[string]*int64{"frame-size": &t.FrameSize, "chunk-size": &t.ChunkSize} {
		if v, ok := os.LookupEnv(transferEnv[name]); ok {
			n, err := bytesize.Parse(v)
			if err != nil {
				return t, fmt.Errorf("%s=%q: %w", transferEnv[name], v, err)
			}
			*dst = n
		}
	}
	return t, nil
}

func (t transferSettings) String() string {
	frame, chunk := "default", "default"
	if t.FrameSize > 0 {
		frame = fmt.Sprint(t.FrameSize)
	}
	if t.ChunkSize > 0 {
		chunk = fmt.Sprint(t.ChunkSize)
	}
	pc := "off"
	if t.ParamCrypt {
		pc = "on"
	}
	return fmt.Sprintf("transform %s, backend %s, frame size %s, chunk size %s, parameter encryption %s",
		t.Transform, t.Backend, frame, chunk, pc)
}

// runStatus reports first the local configuration (server URL, transport,
// transfer settings, key), which needs no server, then contacts the server
// (registering with the IP firewall if it blocks) for what it offers and
// whether the key works. A local error or an unreachable server still leaves
// the local section printed.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	out := statusOut{
		URL:    g.url,
		TLS:    strings.HasPrefix(g.url, "https://"),
		Pinned: g.token != nil && g.token.CertPin != nil,
	}
	transfer, transferErr := envTransferSettings(g)
	out.Transfer = transfer
	keyConfigured := g.authKey != "" || g.authK != ""
	if id, ok := g.localKeyID(); ok {
		out.KeyID = id
	}

	// gather fills the network part of out; the key ID becomes available here
	// too when it was derived from a passphrase (the salt comes from the server).
	gather := func() error {
		settings, err := g.fetchSettings()
		if err != nil {
			return fmt.Errorf("reaching the server: %w", err)
		}
		out.Reachable = true
		out.Uploads, out.UlFlat, out.Registered = settings.Uploads, settings.UlFlat, g.registered
		k, err := g.key()
		if err != nil {
			return err
		}
		if k != nil {
			out.KeyID = authhash.Fingerprint(k)
		}
		out.Auth, err = g.checkAuth(k != nil)
		return err
	}

	if g.json {
		err := transferErr
		if err == nil {
			err = gather()
		}
		if err != nil {
			out.Error = err.Error()
		}
		printJSON(out)
		return err
	}

	printStatusLocal(out, keyConfigured, transferErr)
	if transferErr != nil {
		return transferErr
	}
	if err := gather(); err != nil {
		fmt.Printf("error:    %v\n", err)
		return err
	}
	printStatusServer(out)
	return nil
}

func printStatusLocal(out statusOut, keyConfigured bool, transferErr error) {
	tls := "plain HTTP"
	if out.TLS {
		tls = "TLS"
		if out.Pinned {
			tls += ", certificate pinned"
		}
	}
	fmt.Printf("server:   %s (%s)\n", out.URL, tls)
	switch {
	case out.KeyID != "":
		fmt.Printf("key:      configured (key ID %s)\n", out.KeyID)
	case keyConfigured:
		fmt.Println("key:      configured (passphrase; key ID shown once the server is reached)")
	default:
		fmt.Println("key:      none")
	}
	if transferErr != nil {
		fmt.Printf("transfer: invalid: %v\n", transferErr)
	} else {
		fmt.Printf("transfer: %s\n", out.Transfer)
	}
}

func printStatusServer(out statusOut) {
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
