package main

import (
	"bytes"
	"encoding/json"
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
