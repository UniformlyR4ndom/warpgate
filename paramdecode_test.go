package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"warpgate/internal/paramcrypt"
)

// listResponseEncrypted asserts rec's body is not directly valid JSON, then
// decrypts it under keys and unmarshals the result into v.
func listResponseEncrypted(t *testing.T, rec *httptest.ResponseRecorder, keys paramcrypt.Keys, v any) {
	t.Helper()
	if json.Valid(rec.Body.Bytes()) {
		t.Fatalf("expected an encrypted (non-JSON) response, got plain JSON: %s", rec.Body.String())
	}
	plaintext, err := paramcrypt.Decrypt(keys, rec.Body.String())
	if err != nil {
		t.Fatalf("response did not decrypt: %v", err)
	}
	if err := json.Unmarshal(plaintext, v); err != nil {
		t.Fatalf("decrypted response is not JSON: %v", err)
	}
}

func newParamDecodeTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg.Root = t.TempDir()
	srv, err := NewServer(cfg, db)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func TestDecodeParamsPlainJSON(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	var got struct {
		Path string `json:"path"`
	}
	encrypted, err := s.decodeParams(strings.NewReader(`{"path":"a.txt"}`), &got)
	if err != nil {
		t.Fatalf("decodeParams: %v", err)
	}
	if got.Path != "a.txt" {
		t.Fatalf("got %q, want %q", got.Path, "a.txt")
	}
	if encrypted {
		t.Fatal("plain JSON body should report encrypted=false")
	}
}

func TestDecodeParamsEncryptedBlob(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	blob, err := paramcrypt.Encrypt(s.paramKeys, []byte(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Path string `json:"path"`
	}
	encrypted, err := s.decodeParams(strings.NewReader(blob), &got)
	if err != nil {
		t.Fatalf("decodeParams: %v", err)
	}
	if got.Path != "a.txt" {
		t.Fatalf("got %q, want %q", got.Path, "a.txt")
	}
	if !encrypted {
		t.Fatal("encrypted blob should report encrypted=true")
	}
}

func TestDecodeParamsGarbageRejected(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	var got struct{}
	if _, err := s.decodeParams(strings.NewReader("not json and not base64!!!"), &got); err == nil {
		t.Fatal("expected error for garbage input")
	}
}

func TestDecodeParamsWrongKeyRejected(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	otherKeys := paramcrypt.DeriveKeys("a-different-key")
	blob, err := paramcrypt.Encrypt(otherKeys, []byte(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{}
	if _, err := s.decodeParams(strings.NewReader(blob), &got); err == nil {
		t.Fatal("expected error for a blob encrypted under a different key")
	}
}

func TestDecodeParamsOversizedBodyRejected(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	// Not valid JSON on its own, so decodeParams must fall through to the
	// decrypt path and reject it there on size, not hang trying to buffer it.
	big := bytes.Repeat([]byte("x"), maxParamBodySize+1)
	var got struct{}
	if _, err := s.decodeParams(bytes.NewReader(big), &got); err == nil {
		t.Fatal("expected error for oversized body")
	}
}

func TestDecodeParamsNoAuthRejectsEncryptedBlob(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true})
	// Built with a real key even though the server has none — simulates a
	// client mistakenly sending an encrypted blob to a -no-auth server.
	blob, err := paramcrypt.Encrypt(paramcrypt.DeriveKeys("some-key"), []byte(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{}
	if _, err := s.decodeParams(strings.NewReader(blob), &got); err == nil {
		t.Fatal("expected -no-auth server to reject an encrypted blob outright")
	}
}

func TestEncodeParamsResponsePlain(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	rec := httptest.NewRecorder()
	if err := s.encodeParamsResponse(rec, false, http.StatusOK, struct {
		Path string `json:"path"`
	}{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body is not plain JSON: %v", err)
	}
	if got.Path != "a.txt" {
		t.Fatalf("got %q, want %q", got.Path, "a.txt")
	}
}

func TestEncodeParamsResponseEncrypted(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	rec := httptest.NewRecorder()
	if err := s.encodeParamsResponse(rec, true, http.StatusCreated, struct {
		Path string `json:"path"`
	}{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	plaintext, err := paramcrypt.Decrypt(s.paramKeys, rec.Body.String())
	if err != nil {
		t.Fatalf("response body did not decrypt: %v", err)
	}
	var got struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(plaintext, &got); err != nil {
		t.Fatalf("decrypted body is not JSON: %v", err)
	}
	if got.Path != "a.txt" {
		t.Fatalf("got %q, want %q", got.Path, "a.txt")
	}
}

func TestHandleUlListQueryParamEncryption(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key", UlRoot: t.TempDir()})
	keys := paramcrypt.DeriveKeys("test-key")

	t.Run("with e param, response is encrypted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/ul-list?e=1", nil)
		rec := httptest.NewRecorder()
		s.handleUlList(rec, req)
		var got []listEntry
		listResponseEncrypted(t, rec, keys, &got)
	})

	t.Run("without e param, response is plain JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/files/ul-list", nil)
		rec := httptest.NewRecorder()
		s.handleUlList(rec, req)
		var got []listEntry
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("expected plain JSON, got %q: %v", rec.Body.String(), err)
		}
	})
}

func TestHandleUlListQueryParamIgnoredUnderNoAuth(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true, UlRoot: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/files/ul-list?e=1", nil)
	rec := httptest.NewRecorder()
	s.handleUlList(rec, req)
	var got []listEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("expected -no-auth to ignore ?e and return plain JSON, got %q: %v", rec.Body.String(), err)
	}
}

func TestHandleClipListQueryParamEncryption(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	keys := paramcrypt.DeriveKeys("test-key")

	t.Run("with e param, response is encrypted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/clip/list?e=1", nil)
		rec := httptest.NewRecorder()
		s.handleClipList(rec, req)
		var got []ClipItem
		listResponseEncrypted(t, rec, keys, &got)
	})

	t.Run("without e param, response is plain JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/clip/list", nil)
		rec := httptest.NewRecorder()
		s.handleClipList(rec, req)
		var got []ClipItem
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("expected plain JSON, got %q: %v", rec.Body.String(), err)
		}
	})
}

func TestHandleClipListQueryParamIgnoredUnderNoAuth(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true})
	req := httptest.NewRequest(http.MethodGet, "/api/clip/list?e=1", nil)
	rec := httptest.NewRecorder()
	s.handleClipList(rec, req)
	var got []ClipItem
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("expected -no-auth to ignore ?e and return plain JSON, got %q: %v", rec.Body.String(), err)
	}
}
