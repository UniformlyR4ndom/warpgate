// Package authhash derives the key material shared by server and clients
// from the operator's passphrase: a memory-hard master key K, and from K the
// value sent over the wire as X-Auth/auth.
package authhash

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/scrypt"
)

// scrypt cost: 128 MiB and roughly a second per derivation in a browser.
// Clients use these fixed values, never ones served by the server, so a proxy
// can't downgrade the cost.
const (
	scryptN  = 1 << 17
	scryptR  = 8
	scryptP  = 1
	KeySize  = 32
	SaltSize = 16
)

const (
	tokenLabel       = "warpgate-auth-token-v2"
	fingerprintLabel = "warpgate-key-fingerprint-v1"
)

// DeriveKey returns the master key K for passphrase under the server's salt.
func DeriveKey(passphrase string, salt []byte) ([]byte, error) {
	k, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, KeySize)
	if err != nil {
		return nil, fmt.Errorf("deriving key: %w", err)
	}
	return k, nil
}

// Token returns the hex-encoded auth token for K.
func Token(k []byte) string {
	return hex.EncodeToString(Label(k, tokenLabel))
}

// Fingerprint returns a short hex tag identifying K, for display only.
func Fingerprint(k []byte) string {
	return hex.EncodeToString(Label(k, fingerprintLabel)[:2])
}

// Label returns HMAC-SHA256(k, label), the derivation of every subkey of K.
func Label(k []byte, label string) []byte {
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}
