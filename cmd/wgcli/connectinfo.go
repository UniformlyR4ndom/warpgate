package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"

	"warpgate/internal/connecttoken"
)

// runConnectInfo decodes a connect token without contacting the server, so it
// needs neither a host nor a key.
func runConnectInfo(args []string) error {
	fs := flag.NewFlagSet("connect-info", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print one JSON document on stdout (env WGCLI_JSON)")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if v, ok := os.LookupEnv("WGCLI_JSON"); ok && !*asJSON {
		*asJSON, _ = strconv.ParseBool(v)
	}
	s := fs.Arg(0)
	if s == "" {
		s = os.Getenv("WGCLI_CONNECT")
	}
	if s == "" {
		return errors.New("usage: wgcli connect-info [token] (default: WGCLI_CONNECT)")
	}
	t, err := connecttoken.Decode(s)
	if err != nil {
		return err
	}
	url, err := t.BaseURL("")
	if err != nil {
		url = ""
	}
	info := map[string]any{
		"url": url, "host": t.Host, "port": t.Port, "tls": t.TLS, "prefix": t.Prefix, "regPath": t.RegPath,
		"certPin": hex.EncodeToString(t.CertPin), "clientCert": t.ClientCert, "keyId": hex.EncodeToString(t.KeyFP),
	}
	if *asJSON {
		printJSON(info)
		return nil
	}
	shown := url
	if url == "" {
		shown, _ = t.BaseURL("<host>")
		fmt.Printf("url:          %s (no host in the token: pass -host)\n", shown)
	} else {
		fmt.Printf("url:          %s\n", url)
	}
	if t.RegPath != "" {
		fmt.Printf("registration: %s\n", t.RegistrationURL(shown))
	} else {
		fmt.Println("registration: at the base URL, if the server has an IP firewall")
	}
	if t.CertPin != nil {
		fmt.Printf("certificate:  pinned, SHA-256 prefix %s\n", hex.EncodeToString(t.CertPin))
	}
	if t.ClientCert {
		fmt.Println("client cert:  required (-cert, -cert-key)")
	}
	if t.KeyFP != nil {
		fmt.Printf("key ID:       %s\n", hex.EncodeToString(t.KeyFP))
	} else {
		fmt.Println("key ID:       none (authentication disabled)")
	}
	return nil
}
