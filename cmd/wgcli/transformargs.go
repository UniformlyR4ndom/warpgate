package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// buildTransformArgs builds the JSON args object for a given transform name
// from the CLI's flat set of flags, matching the shapes documented in
// CLAUDE.md ("Transform System"). A key or IV left empty is generated
// randomly, as the frontend does; both sides of a transfer use the same args.
func buildTransformArgs(transform, key, iv, password, filename string) (json.RawMessage, error) {
	switch transform {
	case "id":
		return nil, nil
	case "xor":
		return json.Marshal(map[string]string{"key": orRandomHex(key, 32)})
	case "aes-ctr":
		return json.Marshal(map[string]string{"key": orRandomHex(key, 32), "iv": orRandomHex(iv, 16)})
	case "pdf":
		args := map[string]string{}
		if filename != "" {
			args["filename"] = filename
		}
		if password != "" {
			args["password"] = password
		}
		return json.Marshal(args)
	case "html-b64":
		return json.Marshal(map[string]string{"key": orRandomHex(key, 16)})
	default:
		return nil, fmt.Errorf("unknown transform %q", transform)
	}
}

// orRandomHex returns s, or n random bytes hex-encoded if s is empty.
func orRandomHex(s string, n int) string {
	if s != "" {
		return s
	}
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
