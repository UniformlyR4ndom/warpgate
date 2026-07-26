// Package authhash derives the value sent over the wire as X-Auth/auth from
// the operator-configured key, so the raw key itself is never transmitted.
package authhash

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

const iterations = 1000

// label domain-separates this derivation from any other value derived from
// the same raw key — internal/paramcrypt.DeriveKeys's two labeled subkeys in
// particular — so knowing one gives no information about, and can't be
// confused with, the other.
const label = "warpgate-auth-token-v1"

// Hash returns the hex-encoded result of seeding with HMAC-SHA256(key,
// label), then applying plain SHA-256 to that seed repeated iterations
// times total.
func Hash(key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(label))
	seed := mac.Sum(nil)

	h := sha256.Sum256(seed)
	for i := 1; i < iterations; i++ {
		h = sha256.Sum256(h[:])
	}
	return hex.EncodeToString(h[:])
}
