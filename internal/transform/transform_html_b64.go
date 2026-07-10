package transform

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// htmlB64Prefix and htmlB64Suffix bracket the base64 payload in the wrapper
// HTML; Transform and EncodedLen must stay in lockstep, so both read from
// these constants rather than duplicating the literal template.
const (
	htmlB64Prefix = "<!DOCTYPE html><html><body>\n" + `<img src="data:image/jpeg;base64,`
	htmlB64Suffix = `">` + "\n" + "</body></html>"
)

type htmlB64Args struct {
	Key string `json:"key"`
}

type htmlB64Transformer struct {
	key []byte
}

func xorBytes(data, key []byte) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ key[i%len(key)]
	}
	return out
}

func (t *htmlB64Transformer) ContentType() string { return "text/html; charset=utf-8" }

func (t *htmlB64Transformer) Transform(r io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("html-b64: reading data: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(xorBytes(data, t.key))
	html := htmlB64Prefix + encoded + htmlB64Suffix
	return strings.NewReader(html), nil
}

func (t *htmlB64Transformer) EncodedLen(plaintextLen int64) int64 {
	return int64(len(htmlB64Prefix)) + int64(base64.StdEncoding.EncodedLen(int(plaintextLen))) + int64(len(htmlB64Suffix))
}

func (t *htmlB64Transformer) Untransform(r io.Reader) (io.Reader, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("html-b64: reading HTML: %w", err)
	}
	html := string(raw)

	const prefix = `src="data:image/jpeg;base64,`
	start := strings.Index(html, prefix)
	if start == -1 {
		return nil, fmt.Errorf("html-b64: base64 payload not found in HTML")
	}
	start += len(prefix)
	end := strings.Index(html[start:], `"`)
	if end == -1 {
		return nil, fmt.Errorf("html-b64: unterminated base64 payload in HTML")
	}

	decoded, err := base64.StdEncoding.DecodeString(html[start : start+end])
	if err != nil {
		return nil, fmt.Errorf("html-b64: base64 decode: %w", err)
	}
	return bytes.NewReader(xorBytes(decoded, t.key)), nil
}

func newHTMLB64Transformer(raw json.RawMessage) (Transformer, error) {
	var args htmlB64Args
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("html-b64 args: %w", err)
	}
	key, err := hex.DecodeString(args.Key)
	if err != nil {
		return nil, fmt.Errorf("html-b64 key: %w", err)
	}
	if len(key) != 16 {
		return nil, fmt.Errorf("html-b64 key must be exactly 16 bytes (got %d)", len(key))
	}
	return &htmlB64Transformer{key: key}, nil
}
