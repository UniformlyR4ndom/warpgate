// Package paramcrypt implements an optional client-side mechanism for
// encrypting request control-parameter JSON blobs (paths, transform
// keys/passwords, etc.), so a proxy that can read plaintext HTTP/WS traffic
// can't recover them. Keys are labeled subkeys of the master key K (see
// internal/authhash), separate from the X-Auth token.
package paramcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"warpgate/internal/authhash"
)

const (
	nonceSize = 16          // aes.BlockSize; also the AES-CTR initial counter size
	macSize   = sha256.Size // 32

	encLabel = "warpgate-param-enc-v2"
	macLabel = "warpgate-param-mac-v2"
)

// ErrAuthFailed is returned by Decrypt for any failure — bad base64, a
// too-short blob, or a MAC mismatch — so callers don't need to distinguish
// why a blob was rejected.
var ErrAuthFailed = errors.New("paramcrypt: authentication failed")

// Keys holds the AES-CTR and HMAC-SHA256 subkeys.
type Keys struct {
	enc []byte
	mac []byte
}

// DeriveKeys derives the encryption and MAC subkeys from the master key K.
func DeriveKeys(k []byte) Keys {
	return Keys{
		enc: authhash.Label(k, encLabel),
		mac: authhash.Label(k, macLabel),
	}
}

// Encrypt returns the base64-encoded nonce||ciphertext||hmac blob for
// plaintext, using a fresh random nonce.
func Encrypt(k Keys, plaintext []byte) (string, error) {
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("paramcrypt: generating nonce: %w", err)
	}
	return encryptWithNonce(k, nonce, plaintext)
}

// encryptWithNonce is Encrypt with an explicit nonce, used by tests for
// deterministic output. Never exposed publicly: reusing a nonce with the
// same key breaks AES-CTR's security, so callers outside this package must
// not be able to pass one.
func encryptWithNonce(k Keys, nonce, plaintext []byte) (string, error) {
	if len(nonce) != nonceSize {
		return "", fmt.Errorf("paramcrypt: nonce must be %d bytes", nonceSize)
	}
	block, err := aes.NewCipher(k.enc)
	if err != nil {
		return "", fmt.Errorf("paramcrypt: %w", err)
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCTR(block, nonce).XORKeyStream(ciphertext, plaintext)

	combined := make([]byte, 0, nonceSize+len(ciphertext)+macSize)
	combined = append(combined, nonce...)
	combined = append(combined, ciphertext...)
	combined = append(combined, mac(k, combined)...)

	return base64.StdEncoding.EncodeToString(combined), nil
}

// Decrypt reverses Encrypt. Any failure — bad base64, too-short input, or a
// MAC mismatch — returns ErrAuthFailed.
func Decrypt(k Keys, blob string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return nil, ErrAuthFailed
	}
	if len(raw) < nonceSize+macSize {
		return nil, ErrAuthFailed
	}
	body, gotMAC := raw[:len(raw)-macSize], raw[len(raw)-macSize:]
	if !hmac.Equal(mac(k, body), gotMAC) {
		return nil, ErrAuthFailed
	}

	nonce, ciphertext := body[:nonceSize], body[nonceSize:]
	block, err := aes.NewCipher(k.enc)
	if err != nil {
		return nil, fmt.Errorf("paramcrypt: %w", err)
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCTR(block, nonce).XORKeyStream(plaintext, ciphertext)
	return plaintext, nil
}

// mac computes HMAC-SHA256(k.mac, data) — used over nonce||ciphertext, i.e.
// everything in the wire blob except the MAC itself.
func mac(k Keys, data []byte) []byte {
	h := hmac.New(sha256.New, k.mac)
	h.Write(data)
	return h.Sum(nil)
}
