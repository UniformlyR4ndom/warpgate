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

// TestDownloadBlockedDuringChunkedUpload covers the scenario CLAUDE.md's
// former TODO flagged: -root and -ul-root pointed at the same directory,
// so a file mid-chunked-upload is also a download candidate.
func TestDownloadBlockedDuringChunkedUpload(t *testing.T) {
	dir := t.TempDir()
	ts := newSimTestServer(t, Config{Root: dir, UlRoot: dir, NoAuth: true})

	body := downloadRequest{Path: "f.txt", Size: 5, Transform: "id"}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	download := func() *http.Response {
		res, err := http.Post(ts.URL+"/api/files/download", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		return res
	}

	// Not uploaded yet at all: a plain miss, not a lock conflict.
	if res := download(); res.StatusCode != http.StatusNotFound {
		t.Fatalf("status before upload = %d, want %d", res.StatusCode, http.StatusNotFound)
	}

	resp, got := postChunk(t, ts.URL, "f.txt", []byte("hel"), "", false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first chunk status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	if res := download(); res.StatusCode != http.StatusConflict {
		t.Fatalf("status mid-upload = %d, want %d", res.StatusCode, http.StatusConflict)
	}

	resp, _ = postChunk(t, ts.URL, "f.txt", []byte("lo"), got.ContinuationKey, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final chunk status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	res := download()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status after completion = %d, want %d", res.StatusCode, http.StatusOK)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("downloaded %q, want %q", data, "hello")
	}
}

// TestDownloadNotBlockedByUnrelatedUpload confirms an in-progress upload
// under a separate -ul-root never blocks a same-named download under -root
// — downloadBlockedByUpload must compare resolved absolute paths, not bare
// filenames.
func TestDownloadNotBlockedByUnrelatedUpload(t *testing.T) {
	root := t.TempDir()
	ulRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ts := newSimTestServer(t, Config{Root: root, UlRoot: ulRoot, NoAuth: true})

	resp, _ := postChunk(t, ts.URL, "f.txt", []byte("hel"), "", false)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first chunk status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	body := downloadRequest{Path: "f.txt", Size: 5, Transform: "id"}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	res, err := http.Post(ts.URL+"/api/files/download", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
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
