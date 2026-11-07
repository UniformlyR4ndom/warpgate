package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warpgate/internal/transform"
)

// slowUpload posts a one-shot upload of content, sending it in pieces with
// pause between them.
func slowUpload(t *testing.T, url string, content []byte, pieces int, pause time.Duration) (*http.Response, error) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "slow.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(content)
	mw.Close()
	body := buf.Bytes()

	pr, pw := io.Pipe()
	go func() {
		step := (len(body) + pieces - 1) / pieces
		for off := 0; off < len(body); off += step {
			if off > 0 {
				time.Sleep(pause)
			}
			if _, err := pw.Write(body[off:min(off+step, len(body))]); err != nil {
				return
			}
		}
		pw.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, url+"/api/files/upload", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.ContentLength = int64(len(body))
	return http.DefaultClient.Do(req)
}

func TestUploadBodyIdleTimeout(t *testing.T) {
	old := ulBodyIdleTimeout
	ulBodyIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { ulBodyIdleTimeout = old })
	ulRoot := t.TempDir()
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})
	content := bytes.Repeat([]byte("x"), 4096)

	// Progressing for well past the idle timeout in total.
	resp, err := slowUpload(t, ts.URL, content, 8, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("slow upload: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("slow upload: status %d", resp.StatusCode)
	}
	if got, _ := os.ReadFile(filepath.Join(ulRoot, "slow.bin")); !bytes.Equal(got, content) {
		t.Fatalf("slow upload stored %d bytes", len(got))
	}

	resp, err = slowUpload(t, ts.URL, []byte("stalled"), 2, time.Second)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatalf("stalled upload: status %d, want a failure", resp.StatusCode)
		}
	}
}

func TestAttachmentBodyLimit(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true})
	noteID := mustInsert(t, s.db, "n", "")
	key := strings.Repeat("ab", 16)

	post := func(file []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		spec, _ := json.Marshal(map[string]any{"transform": "html-b64", "args": map[string]string{"key": key}, "note_id": noteID, "mime": "image/png"})
		for _, part := range []struct {
			field string
			data  []byte
		}{{"transform", spec}, {"file", file}} {
			pw, err := mw.CreateFormFile(part.field, part.field)
			if err == nil {
				_, err = pw.Write(part.data)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/api/clip/attachment", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		s.handleClipAttachmentNew(rec, r)
		return rec
	}

	tr, err := transform.New("html-b64", json.RawMessage(`{"key":"`+key+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, clipAttachmentMaxSize)
	copy(raw, "\x89PNG\r\n\x1a\n")
	encoded, err := tr.Transform(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := io.ReadAll(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if rec := post(wire); rec.Code != http.StatusOK {
		t.Fatalf("largest image as html-b64 (%d bytes on the wire): status %d %s", len(wire), rec.Code, rec.Body.String())
	}
	if rec := post(make([]byte, clipAttachmentMaxBody)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: status %d, want 413", rec.Code)
	}
}
