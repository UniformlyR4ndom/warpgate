package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gorilla/websocket"
)

func TestValidateSimTotalSize(t *testing.T) {
	tests := []struct {
		totalSize int64
		wantErr   bool
	}{
		{0, true},
		{-1, true},
		{simMaxTotalSize + 1, true},
		{simMaxTotalSize, false},
		{1, true},   // "1"+" " needs 2 bytes, doesn't fit in a 1-byte file
		{2, false},  // "2"+" " fits exactly in 2 bytes
		{9, false},  // "9"+" " fits exactly in 2 bytes
		{10, false}, // "10"+" " needs 3 bytes, fits in 10
	}

	for _, tt := range tests {
		err := validateSimTotalSize(tt.totalSize)
		if tt.wantErr != (err != nil) {
			t.Errorf("validateSimTotalSize(%d): got err=%v, wantErr=%v", tt.totalSize, err, tt.wantErr)
		}
	}
}

// syntheticFile builds the full expected content for a given totalSize, for
// comparison against simReader output.
func syntheticFile(totalSize int64) []byte {
	want := []byte(strconv.FormatInt(totalSize, 10) + " ")
	for int64(len(want)) < totalSize {
		want = append(want, 0xff)
	}
	return want[:totalSize]
}

func TestSimReader(t *testing.T) {
	const totalSize = 137
	want := syntheticFile(totalSize)

	for _, start := range []int64{0, 1, 3, 50, 136, 137} {
		r := newSimReader(totalSize, start)
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("start=%d: ReadAll: %v", start, err)
		}
		if !bytes.Equal(got, want[start:]) {
			t.Errorf("start=%d: got %d bytes, want %d bytes matching tail of synthetic file", start, len(got), len(want[start:]))
		}
	}
}

func TestSimReaderSmallBuffer(t *testing.T) {
	// Force many short Read calls, straddling the header/padding boundary,
	// to exercise the loop in simReader.Read rather than one big Read.
	const totalSize = 50
	want := syntheticFile(totalSize)
	r := newSimReader(totalSize, 0)
	var got []byte
	buf := make([]byte, 3)
	for {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func newSimTestServer(t *testing.T, cfg Config) *httptest.Server {
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

func TestSimDownloadHTTP(t *testing.T) {
	ts := newSimTestServer(t, Config{Root: t.TempDir(), NoAuth: true})

	const totalSize = 10*1024 + 37 // deliberately not a multiple of the chunk size
	want := syntheticFile(totalSize)

	const chunkSize = 4000
	var got []byte
	for int64(len(got)) < totalSize {
		size := int64(chunkSize)
		if remaining := int64(totalSize) - int64(len(got)); remaining < size {
			size = remaining
		}
		body, _ := json.Marshal(simDownloadRequest{TotalSize: totalSize, Start: int64(len(got)), Size: size})
		resp, err := http.Post(ts.URL+"/api/sim/download", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		chunk, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("reading response: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status %d: %s", resp.StatusCode, chunk)
		}
		got = append(got, chunk...)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("reassembled content mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func TestSimDownloadHTTPValidation(t *testing.T) {
	ts := newSimTestServer(t, Config{Root: t.TempDir(), NoAuth: true})

	tests := []simDownloadRequest{
		{TotalSize: 0, Start: 0, Size: 10},                   // totalSize must be positive
		{TotalSize: 1, Start: 0, Size: 1},                    // header doesn't fit
		{TotalSize: simMaxTotalSize + 1, Start: 0, Size: 10}, // exceeds cap
		{TotalSize: 100, Start: -1, Size: 10},                // negative start
		{TotalSize: 100, Start: 0, Size: 0},                  // size must be positive
	}
	for _, req := range tests {
		body, _ := json.Marshal(req)
		resp, err := http.Post(ts.URL+"/api/sim/download", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("request %+v: got status %d, want 400", req, resp.StatusCode)
		}
	}
}

// multipartUpload builds a multipart/form-data body with a single "file"
// part, matching what handleSimUpload expects.
func multipartUpload(t *testing.T, filename string, content []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("writing file part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

func TestSimUploadHTTP(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	content := []byte("some file content for the sim upload benchmark endpoint")
	body, contentType := multipartUpload(t, "bench.bin", content)

	resp, err := http.Post(ts.URL+"/api/sim/upload", contentType, body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, b)
	}

	var got simUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	want := sha256.Sum256(content)
	if got.Hash != hex.EncodeToString(want[:]) {
		t.Fatalf("hash mismatch: got %s, want %s", got.Hash, hex.EncodeToString(want[:]))
	}

	// Nothing should have been written to the upload directory.
	entries, err := listDir(ulRoot)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files written to upload root, found %d", len(entries))
	}
}

func TestSimUploadExplicitUlDoneFalseStartsChunking(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	// Unlike an omitted transform part (TestSimUploadHTTP, one-shot), an
	// explicit "ul-done":false opts into chunking: the response carries a
	// continuation-key instead of a hash.
	content := []byte("some content")
	body, contentType := multipartChunkedUpload(t, "id", "bench.bin", content, "", false)
	resp, err := http.Post(ts.URL+"/api/sim/upload", contentType, body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var got simUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Hash != "" {
		t.Fatalf("expected no hash on a non-final response, got %q", got.Hash)
	}
	if got.ContinuationKey == "" {
		t.Fatalf("expected a continuation-key")
	}
}

func TestSimUploadHTTPTooLarge(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	content := make([]byte, simMaxTotalSize+1)
	body, contentType := multipartUpload(t, "big.bin", content)

	resp, err := http.Post(ts.URL+"/api/sim/upload", contentType, body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected rejection of oversized upload, got 200")
	}
}

func TestSimUploadNotRegisteredWithoutUlRoot(t *testing.T) {
	ts := newSimTestServer(t, Config{Root: t.TempDir(), NoAuth: true})

	content := []byte("x")
	body, contentType := multipartUpload(t, "x.bin", content)
	resp, err := http.Post(ts.URL+"/api/sim/upload", contentType, body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when -ul-root is unset, got %d", resp.StatusCode)
	}
}

func TestSimWSDownloadRoundTrip(t *testing.T) {
	ts := newWSTestServer(t, Config{Root: t.TempDir(), NoAuth: true, WSFrameMax: 16 * 1024 * 1024})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/sim/ws/download"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	const totalSize = 5000
	if err := conn.WriteJSON(wsSimDownloadRequest{TotalSize: totalSize}); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("reading ack: %v", err)
	}
	if !ack.OK || ack.Size != totalSize {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	const chunkSize = 700
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

	want := syntheticFile(totalSize)
	if !bytes.Equal(got, want) {
		t.Fatalf("reassembled content mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func TestSimWSUploadRoundTrip(t *testing.T) {
	ts := newWSTestServer(t, Config{Root: t.TempDir(), UlRoot: t.TempDir(), NoAuth: true, WSFrameMax: 16 * 1024 * 1024})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/sim/ws/upload"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(wsSimUploadRequest{}); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("reading ack: %v", err)
	}
	if !ack.OK {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	content := []byte("hello, websocket sim upload world - part one; part two follows right after")
	mid := len(content) / 2
	for _, chunk := range [][]byte{content[:mid], content[mid:]} {
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
	want := sha256.Sum256(content)
	if !final.OK || final.Hash != hex.EncodeToString(want[:]) {
		t.Fatalf("unexpected final ack: %+v, want hash %s", final, hex.EncodeToString(want[:]))
	}
}

func TestSimWSUploadTooLarge(t *testing.T) {
	ts := newWSTestServer(t, Config{Root: t.TempDir(), UlRoot: t.TempDir(), NoAuth: true, WSFrameMax: 16 * 1024 * 1024})

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/sim/ws/upload"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(wsSimUploadRequest{}); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("reading ack: %v", err)
	}
	if !ack.OK {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	// Write enough 1 MiB chunks to exceed simMaxTotalSize before reading any
	// response: the server only replies once, on error or "done", so reading
	// after every chunk here would deadlock both sides.
	chunk := make([]byte, 1024*1024)
	for sent := 0; sent <= simMaxTotalSize; sent += len(chunk) {
		if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
			t.Fatalf("writing chunk at sent=%d: %v", sent, err)
		}
	}

	var final wsAck
	if err := conn.ReadJSON(&final); err != nil {
		t.Fatalf("reading final ack: %v", err)
	}
	if final.OK {
		t.Fatalf("expected rejection of oversized upload, got ok ack: %+v", final)
	}
}
