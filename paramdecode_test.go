package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	srv := newTestServer(t, cfg, db)
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

// With auth on, a garbage body can't be told from a wrong-key encrypted one,
// so it stays the deliberately ambiguous errParamsInvalid (→ 401).
func TestDecodeParamsGarbageRejected(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	var got struct{}
	_, err := s.decodeParams(strings.NewReader("not json and not base64!!!"), &got)
	if !errors.Is(err, errParamsInvalid) {
		t.Fatalf("got %v, want errParamsInvalid", err)
	}
}

func TestDecodeParamsWrongKeyRejected(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	otherKeys := paramcrypt.DeriveKeys(testK("a-different-key"))
	blob, err := paramcrypt.Encrypt(otherKeys, []byte(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{}
	if _, err := s.decodeParams(strings.NewReader(blob), &got); !errors.Is(err, errParamsInvalid) {
		t.Fatalf("got %v, want errParamsInvalid", err)
	}
}

// An oversize body is a size problem, not an auth one: errParamsTooLarge (→ 413).
func TestDecodeParamsOversizedBodyRejected(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	big := bytes.Repeat([]byte("x"), maxParamBodySize+1)
	var got struct{}
	if _, err := s.decodeParams(bytes.NewReader(big), &got); !errors.Is(err, errParamsTooLarge) {
		t.Fatalf("got %v, want errParamsTooLarge", err)
	}
}

// Under -no-auth there is no encryption to fall back to, so an undecodable body
// is unambiguously malformed: errParamsMalformed (→ 400), never 401.
func TestDecodeParamsNoAuthRejectsEncryptedBlob(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true})
	// Built with a real key even though the server has none — simulates a
	// client mistakenly sending an encrypted blob to a -no-auth server.
	blob, err := paramcrypt.Encrypt(paramcrypt.DeriveKeys(testK("some-key")), []byte(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{}
	if _, err := s.decodeParams(strings.NewReader(blob), &got); !errors.Is(err, errParamsMalformed) {
		t.Fatalf("got %v, want errParamsMalformed", err)
	}
}

// The report's concrete case: a clipboard note whose body crosses the
// parameter-body limit must come back 413 (a size error), not 401 — which the
// browser would read as a wrong key and latch the whole UI. Checked under
// -no-auth too, where a 401 would be especially nonsensical.
func TestHandleClipNewOversizeIsNotUnauthorized(t *testing.T) {
	for _, noAuth := range []bool{false, true} {
		cfg := Config{Key: "test-key"}
		if noAuth {
			cfg = Config{NoAuth: true}
		}
		s := newParamDecodeTestServer(t, cfg)
		body := bytes.Repeat([]byte("x"), maxParamBodySize+1)
		rec := httptest.NewRecorder()
		s.handleClipNew(rec, httptest.NewRequest(http.MethodPost, "/api/clip/new", bytes.NewReader(body)))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("noAuth=%v: oversize note status = %d, want %d", noAuth, rec.Code, http.StatusRequestEntityTooLarge)
		}
	}
}

// Under -no-auth a malformed body is a 400, never 401.
func TestHandleClipNewMalformedNoAuthIsBadRequest(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true})
	rec := httptest.NewRecorder()
	s.handleClipNew(rec, httptest.NewRequest(http.MethodPost, "/api/clip/new", strings.NewReader("not json!!!")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body under -no-auth = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// writeParamError maps each decodeParams failure to its intended status.
func TestWriteParamErrorStatus(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	for _, tc := range []struct {
		err  error
		want int
	}{
		{errParamsTooLarge, http.StatusRequestEntityTooLarge},
		{errParamsMalformed, http.StatusBadRequest},
		{errParamsInvalid, http.StatusUnauthorized},
	} {
		rec := httptest.NewRecorder()
		s.writeParamError(rec, tc.err)
		if rec.Code != tc.want {
			t.Errorf("writeParamError(%v) = %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}

func TestEncodeParamsResponsePlain(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	rec := httptest.NewRecorder()
	s.encodeParamsResponse(rec, false, http.StatusOK, struct {
		Path string `json:"path"`
	}{"a.txt"})
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
	s.encodeParamsResponse(rec, true, http.StatusCreated, struct {
		Path string `json:"path"`
	}{"a.txt"})
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
	keys := paramcrypt.DeriveKeys(testK("test-key"))

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

func TestHandleClipStateQueryParamEncryption(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	keys := paramcrypt.DeriveKeys(testK("test-key"))

	t.Run("with e param, response is encrypted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/clip/state?e=1", nil)
		rec := httptest.NewRecorder()
		s.handleClipState(rec, req)
		var got clipStateResponse
		listResponseEncrypted(t, rec, keys, &got)
	})

	t.Run("without e param, response is plain JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/clip/state", nil)
		rec := httptest.NewRecorder()
		s.handleClipState(rec, req)
		var got clipStateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("expected plain JSON, got %q: %v", rec.Body.String(), err)
		}
	})
}

func TestHandleClipStateQueryParamIgnoredUnderNoAuth(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true})
	req := httptest.NewRequest(http.MethodGet, "/api/clip/state?e=1", nil)
	rec := httptest.NewRecorder()
	s.handleClipState(rec, req)
	var got clipStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("expected -no-auth to ignore ?e and return plain JSON, got %q: %v", rec.Body.String(), err)
	}
}

// TestEncodeParamsResponseFailure: a body that can't be encoded is a 500, not
// an empty response with the intended status.
func TestEncodeParamsResponseFailure(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{Key: "test-key"})
	rec := httptest.NewRecorder()
	s.encodeParamsResponse(rec, false, http.StatusCreated, make(chan int))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
