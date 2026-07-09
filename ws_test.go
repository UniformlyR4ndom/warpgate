package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func newWSTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	srv, err := NewServer(cfg, db)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	return ts
}

func wsURL(ts *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + path
}

func TestWSDownloadRoundTrip(t *testing.T) {
	root := t.TempDir()
	content := make([]byte, 10*1024+37) // deliberately not a multiple of the chunk size
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(filepath.Join(root, "test.bin"), content, 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	ts := newWSTestServer(t, Config{Root: root, NoAuth: true})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/ws/download"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(wsDownloadRequest{Path: "test.bin", Transform: "id"}); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("reading ack: %v", err)
	}
	if !ack.OK || ack.Size != int64(len(content)) {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	const chunkSize = 4000
	var got []byte
	for int64(len(got)) < ack.Size {
		size := int64(chunkSize)
		if remaining := ack.Size - int64(len(got)); remaining < size {
			size = remaining
		}
		if err := conn.WriteJSON(wsChunkRequest{Start: int64(len(got)), Size: size}); err != nil {
			t.Fatalf("writing chunk request: %v", err)
		}
		mt, p, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading chunk: %v", err)
		}
		if mt != websocket.BinaryMessage {
			t.Fatalf("expected binary message, got type %d", mt)
		}
		got = append(got, p...)
	}
	if err := conn.WriteJSON(wsChunkRequest{Done: true}); err != nil {
		t.Fatalf("writing done: %v", err)
	}

	if string(got) != string(content) {
		t.Fatalf("reassembled content mismatch: got %d bytes, want %d bytes", len(got), len(content))
	}
}

func TestWSUploadRoundTrip(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newWSTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/ws/upload"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(wsUploadRequest{Filename: "up.bin", Transform: "id"}); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("reading ack: %v", err)
	}
	if !ack.OK {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	content := []byte("hello, websocket upload world - part one; part two follows right after")
	mid := len(content) / 2
	chunks := [][]byte{content[:mid], content[mid:]}
	for _, chunk := range chunks {
		if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
			t.Fatalf("writing chunk: %v", err)
		}
	}
	if err := conn.WriteJSON(wsDoneMessage{Done: true}); err != nil {
		t.Fatalf("writing done: %v", err)
	}

	var final wsAck
	if err := conn.ReadJSON(&final); err != nil {
		t.Fatalf("reading final ack: %v", err)
	}
	if !final.OK || final.Name != "up.bin" {
		t.Fatalf("unexpected final ack: %+v", final)
	}

	got, err := os.ReadFile(filepath.Join(ulRoot, "up.bin"))
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("uploaded content mismatch: got %q, want %q", got, content)
	}
}

func TestWSDownloadAuthFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret.bin"), []byte("top secret"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	ts := newWSTestServer(t, Config{Root: root, Key: "correct-key"})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/ws/download"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(wsDownloadRequest{Auth: "wrong-key", Path: "secret.bin", Transform: "id"}); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("reading ack: %v", err)
	}
	if ack.OK {
		t.Fatalf("expected auth failure, got ok ack: %+v", ack)
	}

	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatalf("expected connection to be closed after auth failure")
	}
}
