package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestHandleSettings(t *testing.T) {
	ts := newWSTestServer(t, Config{Root: t.TempDir(), NoAuth: true, WSFrameMax: 5 * 1024 * 1024})

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
	want := settingsResponse{WSFrameMin: wsFrameSizeMin, WSFrameMax: 5 * 1024 * 1024}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
