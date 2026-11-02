package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"warpgate/internal/authhash"
)

func TestHandleSettings(t *testing.T) {
	ts := newSimTestServer(t, Config{Root: t.TempDir(), NoAuth: true, WSFrameMax: 5 * 1024 * 1024})

	res, err := http.Get(ts.URL + "/api/public/settings")
	if err != nil {
		t.Fatalf("GET /api/public/settings: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", res.StatusCode)
	}

	var got settingsResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.KDFSalt) != 2*authhash.SaltSize {
		t.Errorf("kdfSalt = %q, want %d hex bytes", got.KDFSalt, authhash.SaltSize)
	}
	want := settingsResponse{WSFrameMin: wsFrameSizeMin, WSFrameMax: 5 * 1024 * 1024, KDFSalt: got.KDFSalt}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestHandleSettingsUploads(t *testing.T) {
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: t.TempDir(), UlFlat: true, NoAuth: true})

	res, err := http.Get(ts.URL + "/api/public/settings")
	if err != nil {
		t.Fatalf("GET /api/public/settings: %v", err)
	}
	defer res.Body.Close()
	var got settingsResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !got.Uploads || !got.UlFlat {
		t.Fatalf("uploads = %v, ulFlat = %v, want both true", got.Uploads, got.UlFlat)
	}
}
