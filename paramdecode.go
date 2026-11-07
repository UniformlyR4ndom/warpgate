package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"warpgate/internal/paramcrypt"
)

// maxParamBodySize bounds how much of a request body or WS control message
// decodeParams will buffer before giving up. Generous for any control-
// parameter JSON blob (paths, transform args, clipboard note text), while
// keeping the full-body buffering below from being an unbounded-memory
// footgun — none of decodeParams's callers had any size cap before this.
const maxParamBodySize = 1 << 20 // 1 MiB

// decodeParams failures, kept distinct so writeParamError can map each to the
// right HTTP status instead of collapsing them all onto 401 (which the browser
// treats as a wrong key, latching the whole UI into the auth-failed state):
//   - errParamsTooLarge: body over maxParamBodySize — a size problem, not auth.
//   - errParamsMalformed: body undecodable with no encryption in play (plain
//     JSON parse failed and there are no param-crypt keys to try), so it is
//     unambiguously a bad request.
//   - errParamsInvalid: decode failed while an encrypted body was possible, so
//     a wrong key can't be told from a malformed one; kept deliberately
//     ambiguous (like a rejected X-Auth) so a MAC failure leaks nothing.
var (
	errParamsTooLarge  = errors.New("request body too large")
	errParamsMalformed = errors.New("invalid request body")
	errParamsInvalid   = errors.New("invalid or unauthorized request body")
)

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
	if err != nil {
		return false, errParamsMalformed
	}
	if len(raw) > maxParamBodySize {
		return false, errParamsTooLarge
	}
	if json.Unmarshal(raw, v) == nil {
		return false, nil
	}
	if s.cfg.NoAuth {
		return false, errParamsMalformed
	}
	plaintext, err := paramcrypt.Decrypt(s.paramKeys.Request, string(raw))
	if err != nil || json.Unmarshal(plaintext, v) != nil {
		return false, errParamsInvalid
	}
	return true, nil
}

// writeParamError sends the HTTP status that matches a decodeParams failure:
// 413 for an oversize body, 400 for a plainly malformed one, and 401 for a
// failure indistinguishable from a wrong key (so it mirrors a rejected X-Auth
// and the browser's wrong-key handling still applies where it should).
func (s *Server) writeParamError(w http.ResponseWriter, err error) {
	status := http.StatusUnauthorized
	switch {
	case errors.Is(err, errParamsTooLarge):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, errParamsMalformed):
		status = http.StatusBadRequest
	}
	http.Error(w, err.Error(), status)
}

// encodeParamsResponse writes v as the HTTP response body: as an encrypted
// paramcrypt blob when encrypted is true (only ever set from a request that
// itself arrived encrypted — see decodeParams), or as plain JSON otherwise.
// If the body can't be produced it answers 500 instead. A failed write means
// the client is gone, so it is not reported.
func (s *Server) encodeParamsResponse(w http.ResponseWriter, encrypted bool, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		log.Printf("encoding response: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	contentType := "application/json"
	if encrypted {
		blob, err := paramcrypt.Encrypt(s.paramKeys.Response, body)
		if err != nil {
			log.Printf("encrypting response: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		body, contentType = []byte(blob), "text/plain"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
