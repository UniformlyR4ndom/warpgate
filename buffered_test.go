package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func lowerBufferedLimit(t *testing.T, n int64) {
	old := maxBufferedPlain
	maxBufferedPlain = n
	t.Cleanup(func() { maxBufferedPlain = old })
}

func TestBufferedTransformDownloadLimit(t *testing.T) {
	lowerBufferedLimit(t, 1024)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.bin"), bytes.Repeat([]byte("x"), 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := newSimTestServer(t, Config{Root: root, NoAuth: true})
	key := json.RawMessage(`{"key":"` + strings.Repeat("ab", 16) + `"}`)
	for _, tc := range []struct {
		transform string
		args      json.RawMessage
		size      int64
		want      int
	}{
		{"html-b64", key, 1024, http.StatusOK},
		{"html-b64", key, 1025, http.StatusRequestEntityTooLarge},
		{"pdf", json.RawMessage(`{"filename":"f.bin"}`), 2048, http.StatusRequestEntityTooLarge},
		{"id", nil, 4096, http.StatusOK},
	} {
		body, _ := json.Marshal(downloadRequest{Path: "f.bin", Size: tc.size, Transform: tc.transform, Args: tc.args})
		resp, err := http.Post(ts.URL+"/api/files/download", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s, size %d: status %d, want %d", tc.transform, tc.size, resp.StatusCode, tc.want)
		}
	}
}

func TestBufferedTransformWSDownloadLimit(t *testing.T) {
	lowerBufferedLimit(t, 1024)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.bin"), bytes.Repeat([]byte("x"), 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := newSimTestServer(t, Config{Root: root, NoAuth: true})
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{"path": "f.bin", "transform": "html-b64", "args": map[string]string{"key": strings.Repeat("ab", 16)}}); err != nil {
		t.Fatal(err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil || !ack.OK {
		t.Fatalf("handshake: %+v %v", ack, err)
	}
	if err := conn.WriteJSON(wsChunkRequest{Start: 0, Size: 2048}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&ack); err != nil || ack.OK || !strings.Contains(ack.Error, "at most 1024 bytes") {
		t.Fatalf("oversized chunk: %+v %v", ack, err)
	}
}

func TestBufferedTransformUploadLimit(t *testing.T) {
	lowerBufferedLimit(t, 1024)
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	resp, _ := postChunkWithTransform(t, ts.URL, "pdf", "big.pdf", bytes.Repeat([]byte("x"), 1024+1<<20+1), "", true)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized pdf upload: status %d, want 413", resp.StatusCode)
	}
	resp, _ = postChunk(t, ts.URL, "big.bin", bytes.Repeat([]byte("x"), 4096), "", true)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("id upload over the limit: status %d, want 201", resp.StatusCode)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/upload"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(wsUploadRequest{Filename: "big.html", Transform: "html-b64", Args: json.RawMessage(`{"key":"` + strings.Repeat("ab", 16) + `"}`)}); err != nil {
		t.Fatal(err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil || !ack.OK {
		t.Fatalf("handshake: %+v %v", ack, err)
	}
	for i := 0; i < 4; i++ {
		_ = conn.WriteMessage(websocket.BinaryMessage, bytes.Repeat([]byte("A"), 1024))
	}
	if err := conn.ReadJSON(&ack); err != nil || ack.OK || !strings.Contains(ack.Error, "at most 1024 bytes") {
		t.Fatalf("oversized WS upload: %+v %v", ack, err)
	}
	if _, err := os.Stat(filepath.Join(ulRoot, "big.html")); !os.IsNotExist(err) {
		t.Error("oversized WS upload was stored")
	}
}
