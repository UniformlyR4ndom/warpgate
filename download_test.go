package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestDownloadContentType(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ts := newSimTestServer(t, Config{Root: root, NoAuth: true})

	tests := []struct {
		name       string
		body       downloadRequest
		wantStatus int
		wantCT     string
	}{
		{
			name:       "default id transform",
			body:       downloadRequest{Path: "f.txt", Size: 11, Transform: "id"},
			wantStatus: http.StatusOK,
			wantCT:     "application/octet-stream",
		},
		{
			name:       "pdf default",
			body:       downloadRequest{Path: "f.txt", Size: 11, Transform: "pdf", Args: json.RawMessage(`{"filename":"f.txt"}`)},
			wantStatus: http.StatusOK,
			wantCT:     "application/pdf",
		},
		{
			name:       "override preset",
			body:       downloadRequest{Path: "f.txt", Size: 11, Transform: "id", ContentType: "image/jpeg"},
			wantStatus: http.StatusOK,
			wantCT:     "image/jpeg",
		},
		{
			name:       "override beats transform default",
			body:       downloadRequest{Path: "f.txt", Size: 11, Transform: "pdf", Args: json.RawMessage(`{"filename":"f.txt"}`), ContentType: "application/octet-stream"},
			wantStatus: http.StatusOK,
			wantCT:     "application/octet-stream",
		},
		{
			name:       "rejects control characters",
			body:       downloadRequest{Path: "f.txt", Size: 11, Transform: "id", ContentType: "text/html\r\nX-Injected: yes"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty means no override",
			body:       downloadRequest{Path: "f.txt", Size: 11, Transform: "id", ContentType: ""},
			wantStatus: http.StatusOK,
			wantCT:     "application/octet-stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			res, err := http.Post(ts.URL+"/api/files/download", "application/json", bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK {
				if got := res.Header.Get("Content-Type"); got != tt.wantCT {
					t.Fatalf("Content-Type = %q, want %q", got, tt.wantCT)
				}
			}
		})
	}
}

// TestDownloadDuringChunkedUpload: with -root and -ul-root the same
// directory, a file being re-uploaded keeps serving its previous version
// until the final chunk publishes the new one.
func TestDownloadDuringChunkedUpload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("old!!"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ts := newSimTestServer(t, Config{Root: dir, UlRoot: dir, NoAuth: true})

	raw, err := json.Marshal(downloadRequest{Path: "f.txt", Size: 5, Transform: "id"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	download := func() string {
		t.Helper()
		res, err := http.Post(ts.URL+"/api/files/download", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("download status = %d", res.StatusCode)
		}
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		return string(data)
	}

	resp, got := postChunk(t, ts.URL, "f.txt", []byte("hel"), "", false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first chunk status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if d := download(); d != "old!!" {
		t.Fatalf("mid-upload download = %q, want the previous version", d)
	}

	resp, _ = postChunk(t, ts.URL, "f.txt", []byte("lo"), got.ContinuationKey, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if d := download(); d != "hello" {
		t.Fatalf("downloaded %q, want %q", d, "hello")
	}
}

// TestUlTmpDirNotReachable: with -root and -ul-root the same directory, the
// upload temp dir must be neither listed nor downloadable, and its name is
// reserved for uploads.
func TestUlTmpDirNotReachable(t *testing.T) {
	dir := t.TempDir()
	ts := newSimTestServer(t, Config{Root: dir, UlRoot: dir, NoAuth: true})

	if resp, _ := postChunk(t, ts.URL, "f.txt", []byte("hel"), "", false); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first chunk status = %d", resp.StatusCode)
	}
	tmp, err := os.ReadDir(filepath.Join(dir, ulTmpDirName))
	if err != nil || len(tmp) != 1 {
		t.Fatalf("expected one temp file, got %v, %v", tmp, err)
	}
	tmpRel := ulTmpDirName + "/" + tmp[0].Name()

	post := func(path string, body any) *http.Response {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		res, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}

	res := post("/api/files/list", listRequest{Path: ""})
	var entries []listEntry
	if err := json.NewDecoder(res.Body).Decode(&entries); err != nil {
		t.Fatalf("decoding list: %v", err)
	}
	for _, e := range entries {
		if e.Name == ulTmpDirName {
			t.Fatalf("temp dir listed: %+v", entries)
		}
	}
	if res := post("/api/files/list", listRequest{Path: ulTmpDirName}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("listing temp dir: status %d, want 404", res.StatusCode)
	}
	if res := post("/api/files/download", downloadRequest{Path: tmpRel, Size: 3, Transform: "id"}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("downloading temp file: status %d, want 404", res.StatusCode)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(wsDownloadRequest{Path: tmpRel, Transform: "id"}); err != nil {
		t.Fatalf("writing request: %v", err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil || ack.OK {
		t.Fatalf("WS download of temp file: ack %+v, err %v", ack, err)
	}

	if resp, _ := postChunk(t, ts.URL, ulTmpDirName, []byte("x"), "", true); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("upload named %q: status %d, want 400", ulTmpDirName, resp.StatusCode)
	}
}

func TestUlTmpDirClearedAtStartup(t *testing.T) {
	ulRoot := t.TempDir()
	leftover := filepath.Join(ulRoot, ulTmpDirName, "ul-stale")
	if err := os.MkdirAll(filepath.Dir(leftover), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(leftover, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("leftover temp file survived startup, stat err=%v", err)
	}
}

func TestValidContentType(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"", false},
		{"application/octet-stream", true},
		{"image/jpeg", true},
		{"text/html\r\nX-Injected: yes", false},
		{"bad\x00null", false},
		{strings.Repeat("a", maxContentTypeLen+1), false},
	}
	for _, tt := range tests {
		if got := validContentType(tt.s); got != tt.want {
			t.Errorf("validContentType(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}
