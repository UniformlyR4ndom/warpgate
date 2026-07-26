package paramcrypt

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	k := DeriveKeys("test-shared-secret")
	cases := [][]byte{
		[]byte(""),
		[]byte("a"),
		[]byte(`{"path":"secret.pdf","transform":"xor","args":{"key":"deadbeef"}}`),
		bytes.Repeat([]byte{0, 1, 2, 3, 255}, 1000),
	}
	for _, pt := range cases {
		blob, err := Encrypt(k, pt)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", pt, err)
		}
		got, err := Decrypt(k, blob)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("round trip mismatch: got %q want %q", got, pt)
		}
	}
}

func TestTamperDetection(t *testing.T) {
	k := DeriveKeys("test-shared-secret")
	nonce := bytes.Repeat([]byte{0x11}, nonceSize)
	pt := []byte(`{"path":"secret.pdf"}`)
	blob, err := encryptWithNonce(k, nonce, pt)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		t.Fatal(err)
	}

	offsets := []int{0, nonceSize - 1, nonceSize, nonceSize + len(pt)/2, len(raw) - macSize, len(raw) - 1}
	for _, off := range offsets {
		tampered := append([]byte(nil), raw...)
		tampered[off] ^= 0x01
		if _, err := Decrypt(k, base64.StdEncoding.EncodeToString(tampered)); !errors.Is(err, ErrAuthFailed) {
			t.Errorf("offset %d: expected ErrAuthFailed, got %v", off, err)
		}
	}
}

func TestWrongKeyRejected(t *testing.T) {
	blob, err := Encrypt(DeriveKeys("secret-a"), []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(DeriveKeys("secret-b"), blob); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("expected ErrAuthFailed, got %v", err)
	}
}

func TestEncMacKeysDiffer(t *testing.T) {
	k := DeriveKeys("test-shared-secret")
	if bytes.Equal(k.enc, k.mac) {
		t.Fatal("enc and mac keys must differ")
	}
}

func TestDeterminism(t *testing.T) {
	k := DeriveKeys("test-shared-secret")
	nonce := bytes.Repeat([]byte{0x42}, nonceSize)
	pt := []byte("same input")
	a, err := encryptWithNonce(k, nonce, pt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := encryptWithNonce(k, nonce, pt)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("expected identical output, got %q vs %q", a, b)
	}
}

func TestDecryptRejectsShortBlob(t *testing.T) {
	k := DeriveKeys("test-shared-secret")
	short := base64.StdEncoding.EncodeToString(make([]byte, nonceSize+macSize-1))
	if _, err := Decrypt(k, short); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("expected ErrAuthFailed, got %v", err)
	}
}

func TestDecryptRejectsBadBase64(t *testing.T) {
	k := DeriveKeys("test-shared-secret")
	if _, err := Decrypt(k, "not base64!!!"); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("expected ErrAuthFailed, got %v", err)
	}
}

// TestVector is a fixed, hardcoded input/output pair used to cross-check the
// JS-side implementation (web/sha256.js + web/aes.js based encryptParams in
// web/app.js) byte-for-byte — see CLAUDE.md's Parameter Encryption section.
// Any change to the wire format, labels, or derivation must update this
// vector (and re-verify the JS side) deliberately, not accidentally.
func TestVector(t *testing.T) {
	k := DeriveKeys("test-shared-secret")
	nonce := make([]byte, nonceSize)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	pt := []byte(`{"path":"secret.pdf","transform":"xor"}`)

	got, err := encryptWithNonce(k, nonce, pt)
	if err != nil {
		t.Fatal(err)
	}
	const want = "AAECAwQFBgcICQoLDA0OD12dksZjXS80RxuoiEWgMLGHJ8ss2ZH8LvBHJTCmWOZimHLh0JNRe6vDUzY5ld9MBfm0hP31KepF8GgPTizonGRu2jcJA6XB"
	if got != want {
		t.Fatalf("test vector mismatch: got %q want %q", got, want)
	}
}
