package main

import (
	"encoding/json"
	"fmt"
)

// buildTransformArgs builds the JSON args object for a given transform name
// from the CLI's flat set of flags, matching the shapes documented in
// CLAUDE.md ("Transform System").
func buildTransformArgs(transform, key, iv, password, filename string) (json.RawMessage, error) {
	switch transform {
	case "id":
		return nil, nil
	case "xor":
		if key == "" {
			return nil, fmt.Errorf("--key is required for the xor transform")
		}
		return json.Marshal(map[string]string{"key": key})
	case "aes-ctr":
		if key == "" || iv == "" {
			return nil, fmt.Errorf("--key and --iv are required for the aes-ctr transform")
		}
		return json.Marshal(map[string]string{"key": key, "iv": iv})
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
		if key == "" {
			return nil, fmt.Errorf("--key is required for the html-b64 transform (16 bytes hex)")
		}
		return json.Marshal(map[string]string{"key": key})
	default:
		return nil, fmt.Errorf("unknown transform %q", transform)
	}
}
