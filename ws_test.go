package main

import (
	"encoding/hex"
	"encoding/json"
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

	ts := newWSTestServer(t, Config{Root: root, NoAuth: true, WSFrameMax: 16 * 1024 * 1024})

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

// TestWSDownloadMultiFrame requests a chunk with a per-request frameSize
// much smaller than the chunk itself, forcing the server to manually split
// the chunk's transformed output into several separate WS messages (see the
// frame-writing loop in ws_get.go). This verifies the client-driven,
// per-request frame size mechanism directly, rather than relying on
// gorilla's connection-wide buffer to auto-fragment one big message.
func TestWSDownloadMultiFrame(t *testing.T) {
	root := t.TempDir()
	const frameSize = 64 * 1024
	size := frameSize*2 + 1 // spans at least three frames
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(filepath.Join(root, "big.bin"), content, 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	ts := newWSTestServer(t, Config{Root: root, NoAuth: true, WSFrameMax: 16 * 1024 * 1024})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/ws/download"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(wsDownloadRequest{Path: "big.bin", Transform: "id", FrameSize: frameSize}); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("reading ack: %v", err)
	}
	if !ack.OK || ack.Size != int64(size) {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	if err := conn.WriteJSON(wsChunkRequest{Start: 0, Size: ack.Size}); err != nil {
		t.Fatalf("writing chunk request: %v", err)
	}

	var got []byte
	messageCount := 0
	for int64(len(got)) < ack.Size {
		mt, p, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading frame: %v", err)
		}
		if mt != websocket.BinaryMessage {
			t.Fatalf("expected binary message, got type %d", mt)
		}
		if len(p) > frameSize {
			t.Fatalf("frame exceeded requested frameSize: got %d bytes, want <= %d", len(p), frameSize)
		}
		got = append(got, p...)
		messageCount++
	}
	if messageCount < 3 {
		t.Fatalf("expected at least 3 separate frames, got %d", messageCount)
	}
	if string(got) != string(content) {
		t.Fatalf("reassembled content mismatch: got %d bytes, want %d bytes", len(got), len(content))
	}

	if err := conn.WriteJSON(wsChunkRequest{Done: true}); err != nil {
		t.Fatalf("writing done: %v", err)
	}
}

func TestWSUploadRoundTrip(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newWSTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true, WSFrameMax: 16 * 1024 * 1024})

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

// TestWSUploadMultiFrame uploads XOR-encoded content split across several
// separate binary WS messages (mirroring the frontend's per-frame send
// loop), verifying the server reassembles all frames into one buffer before
// calling Untransform once. With the older per-message-Untransform logic
// this would corrupt everything after the first frame, since XOR resets its
// key phase at the start of every Untransform call.
func TestWSUploadMultiFrame(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newWSTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true, WSFrameMax: 16 * 1024 * 1024})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/ws/upload"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	const keyHex = "a1b2c3d4" // 4-byte repeating XOR key
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatalf("decoding test key: %v", err)
	}

	// Deliberately not a multiple of the frame size or the key length, so a
	// naive per-frame key-phase reset would visibly corrupt bytes past the
	// first frame. Comfortably larger than frameSize so this exercises
	// several frames, not just one.
	content := make([]byte, 3500)
	for i := range content {
		content[i] = byte(i % 256)
	}
	encoded := make([]byte, len(content))
	for i := range content {
		encoded[i] = content[i] ^ key[i%len(key)]
	}

	args, err := json.Marshal(map[string]string{"key": keyHex})
	if err != nil {
		t.Fatalf("marshaling args: %v", err)
	}
	const frameSize = 1024 // the minimum allowed frameSize (wsFrameSizeMin)
	if err := conn.WriteJSON(wsUploadRequest{Filename: "multiframe.bin", Transform: "xor", Args: args, FrameSize: frameSize}); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("reading ack: %v", err)
	}
	if !ack.OK {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	for sent := 0; sent < len(encoded); {
		end := min(sent+frameSize, len(encoded))
		if err := conn.WriteMessage(websocket.BinaryMessage, encoded[sent:end]); err != nil {
			t.Fatalf("writing frame: %v", err)
		}
		sent = end
	}
	if err := conn.WriteJSON(wsDoneMessage{Done: true}); err != nil {
		t.Fatalf("writing done: %v", err)
	}

	var final wsAck
	if err := conn.ReadJSON(&final); err != nil {
		t.Fatalf("reading final ack: %v", err)
	}
	if !final.OK {
		t.Fatalf("unexpected final ack: %+v", final)
	}

	got, err := os.ReadFile(filepath.Join(ulRoot, "multiframe.bin"))
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("uploaded content mismatch (frame reassembly bug?): got %d bytes, want %d bytes", len(got), len(content))
	}
}

func TestWSDownloadFrameSizeValidation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.bin"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	ts := newWSTestServer(t, Config{Root: root, NoAuth: true, WSFrameMax: 1024 * 1024})

	for _, frameSize := range []int64{100, 2 * 1024 * 1024} { // too small, too large
		conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/ws/download"), nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if err := conn.WriteJSON(wsDownloadRequest{Path: "f.bin", Transform: "id", FrameSize: frameSize}); err != nil {
			t.Fatalf("writing request: %v", err)
		}
		var ack wsAck
		if err := conn.ReadJSON(&ack); err != nil {
			t.Fatalf("reading ack: %v", err)
		}
		if ack.OK {
			t.Fatalf("frameSize %d: expected rejection, got ok ack", frameSize)
		}
		conn.Close()
	}
}

func TestWSUploadFrameSizeValidation(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newWSTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true, WSFrameMax: 1024 * 1024})

	for _, frameSize := range []int64{100, 2 * 1024 * 1024} { // too small, too large
		conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/ws/upload"), nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if err := conn.WriteJSON(wsUploadRequest{Filename: "x.bin", Transform: "id", FrameSize: frameSize}); err != nil {
			t.Fatalf("writing request: %v", err)
		}
		var ack wsAck
		if err := conn.ReadJSON(&ack); err != nil {
			t.Fatalf("reading ack: %v", err)
		}
		if ack.OK {
			t.Fatalf("frameSize %d: expected rejection, got ok ack", frameSize)
		}
		conn.Close()
	}

	if _, err := os.Stat(filepath.Join(ulRoot, "x.bin")); err == nil {
		t.Fatalf("file should not have been created after frameSize rejection")
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
