package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func postList(t *testing.T, url string, req listRequest) (int, []listEntry) {
	t.Helper()
	body, _ := json.Marshal(req)
	resp, err := http.Post(url+"/api/files/list", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var entries []listEntry
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, entries
}

func TestListHash(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory holding a file of its own name must still list as a directory.
	for name, content := range map[string]string{"a.txt": "alpha", "x/x": "inner"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts := newSimTestServer(t, Config{Root: root, NoAuth: true})

	status, entries := postList(t, ts.URL, listRequest{Path: "a.txt", Hash: true})
	if status != http.StatusOK || len(entries) != 1 || entries[0].Name != "a.txt" ||
		*entries[0].Size != 5 || entries[0].SHA256 != sha256Hex([]byte("alpha")) {
		t.Fatalf("file: status %d, entries %+v", status, entries)
	}
	if _, entries = postList(t, ts.URL, listRequest{Path: "a.txt"}); len(entries) != 1 || entries[0].SHA256 != "" {
		t.Fatalf("file without hash: %+v", entries)
	}
	_, entries = postList(t, ts.URL, listRequest{Path: "x", Hash: true})
	if len(entries) != 1 || entries[0].SHA256 != sha256Hex([]byte("inner")) {
		t.Fatalf("directory x: %+v", entries)
	}
	_, entries = postList(t, ts.URL, listRequest{Path: "", Hash: true})
	for _, e := range entries {
		if (e.Type == "f") != (e.SHA256 != "") {
			t.Errorf("root listing: %+v", e)
		}
	}
	if status, _ = postList(t, ts.URL, listRequest{Path: "missing", Hash: true}); status != http.StatusNotFound {
		t.Errorf("missing path: status %d", status)
	}
}

func TestWSDownloadHash(t *testing.T) {
	root := t.TempDir()
	content := []byte("websocket download content")
	if err := os.WriteFile(filepath.Join(root, "f.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	ts := newSimTestServer(t, Config{Root: root, NoAuth: true, WSFrameMax: 1 << 20})
	for _, withHash := range []bool{true, false} {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteJSON(wsDownloadRequest{Path: "f.bin", Transform: "id", Hash: withHash}); err != nil {
			t.Fatal(err)
		}
		var ack wsAck
		if err := conn.ReadJSON(&ack); err != nil {
			t.Fatal(err)
		}
		conn.Close()
		want := ""
		if withHash {
			want = sha256Hex(content)
		}
		if !ack.OK || ack.Size != int64(len(content)) || ack.SHA256 != want {
			t.Errorf("hash %v: ack %+v", withHash, ack)
		}
	}
}

func TestUploadOneShotHash(t *testing.T) {
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: t.TempDir(), NoAuth: true})
	resp, got := postChunk(t, ts.URL, "one.bin", []byte("one shot"), "", true)
	if resp.StatusCode != http.StatusCreated || got.SHA256 != sha256Hex([]byte("one shot")) {
		t.Fatalf("status %d, response %+v", resp.StatusCode, got)
	}
}
