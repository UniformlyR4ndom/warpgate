// Package authhash derives the value sent over the wire as X-Auth/auth from
// the operator-configured key, so the raw key itself is never transmitted.
package authhash

import (
	"crypto/sha256"
	"encoding/hex"
)

const iterations = 1000

// Hash returns the hex-encoded result of applying SHA-256 to key's UTF-8
// bytes, then to that digest, repeated iterations times total.
func Hash(key string) string {
	h := sha256.Sum256([]byte(key))
	for i := 1; i < iterations; i++ {
		h = sha256.Sum256(h[:])
	}
	return hex.EncodeToString(h[:])
}
