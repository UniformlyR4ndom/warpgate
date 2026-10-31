package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"unicode"

	"warpgate/internal/transform"
)

type downloadRequest struct {
	Path        string          `json:"path"`
	Start       int64           `json:"start"`
	Size        int64           `json:"size"`
	Transform   string          `json:"transform"`
	Args        json.RawMessage `json:"args"`
	ContentType string          `json:"contentType"`
}

// maxContentTypeLen bounds a client-supplied Content-Type override; actual
// MIME types are well under this, so it only rejects garbage input.
const maxContentTypeLen = 255

// validContentType rejects control characters and empty/oversized input; it
// isn't a full MIME-type grammar check, just a boundary sanity check on
// otherwise-arbitrary client input reflected into a response header.
func validContentType(s string) bool {
	if s == "" || len(s) > maxContentTypeLen {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// resolveContentType picks a response Content-Type: a transform's own type
// (pdf/html-b64) wins over fallback, and a non-empty client override wins over
// both.
func resolveContentType(t transform.Transformer, override, fallback string) string {
	ct := fallback
	if ctr, ok := t.(interface{ ContentType() string }); ok {
		ct = ctr.ContentType()
	}
	if override != "" {
		ct = override
	}
	return ct
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req downloadRequest
	if _, err := s.decodeParams(r.Body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusUnauthorized)
		return
	}

	if req.Size <= 0 {
		http.Error(w, "size must be positive", http.StatusBadRequest)
		return
	}

	rel, err := rootRelPath(req.Path)
	if err != nil {
		log.Printf("invalid path %q requested", req.Path)
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	transformer, err := transform.New(req.Transform, req.Args)
	if err != nil {
		http.Error(w, "invalid transform: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ContentType != "" && !validContentType(req.ContentType) {
		http.Error(w, "invalid contentType", http.StatusBadRequest)
		return
	}

	if s.isUlTmpPath(filepath.Join(s.absRoot, rel)) {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	f, err := s.root.Open(rel)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
		} else if isPathEscape(err) {
			http.Error(w, errSymlinkEscape, http.StatusForbidden)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	defer f.Close()

	if req.Start > 0 {
		if _, err := f.Seek(req.Start, io.SeekStart); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	reader, err := transformer.Transform(io.LimitReader(f, req.Size))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", resolveContentType(transformer, req.ContentType, "application/octet-stream"))
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("error writing response for %q: %v", req.Path, err)
	}
}
