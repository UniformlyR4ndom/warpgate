package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"warpgate/internal/authhash"
)

func TestKDFSaltStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg.sqlite")
	read := func() []byte {
		db, err := OpenDB(path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		salt, err := db.KDFSalt()
		if err != nil {
			t.Fatal(err)
		}
		return salt
	}
	first := read()
	if len(first) != authhash.SaltSize {
		t.Fatalf("salt length %d", len(first))
	}
	if second := read(); !bytes.Equal(first, second) {
		t.Error("salt changed on reopen")
	}
}
