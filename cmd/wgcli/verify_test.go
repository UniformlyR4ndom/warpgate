package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// alteringServer serves content, but flips a byte in downloads when
// alterDownload is set, and reports a wrong hash for uploads.
func alteringServer(t *testing.T, content []byte, alterDownload bool) *httptest.Server {
	sum := sha256.Sum256(content)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/files/list":
			_, _ = fmt.Fprintf(w, `[{"name":"f.bin","type":"f","size":%d,"sha256":%q}]`, len(content), hex.EncodeToString(sum[:]))
		case "/api/files/download":
			var req struct{ Start, Size int64 }
			_ = json.NewDecoder(r.Body).Decode(&req)
			chunk := append([]byte(nil), content[req.Start:req.Start+req.Size]...)
			if alterDownload {
				chunk[0] ^= 1
			}
			_, _ = w.Write(chunk)
		case "/api/files/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"f.bin","sha256":"00"}`))
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestDownloadVerify(t *testing.T) {
	content := []byte("content to verify")
	out := filepath.Join(t.TempDir(), "out.bin")
	if err := runDownload([]string{"-url", alteringServer(t, content, false).URL, "-o", out, "f.bin"}); err != nil {
		t.Fatalf("intact download: %v", err)
	}
	err := runDownload([]string{"-url", alteringServer(t, content, true).URL, "-o", out, "f.bin"})
	if err == nil || !strings.Contains(err.Error(), "altered in transit") {
		t.Fatalf("altered download: err = %v", err)
	}
	// The failed download must leave the earlier, intact file alone.
	if got, err := os.ReadFile(out); err != nil || string(got) != string(content) {
		t.Errorf("after the altered download, %s = %q, %v", out, got, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 1 {
		t.Errorf("altered download left a temporary file behind: %d entries", len(entries))
	}
	if err := runDownload([]string{"-url", alteringServer(t, content, true).URL, "-o", out, "-no-verify", "f.bin"}); err != nil {
		t.Fatalf("-no-verify: %v", err)
	}
}

func TestUploadVerify(t *testing.T) {
	local := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(local, []byte("upload"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runUpload([]string{"-url", alteringServer(t, nil, false).URL, local})
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("wrong server hash: err = %v", err)
	}
}

func TestDownloadDepthLimit(t *testing.T) {
	// Every directory contains another: what a symlink loop looks like.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"d","type":"d"}]`))
	}))
	defer ts.Close()
	err := runDownload([]string{"-url", ts.URL, "-r", "-o", t.TempDir(), "d"})
	if err == nil || !strings.Contains(err.Error(), "1 of 1 files failed") {
		t.Fatalf("err = %v", err)
	}
}
