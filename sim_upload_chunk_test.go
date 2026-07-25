package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func postSimChunk(t *testing.T, url, name string, content []byte, continuationKey string, done bool) (*http.Response, simUploadResponse) {
	t.Helper()
	body, contentType := multipartChunkedUpload(t, "id", name, content, continuationKey, done)
	resp, err := http.Post(url+"/api/sim/upload", contentType, body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var got simUploadResponse
	if resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return resp, got
}

func TestSimUploadChunkedRoundTrip(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	chunks := [][]byte{
		[]byte("first chunk of the benchmark payload "),
		[]byte("second chunk of the benchmark payload "),
		[]byte("third and final chunk"),
	}
	var all []byte
	for _, c := range chunks {
		all = append(all, c...)
	}
	want := sha256.Sum256(all)
	wantHex := hex.EncodeToString(want[:])

	resp, got := postSimChunk(t, ts.URL, "run-a", chunks[0], "", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 1: status %d", resp.StatusCode)
	}
	if got.ContinuationKey == "" || got.Hash != "" {
		t.Fatalf("chunk 1: got %+v, want a continuation-key and no hash", got)
	}
	key := got.ContinuationKey

	resp, got = postSimChunk(t, ts.URL, "run-a", chunks[1], key, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 2: status %d", resp.StatusCode)
	}
	if got.ContinuationKey == "" || got.ContinuationKey == key {
		t.Fatalf("chunk 2: expected a fresh continuation-key, got %q", got.ContinuationKey)
	}
	key = got.ContinuationKey

	resp, got = postSimChunk(t, ts.URL, "run-a", chunks[2], key, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk: status %d", resp.StatusCode)
	}
	if got.ContinuationKey != "" {
		t.Fatalf("final chunk: expected no continuation-key, got %q", got.ContinuationKey)
	}
	if got.Hash != wantHex {
		t.Fatalf("hash mismatch: got %s, want %s", got.Hash, wantHex)
	}

	// Sim upload never writes to disk, chunked or not.
	entries, err := listDir(ulRoot)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files written to upload root, found %d", len(entries))
	}
}

func TestSimUploadChunkedReplayPreviousKey(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	_, got := postSimChunk(t, ts.URL, "run-b", []byte("aaa"), "", false)
	key1 := got.ContinuationKey

	resp, got := postSimChunk(t, ts.URL, "run-b", []byte("bbb"), key1, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 2: status %d", resp.StatusCode)
	}
	key2 := got.ContinuationKey

	// Simulate a lost ack: resend chunk 2 with the now-superseded key. Must
	// not be rehashed (which would corrupt the running hash) and must get
	// key2 back.
	resp, got = postSimChunk(t, ts.URL, "run-b", []byte("bbb"), key1, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay: status %d", resp.StatusCode)
	}
	if got.ContinuationKey != key2 {
		t.Fatalf("replay: got key %q, want %q", got.ContinuationKey, key2)
	}

	resp, got = postSimChunk(t, ts.URL, "run-b", []byte("ccc"), key2, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk: status %d", resp.StatusCode)
	}

	want := sha256.Sum256([]byte("aaabbbccc"))
	if wantHex := hex.EncodeToString(want[:]); got.Hash != wantHex {
		t.Fatalf("hash mismatch (replay must not double-hash): got %s, want %s", got.Hash, wantHex)
	}
}

func TestSimUploadChunkedCompletedReplay(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	_, got := postSimChunk(t, ts.URL, "run-c", []byte("aaa"), "", false)
	key := got.ContinuationKey
	resp, got := postSimChunk(t, ts.URL, "run-c", []byte("bbb"), key, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk: status %d", resp.StatusCode)
	}
	wantHash := got.Hash

	// Lost final ack: resend the exact same final chunk. Must be answered
	// idempotently with the same hash, not reprocessed.
	resp, got = postSimChunk(t, ts.URL, "run-c", []byte("bbb"), key, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("completed replay: status %d", resp.StatusCode)
	}
	if got.Hash != wantHash {
		t.Fatalf("completed replay: got hash %q, want %q", got.Hash, wantHash)
	}
	if got.ContinuationKey != "" {
		t.Fatalf("completed replay: expected no continuation-key, got %q", got.ContinuationKey)
	}
}

func TestSimUploadChunkedUnrecognizedKeyLeavesLiveSessionAlone(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	_, got := postSimChunk(t, ts.URL, "run-d", []byte("aaa"), "", false)
	key := got.ContinuationKey

	resp, _ := postSimChunk(t, ts.URL, "run-d", []byte("xxx"), "not-a-real-key", false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unrecognized key: status %d, want 400", resp.StatusCode)
	}

	resp, got = postSimChunk(t, ts.URL, "run-d", []byte("bbb"), key, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk after unrelated bad request: status %d", resp.StatusCode)
	}
	want := sha256.Sum256([]byte("aaabbb"))
	if wantHex := hex.EncodeToString(want[:]); got.Hash != wantHex {
		t.Fatalf("hash mismatch: got %s, want %s", got.Hash, wantHex)
	}
}

func TestSimUploadChunkedTooLarge(t *testing.T) {
	ulRoot := t.TempDir()
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	srv, err := NewServer(Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true}, db)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)

	// Seed a session whose running total is already within a few bytes of
	// the cap, so the cumulative-across-chunks check can be exercised
	// without actually transferring gigabytes of test data.
	const key = "seeded-key"
	srv.simUploadSessions.sessions["run-f"] = &simUlSession{
		ulSessionCore: ulSessionCore{currentKey: key, expiresAt: time.Now().Add(time.Hour)},
		hasher:        sha256.New(),
		totalBytes:    simMaxTotalSize - 3,
	}

	resp, _ := postSimChunk(t, ts.URL, "run-f", make([]byte, 10), key, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected rejection once cumulative size exceeds the cap, got %d", resp.StatusCode)
	}

	// The whole session must be torn down, not left stuck at an
	// over-the-cap state.
	if _, ok := srv.simUploadSessions.sessions["run-f"]; ok {
		t.Fatalf("expected the session to be discarded after the cap violation")
	}
}
