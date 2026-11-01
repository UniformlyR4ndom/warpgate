package authhash

import (
	"encoding/hex"
	"testing"
)

// TestVector pins the derivation; web/app/lib/authhash.js must produce the
// same values (checked by Python's hashlib.scrypt too).
func TestVector(t *testing.T) {
	salt, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	k, err := DeriveKey("correct horse battery staple", salt)
	if err != nil {
		t.Fatal(err)
	}
	const (
		wantK     = "1b2946da71f41179e83b99dc33842d15741b87c4121c8c7f3781c1df864fb58b"
		wantToken = "803b1441f4aa53dd406fb5e1c6f01c811651a4f6edf4507fd6ca741868fa74cf"
		wantFP    = "4fca"
	)
	if got := hex.EncodeToString(k); got != wantK {
		t.Errorf("K = %s, want %s", got, wantK)
	}
	if got := Token(k); got != wantToken {
		t.Errorf("Token = %s, want %s", got, wantToken)
	}
	if got := Fingerprint(k); got != wantFP {
		t.Errorf("Fingerprint = %s, want %s", got, wantFP)
	}
}
