package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"warpgate/internal/paramcrypt"
)

// maxParamBodySize bounds how much of a request body or WS control message
// decodeParams will buffer before giving up. Generous for any control-
// parameter JSON blob (paths, transform args, clipboard note text), while
// keeping the full-body buffering below from being an unbounded-memory
// footgun — none of decodeParams's callers had any size cap before this.
const maxParamBodySize = 1 << 20 // 1 MiB

// errParamsInvalid is returned by decodeParams for any decode failure —
// malformed JSON, bad base64, or a paramcrypt MAC mismatch — so callers
// don't need to (and can't reliably) distinguish which occurred.
var errParamsInvalid = errors.New("invalid request body")

// decodeParams reads all of r (bounded by maxParamBodySize) and decodes it
// into v: as plain JSON first; if that fails and the server has derived
// param-crypt keys (auth is enabled), as a base64 paramcrypt blob
// containing JSON instead. This lets a client optionally encrypt control
// parameters (see CLAUDE.md's Parameter Encryption section) with no
// server-side negotiation — either wire format is accepted transparently,
// from any client, at all times. The returned bool reports whether the
// decrypt-fallback path was used, so a handler can encrypt its response the
// same way — but only ever in reply to a message that arrived encrypted.
func (s *Server) decodeParams(r io.Reader, v any) (bool, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxParamBodySize+1))
	if err != nil || len(raw) > maxParamBodySize {
		return false, errParamsInvalid
	}
	if json.Unmarshal(raw, v) == nil {
		return false, nil
	}
	if s.cfg.NoAuth {
		return false, errParamsInvalid
	}
	plaintext, err := paramcrypt.Decrypt(s.paramKeys, string(raw))
	if err != nil || json.Unmarshal(plaintext, v) != nil {
		return false, errParamsInvalid
	}
	return true, nil
}

// encodeParamsResponse writes v as the HTTP response body: as an encrypted
// paramcrypt blob when encrypted is true (only ever set from a request that
// itself arrived encrypted — see decodeParams), or as plain JSON otherwise.
func (s *Server) encodeParamsResponse(w http.ResponseWriter, encrypted bool, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if encrypted {
		blob, err := paramcrypt.Encrypt(s.paramKeys, body)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(status)
		_, err = io.WriteString(w, blob)
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}
