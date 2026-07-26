package main

import (
	"encoding/json"

	"warpgate/internal/paramcrypt"
)

// encodeBody marshals v to JSON and, if parameter encryption is enabled,
// encrypts it into the base64 blob the server's decodeParams auto-detects
// instead of returning it plain. Every JSON request body, every multipart
// upload's "transform" part, and every WS control message this client sends
// routes through this, mirroring the frontend's encodeBody/sendWSControl.
func (g *globalFlags) encodeBody(v any) ([]byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	keys, enabled := g.paramKeys()
	if !enabled {
		return plain, nil
	}
	blob, err := paramcrypt.Encrypt(keys, plain)
	if err != nil {
		return nil, err
	}
	return []byte(blob), nil
}

// decodeJSON reverses encodeBody's choice for a response body: decrypts
// first when parameter encryption is enabled, otherwise parses plain JSON.
// The server only ever encrypts a response in reply to a request that
// itself arrived encrypted, so reusing this client's own paramKeys() (fixed
// for the whole invocation) to decide is always correct — there's no need
// to auto-detect the way the server's decodeParams does for an arbitrary
// client.
func (g *globalFlags) decodeJSON(body []byte, v any) error {
	keys, enabled := g.paramKeys()
	if !enabled {
		return json.Unmarshal(body, v)
	}
	plain, err := paramcrypt.Decrypt(keys, string(body))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

// maybeEncryptedQuery appends the "?e=1" hint used by GET endpoints with no
// request body to key a response-encryption decision off of (see
// CLAUDE.md's "Parameter Encryption" -> "Response encryption" for
// /api/files/ul-list and /api/clip/list).
func (g *globalFlags) maybeEncryptedQuery(path string) string {
	if _, enabled := g.paramKeys(); enabled {
		return path + "?e=1"
	}
	return path
}
