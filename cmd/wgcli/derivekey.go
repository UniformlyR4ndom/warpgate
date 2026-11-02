package main

import (
	"encoding/hex"
	"errors"
	"flag"

	"warpgate/internal/authhash"
)

// runDeriveKey prints K for -auth-key, to be passed as -auth-k later
// without repeating the derivation.
func runDeriveKey(args []string) error {
	fs := flag.NewFlagSet("derive-key", flag.ExitOnError)
	saltHex := fs.String("salt", "", "hex KDF salt (default: fetched from the server's /api/public/settings)")
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	if g.authKey == "" {
		return errors.New("derive-key needs -auth-key")
	}
	var salt []byte
	var err error
	if *saltHex != "" {
		if salt, err = hex.DecodeString(*saltHex); err != nil || len(salt) == 0 {
			return errors.New("-salt must be non-empty hex")
		}
	} else if salt, err = g.fetchSalt(); err != nil {
		return err
	}
	k, err := authhash.DeriveKey(g.authKey, salt)
	if err != nil {
		return err
	}
	if err := g.checkKeyFP(k); err != nil {
		return err
	}
	kHex := hex.EncodeToString(k)
	g.report(map[string]string{"key": kHex, "fingerprint": authhash.Fingerprint(k)}, "%s", kHex)
	return nil
}
