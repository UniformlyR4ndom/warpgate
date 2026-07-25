package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// multipartChunkedUpload builds a multipart/form-data body with a
// "transform" part carrying chunking fields plus a "file" part, matching
// what handleUpload expects for a chunked (or plain) upload request.
func multipartChunkedUpload(t *testing.T, transformName, filename string, content []byte, continuationKey string, done bool) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Done has no omitempty: encoding/json's omitempty treats false as empty
	// for a bool, which would silently drop "ul-done" from the request
	// whenever a caller passes done=false — indistinguishable server-side
	// from the field being absent entirely, which now defaults to true.
	spec, err := json.Marshal(struct {
		Transform       string `json:"transform"`
		ContinuationKey string `json:"continuation-key,omitempty"`
		Done            bool   `json:"ul-done"`
	}{Transform: transformName, ContinuationKey: continuationKey, Done: done})
	if err != nil {
		t.Fatalf("marshaling transform spec: %v", err)
	}
	tw, err := w.CreateFormFile("transform", "transform.json")
	if err != nil {
		t.Fatalf("CreateFormFile(transform): %v", err)
	}
	if _, err := tw.Write(spec); err != nil {
		t.Fatalf("writing transform part: %v", err)
	}

	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile(file): %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("writing file part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

func postChunk(t *testing.T, url string, filename string, content []byte, continuationKey string, done bool) (*http.Response, uploadResponse) {
	t.Helper()
	return postChunkWithTransform(t, url, "id", filename, content, continuationKey, done)
}

func postChunkWithTransform(t *testing.T, url, transformName, filename string, content []byte, continuationKey string, done bool) (*http.Response, uploadResponse) {
	t.Helper()
	body, contentType := multipartChunkedUpload(t, transformName, filename, content, continuationKey, done)
	resp, err := http.Post(url+"/api/files/upload", contentType, body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var got uploadResponse
	if resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return resp, got
}

func TestUploadChunkedRoundTrip(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	chunks := [][]byte{
		[]byte("first chunk of the file "),
		[]byte("second chunk of the file "),
		[]byte("third and final chunk"),
	}
	var want []byte
	for _, c := range chunks {
		want = append(want, c...)
	}

	resp, got := postChunk(t, ts.URL, "big.bin", chunks[0], "", false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("chunk 1: status %d", resp.StatusCode)
	}
	if got.ContinuationKey == "" {
		t.Fatalf("chunk 1: expected a continuation-key")
	}
	key := got.ContinuationKey

	resp, got = postChunk(t, ts.URL, "big.bin", chunks[1], key, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 2: status %d", resp.StatusCode)
	}
	if got.ContinuationKey == "" || got.ContinuationKey == key {
		t.Fatalf("chunk 2: expected a fresh continuation-key, got %q", got.ContinuationKey)
	}
	key = got.ContinuationKey

	resp, got = postChunk(t, ts.URL, "big.bin", chunks[2], key, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 3: status %d", resp.StatusCode)
	}
	if got.ContinuationKey != "" {
		t.Fatalf("final chunk: expected no continuation-key, got %q", got.ContinuationKey)
	}

	gotContent, err := os.ReadFile(filepath.Join(ulRoot, "big.bin"))
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if !bytes.Equal(gotContent, want) {
		t.Fatalf("content mismatch: got %q, want %q", gotContent, want)
	}
}

func TestUploadChunkedReplayPreviousKey(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	_, got := postChunk(t, ts.URL, "f.bin", []byte("aaa"), "", false)
	key1 := got.ContinuationKey

	resp, got := postChunk(t, ts.URL, "f.bin", []byte("bbb"), key1, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 2: status %d", resp.StatusCode)
	}
	key2 := got.ContinuationKey

	// Simulate a lost ack: resend chunk 2 with the now-superseded key1. It
	// must not be reprocessed (no duplicated bytes) and must get key2 back.
	resp, got = postChunk(t, ts.URL, "f.bin", []byte("bbb"), key1, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay: status %d", resp.StatusCode)
	}
	if got.ContinuationKey != key2 {
		t.Fatalf("replay: got key %q, want %q", got.ContinuationKey, key2)
	}

	resp, got = postChunk(t, ts.URL, "f.bin", []byte("ccc"), key2, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk: status %d", resp.StatusCode)
	}

	gotContent, err := os.ReadFile(filepath.Join(ulRoot, "f.bin"))
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if want := "aaabbbccc"; string(gotContent) != want {
		t.Fatalf("content mismatch (replay must not duplicate bytes): got %q, want %q", gotContent, want)
	}
}

func TestUploadChunkedCompletedReplay(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	_, got := postChunk(t, ts.URL, "f.bin", []byte("aaa"), "", false)
	key := got.ContinuationKey
	resp, got := postChunk(t, ts.URL, "f.bin", []byte("bbb"), key, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk: status %d", resp.StatusCode)
	}

	// Lost final ack: resend the exact same final chunk request. Must be
	// answered idempotently, not reprocessed.
	resp, got = postChunk(t, ts.URL, "f.bin", []byte("bbb"), key, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("completed replay: status %d", resp.StatusCode)
	}
	if got.ContinuationKey != "" {
		t.Fatalf("completed replay: expected no continuation-key, got %q", got.ContinuationKey)
	}

	gotContent, err := os.ReadFile(filepath.Join(ulRoot, "f.bin"))
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if want := "aaabbb"; string(gotContent) != want {
		t.Fatalf("content mismatch (replay must not duplicate bytes): got %q, want %q", gotContent, want)
	}
}

func TestUploadChunkedUnrecognizedKeyLeavesLiveSessionAlone(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	_, got := postChunk(t, ts.URL, "f.bin", []byte("aaa"), "", false)
	key := got.ContinuationKey

	// A bogus/stale key for the same filename must be rejected without
	// touching the live session.
	resp, _ := postChunk(t, ts.URL, "f.bin", []byte("xxx"), "not-a-real-key", false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unrecognized key: status %d, want 400", resp.StatusCode)
	}

	// The real session must still be usable with its actual key.
	resp, got = postChunk(t, ts.URL, "f.bin", []byte("bbb"), key, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk after unrelated bad request: status %d", resp.StatusCode)
	}

	gotContent, err := os.ReadFile(filepath.Join(ulRoot, "f.bin"))
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if want := "aaabbb"; string(gotContent) != want {
		t.Fatalf("content mismatch: got %q, want %q", gotContent, want)
	}
}

func TestUploadChunkedLockRejection(t *testing.T) {
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

	newUlSession := func() *ulSession { return &ulSession{} }

	_, wasNew, ok := srv.uploadSessions.tryClaim("f.bin", newUlSession)
	if !ok || !wasNew {
		t.Fatalf("first claim: ok=%v wasNew=%v, want true,true", ok, wasNew)
	}
	if _, _, ok := srv.uploadSessions.tryClaim("f.bin", newUlSession); ok {
		t.Fatalf("second concurrent claim on the same filename unexpectedly succeeded")
	}
	// A different filename must not be affected.
	if _, _, ok := srv.uploadSessions.tryClaim("other.bin", newUlSession); !ok {
		t.Fatalf("claim on an unrelated filename unexpectedly rejected")
	}

	srv.uploadSessions.release("f.bin", false)
	if _, _, ok := srv.uploadSessions.tryClaim("f.bin", newUlSession); !ok {
		t.Fatalf("claim after release unexpectedly rejected")
	}
}

func TestUploadOneShotBackwardCompatible(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	// A plain multipart upload with only a "file" part, matching today's
	// existing (pre-chunking) clients exactly — no "transform" part at all,
	// so no continuation-key/ul-done fields either.
	content := []byte("plain one-shot upload content")
	body, contentType := multipartUpload(t, "plain.bin", content)
	resp, err := http.Post(ts.URL+"/api/files/upload", contentType, body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var got uploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	// No session may be left open: a real client that never learned about
	// chunking will never send a follow-up request, so a stray
	// continuation-key here means the file would sit hidden from ul-list
	// and eventually get deleted by ulSweepExpired.
	if got.ContinuationKey != "" {
		t.Fatalf("expected no continuation-key on a plain one-shot upload, got %q", got.ContinuationKey)
	}

	gotContent, err := os.ReadFile(filepath.Join(ulRoot, "plain.bin"))
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if !bytes.Equal(gotContent, content) {
		t.Fatalf("content mismatch: got %q, want %q", gotContent, content)
	}

	resp2, err := http.Get(ts.URL + "/api/files/ul-list")
	if err != nil {
		t.Fatalf("GET ul-list: %v", err)
	}
	defer resp2.Body.Close()
	var entries []listEntry
	if err := json.NewDecoder(resp2.Body).Decode(&entries); err != nil {
		t.Fatalf("decoding ul-list: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "plain.bin" {
		t.Fatalf("expected plain.bin immediately visible in ul-list, got %+v", entries)
	}
}

func TestUploadChunkedHiddenFromListing(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	listNames := func() []string {
		t.Helper()
		resp, err := http.Get(ts.URL + "/api/files/ul-list")
		if err != nil {
			t.Fatalf("GET ul-list: %v", err)
		}
		defer resp.Body.Close()
		var entries []listEntry
		if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
			t.Fatalf("decoding ul-list: %v", err)
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name
		}
		return names
	}

	_, got := postChunk(t, ts.URL, "f.bin", []byte("aaa"), "", false)
	key := got.ContinuationKey

	if names := listNames(); len(names) != 0 {
		t.Fatalf("in-progress upload visible in listing: %v", names)
	}

	resp, _ := postChunk(t, ts.URL, "f.bin", []byte("bbb"), key, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk: status %d", resp.StatusCode)
	}

	if names := listNames(); len(names) != 1 || names[0] != "f.bin" {
		t.Fatalf("completed upload not visible in listing: %v", names)
	}
}

func TestUploadChunkedFailureDeletesEverything(t *testing.T) {
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	_, got := postChunk(t, ts.URL, "f.bin", []byte("aaa"), "", false)
	key := got.ContinuationKey

	// "pdf" constructs successfully with no args but fails at Untransform
	// time on non-PDF content, exercising the failure path after the
	// session/lock is already established.
	resp, _ := postChunkWithTransform(t, ts.URL, "pdf", "f.bin", []byte("not a pdf"), key, false)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failing chunk: status %d, want 500", resp.StatusCode)
	}

	if _, err := os.Stat(filepath.Join(ulRoot, "f.bin")); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted after failure, stat err=%v", err)
	}

	// The old key must no longer be recognized: the whole session was torn
	// down, not just the failed chunk.
	resp2, _ := postChunk(t, ts.URL, "f.bin", []byte("bbb"), key, true)
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected unrecognized-key rejection after teardown, got %d", resp2.StatusCode)
	}
}

func TestUlSweepExpired(t *testing.T) {
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

	// An active (non-completed) session past expiry: its partial file must
	// be deleted along with the session entry.
	if err := os.WriteFile(filepath.Join(ulRoot, "active.bin"), []byte("partial"), 0o644); err != nil {
		t.Fatalf("seeding partial file: %v", err)
	}
	srv.uploadSessions.sessions["active.bin"] = &ulSession{ulSessionCore{currentKey: "k1", expiresAt: time.Now().Add(-time.Second)}}

	// A completed session past expiry: the file is done and correct, and
	// must survive the sweep untouched.
	if err := os.WriteFile(filepath.Join(ulRoot, "done.bin"), []byte("finished"), 0o644); err != nil {
		t.Fatalf("seeding completed file: %v", err)
	}
	srv.uploadSessions.sessions["done.bin"] = &ulSession{ulSessionCore{completed: true, currentKey: "k2", expiresAt: time.Now().Add(-time.Second)}}

	// A session not yet expired must be left alone entirely.
	srv.uploadSessions.sessions["fresh.bin"] = &ulSession{ulSessionCore{currentKey: "k3", expiresAt: time.Now().Add(time.Hour)}}

	srv.uploadSessions.sweepExpired(srv.abortUploadSession)

	if _, err := os.Stat(filepath.Join(ulRoot, "active.bin")); !os.IsNotExist(err) {
		t.Fatalf("expected active.bin's partial file to be deleted, stat err=%v", err)
	}
	if _, ok := srv.uploadSessions.sessions["active.bin"]; ok {
		t.Fatalf("expected active.bin's session entry to be purged")
	}

	if _, err := os.Stat(filepath.Join(ulRoot, "done.bin")); err != nil {
		t.Fatalf("completed file was deleted by sweep: %v", err)
	}
	if _, ok := srv.uploadSessions.sessions["done.bin"]; ok {
		t.Fatalf("expected done.bin's session entry to be purged")
	}

	if _, ok := srv.uploadSessions.sessions["fresh.bin"]; !ok {
		t.Fatalf("non-expired session was incorrectly purged")
	}
}
