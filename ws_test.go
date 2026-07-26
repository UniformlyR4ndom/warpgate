package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"warpgate/internal/authhash"
	"warpgate/internal/paramcrypt"
	"warpgate/internal/transform"
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

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
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
		var chunkAck wsAck
		if err := conn.ReadJSON(&chunkAck); err != nil {
			t.Fatalf("reading chunk ack: %v", err)
		}
		if !chunkAck.OK || chunkAck.ChunkSize != size {
			t.Fatalf("unexpected chunk ack: %+v (requested size %d)", chunkAck, size)
		}
		var chunk []byte
		for int64(len(chunk)) < chunkAck.ChunkSize {
			mt, p, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("reading chunk: %v", err)
			}
			if mt != websocket.BinaryMessage {
				t.Fatalf("expected binary message, got type %d", mt)
			}
			chunk = append(chunk, p...)
		}
		got = append(got, chunk...)
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
// frame-writing loop in ws_download.go). This verifies the client-driven,
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

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
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
	var chunkAck wsAck
	if err := conn.ReadJSON(&chunkAck); err != nil {
		t.Fatalf("reading chunk ack: %v", err)
	}
	if !chunkAck.OK || chunkAck.ChunkSize != ack.Size {
		t.Fatalf("unexpected chunk ack: %+v", chunkAck)
	}

	var got []byte
	messageCount := 0
	for int64(len(got)) < chunkAck.ChunkSize {
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

// TestWSDownloadChunkSizeAnnouncement is the regression test for the bug
// where chunked WS download had no way to signal a chunk's actual
// transformed length: pdf and html-b64 both expand their input, so a client
// trusting the requested plaintext size to know how many bytes to read
// would either hang (short read) or, per the frontend's actual historical
// behavior, throw trying to write more bytes than it allocated for.
func TestWSDownloadChunkSizeAnnouncement(t *testing.T) {
	root := t.TempDir()
	content := []byte("hello from the chunk-size announcement test")
	if err := os.WriteFile(filepath.Join(root, "note.txt"), content, 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	ts := newWSTestServer(t, Config{Root: root, NoAuth: true, WSFrameMax: 16 * 1024 * 1024})

	htmlB64Args, err := json.Marshal(map[string]string{"key": "000102030405060708090a0b0c0d0e0f"})
	if err != nil {
		t.Fatalf("marshaling html-b64 args: %v", err)
	}

	for _, tc := range []struct {
		name      string
		transform string
		args      json.RawMessage
	}{
		{"pdf", "pdf", nil},
		{"html-b64", "html-b64", htmlB64Args},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()

			if err := conn.WriteJSON(wsDownloadRequest{Path: "note.txt", Transform: tc.transform, Args: tc.args}); err != nil {
				t.Fatalf("writing request: %v", err)
			}
			var ack wsAck
			if err := conn.ReadJSON(&ack); err != nil {
				t.Fatalf("reading ack: %v", err)
			}
			if !ack.OK || ack.Size != int64(len(content)) {
				t.Fatalf("unexpected ack: %+v", ack)
			}

			if err := conn.WriteJSON(wsChunkRequest{Start: 0, Size: ack.Size}); err != nil {
				t.Fatalf("writing chunk request: %v", err)
			}
			var chunkAck wsAck
			if err := conn.ReadJSON(&chunkAck); err != nil {
				t.Fatalf("reading chunk ack: %v", err)
			}
			// The whole point: the announced length must NOT equal the
			// plaintext size for these two transforms.
			if !chunkAck.OK || chunkAck.ChunkSize <= ack.Size {
				t.Fatalf("expected chunkSize to exceed plaintext size %d, got %+v", ack.Size, chunkAck)
			}

			var got []byte
			for int64(len(got)) < chunkAck.ChunkSize {
				mt, p, err := conn.ReadMessage()
				if err != nil {
					t.Fatalf("reading frame: %v", err)
				}
				if mt != websocket.BinaryMessage {
					t.Fatalf("expected binary message, got type %d", mt)
				}
				got = append(got, p...)
			}
			if int64(len(got)) != chunkAck.ChunkSize {
				t.Fatalf("read %d bytes but chunkAck announced %d", len(got), chunkAck.ChunkSize)
			}

			transformer, err := transform.New(tc.transform, tc.args)
			if err != nil {
				t.Fatalf("constructing transform: %v", err)
			}
			if st, ok := transformer.(transform.SizedTransformer); ok {
				if want := st.EncodedLen(ack.Size); chunkAck.ChunkSize != want {
					t.Fatalf("chunkSize %d does not match EncodedLen %d", chunkAck.ChunkSize, want)
				}
			}

			decoded, err := transformer.Untransform(bytes.NewReader(got))
			if err != nil {
				t.Fatalf("untransform: %v", err)
			}
			decodedBytes, err := io.ReadAll(decoded)
			if err != nil {
				t.Fatalf("reading decoded: %v", err)
			}
			if string(decodedBytes) != string(content) {
				t.Fatalf("decoded mismatch: got %q, want %q", decodedBytes, content)
			}

			if err := conn.WriteJSON(wsChunkRequest{Done: true}); err != nil {
				t.Fatalf("writing done: %v", err)
			}
		})
	}
}

func TestWSUploadRoundTrip(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newWSTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true, WSFrameMax: 16 * 1024 * 1024})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/upload"), nil)
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

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/upload"), nil)
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
		conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
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
		conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/upload"), nil)
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

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
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

// writeEncryptedWSJSON marshals v, encrypts it under keys, and sends it as a
// text message — mirroring what an encrypting client sends instead of plain
// JSON.
func writeEncryptedWSJSON(t *testing.T, conn *websocket.Conn, keys paramcrypt.Keys, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}
	blob, err := paramcrypt.Encrypt(keys, body)
	if err != nil {
		t.Fatalf("encrypting: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(blob)); err != nil {
		t.Fatalf("writing encrypted message: %v", err)
	}
}

// readEncryptedWSJSON reads the next text message, asserts it is NOT valid
// plain JSON (proving the server actually encrypted it, not just echoed
// plaintext), then decrypts it under keys into v.
func readEncryptedWSJSON(t *testing.T, conn *websocket.Conn, keys paramcrypt.Keys, v any) {
	t.Helper()
	mt, p, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("reading message: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("expected text message, got type %d", mt)
	}
	if json.Valid(p) {
		t.Fatalf("expected an encrypted (non-JSON) message, got plain JSON: %s", p)
	}
	plaintext, err := paramcrypt.Decrypt(keys, string(p))
	if err != nil {
		t.Fatalf("decrypting message: %v", err)
	}
	if err := json.Unmarshal(plaintext, v); err != nil {
		t.Fatalf("unmarshaling decrypted message: %v", err)
	}
}

// TestWSDownloadEncryptedHandshakeEncryptsAcks verifies the response-side of
// parameter encryption: once the handshake control message arrives
// encrypted, every ack the server sends back for that connection — the
// initial ack and the per-chunk ack — must also be encrypted, decided once
// at the handshake and held for the whole connection (not re-detected per
// message). Binary file-content frames are unaffected either way.
func TestWSDownloadEncryptedHandshakeEncryptsAcks(t *testing.T) {
	root := t.TempDir()
	content := []byte("hello from the response-encryption test")
	if err := os.WriteFile(filepath.Join(root, "note.txt"), content, 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	const rawKey = "test-key"
	ts := newWSTestServer(t, Config{Root: root, Key: rawKey, WSFrameMax: 16 * 1024 * 1024})
	keys := paramcrypt.DeriveKeys(rawKey)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	writeEncryptedWSJSON(t, conn, keys, wsDownloadRequest{
		Auth: authhash.Hash(rawKey), Path: "note.txt", Transform: "id",
	})
	var ack wsAck
	readEncryptedWSJSON(t, conn, keys, &ack)
	if !ack.OK || ack.Size != int64(len(content)) {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	writeEncryptedWSJSON(t, conn, keys, wsChunkRequest{Start: 0, Size: ack.Size})
	var chunkAck wsAck
	readEncryptedWSJSON(t, conn, keys, &chunkAck)
	if !chunkAck.OK || chunkAck.ChunkSize != ack.Size {
		t.Fatalf("unexpected chunk ack: %+v", chunkAck)
	}

	var got []byte
	for int64(len(got)) < chunkAck.ChunkSize {
		mt, p, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("reading frame: %v", err)
		}
		if mt != websocket.BinaryMessage {
			t.Fatalf("expected binary message, got type %d", mt)
		}
		got = append(got, p...)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: got %q, want %q", got, content)
	}

	writeEncryptedWSJSON(t, conn, keys, wsChunkRequest{Done: true})
}

// TestWSUploadEncryptedHandshakeEncryptsAcks is TestWSUploadRoundTrip's
// encrypted counterpart: handshake, done message, and both the initial and
// final acks all travel encrypted once the handshake did.
func TestWSUploadEncryptedHandshakeEncryptsAcks(t *testing.T) {
	ulRoot := t.TempDir()
	const rawKey = "test-key"
	ts := newWSTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, Key: rawKey, WSFrameMax: 16 * 1024 * 1024})
	keys := paramcrypt.DeriveKeys(rawKey)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/upload"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	writeEncryptedWSJSON(t, conn, keys, wsUploadRequest{
		Auth: authhash.Hash(rawKey), Filename: "up.bin", Transform: "id",
	})
	var ack wsAck
	readEncryptedWSJSON(t, conn, keys, &ack)
	if !ack.OK {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	content := []byte("encrypted-handshake upload content")
	if err := conn.WriteMessage(websocket.BinaryMessage, content); err != nil {
		t.Fatalf("writing content: %v", err)
	}
	writeEncryptedWSJSON(t, conn, keys, wsDoneMessage{Done: true})

	var final wsAck
	readEncryptedWSJSON(t, conn, keys, &final)
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
