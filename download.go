package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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

const errInvalidRange = "start must be non-negative and size positive"

func validRange(start, size int64) bool {
	return start >= 0 && size > 0
}

var maxBufferedPlain int64 = transform.MaxBufferedPlain

// bufferedTooLarge is the error for a download a buffering transform would
// have to hold in memory beyond maxBufferedPlain; "" if it fits.
func bufferedTooLarge(name string, t transform.Transformer, plain int64) string {
	if !transform.Buffers(t) || plain <= maxBufferedPlain {
		return ""
	}
	return bufferedLimitMsg(name)
}

// bufferedUploadTooLarge is bufferedTooLarge for encoded upload data.
func bufferedUploadTooLarge(name string, t transform.Transformer, encoded int64) string {
	if !transform.Buffers(t) || encoded <= transform.MaxEncodedLen(t, maxBufferedPlain) {
		return ""
	}
	return bufferedLimitMsg(name)
}

func bufferedLimitMsg(name string) string {
	return fmt.Sprintf("transform %q handles at most %d bytes per request; use smaller chunks", name, maxBufferedPlain)
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

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request, root *os.Root) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req downloadRequest
	if _, err := s.decodeParams(r.Body, &req); err != nil {
		s.writeParamError(w, err)
		return
	}

	if !validRange(req.Start, req.Size) {
		http.Error(w, errInvalidRange, http.StatusBadRequest)
		return
	}

	rel, err := rootRelPath(req.Path)
	if err != nil {
		log.Printf("invalid path %q requested", req.Path)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	transformer, err := transform.New(req.Transform, req.Args)
	if err != nil {
		http.Error(w, "invalid transform: "+err.Error(), http.StatusBadRequest)
		return
	}

	if msg := bufferedTooLarge(req.Transform, transformer, req.Size); msg != "" {
		http.Error(w, msg, http.StatusRequestEntityTooLarge)
		return
	}

	if req.ContentType != "" && !validContentType(req.ContentType) {
		http.Error(w, "invalid contentType", http.StatusBadRequest)
		return
	}

	if s.reachesUlTmp(root, rel) {
		http.Error(w, errUlTmpPath, http.StatusForbidden)
		return
	}

	f, err := root.Open(rel)
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

	if _, err := f.Seek(req.Start, io.SeekStart); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
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
