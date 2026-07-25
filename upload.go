package main

import (
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"warpgate/internal/transform"
)

type uploadResponse struct {
	Name string `json:"name,omitempty"`
	// ContinuationKey is set whenever more chunks are expected: absent on a
	// plain one-shot upload, the final chunk's success response, and a
	// completed-session replay ack.
	ContinuationKey string `json:"continuation-key,omitempty"`
}

// ulSession is a chunked real-file upload's session payload — the file
// itself, reopened via the map key (filename) on each request, is the
// actual persisted state, so nothing beyond the shared core is needed here.
type ulSession struct {
	ulSessionCore
}

// abortUploadSession deletes the partial file left behind by an expired or
// failed, incomplete chunked upload.
func (s *Server) abortUploadSession(name string, _ *ulSession) {
	path, err := makePath(s.absUlRoot, name)
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("ul session expiry: removing %q: %v", path, err)
	}
}

func writeUploadResponse(w http.ResponseWriter, status int, filename, continuationKey string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(uploadResponse{Name: filename, ContinuationKey: continuationKey})
}

// handleUpload serves both a plain one-shot upload (no continuation-key,
// ul-done true or omitted for backward compatibility with callers that
// don't know about chunking yet) and each chunk of a multi-request chunked
// upload. See "Upload" in CLAUDE.md for the full protocol.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Bounds how long this request's body may take to arrive; a client that
	// stalls mid-send fails ParseMultipartForm below rather than holding a
	// per-filename claim open indefinitely.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetReadDeadline(time.Now().Add(ulChunkProcessTimeout))
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	// Read optional transform spec (plus chunking fields) from the
	// "transform" file part.
	transformName := "id"
	var transformArgs json.RawMessage
	var continuationKey string
	// done defaults to true (one-shot) when ul-done is absent entirely, not
	// just falsy: Done is a *bool specifically so a legacy client that omits
	// the field completes immediately instead of silently starting a
	// chunked session it will never continue.
	done := true
	if tf, _, err := r.FormFile("transform"); err == nil {
		defer tf.Close()
		var spec struct {
			Transform       string          `json:"transform"`
			Args            json.RawMessage `json:"args"`
			ContinuationKey string          `json:"continuation-key"`
			Done            *bool           `json:"ul-done"`
		}
		if err := json.NewDecoder(tf).Decode(&spec); err != nil {
			http.Error(w, "invalid transform spec", http.StatusBadRequest)
			return
		}
		transformName = spec.Transform
		transformArgs = spec.Args
		continuationKey = spec.ContinuationKey
		if spec.Done != nil {
			done = *spec.Done
		}
	}

	transformer, err := transform.New(transformName, transformArgs)
	if err != nil {
		http.Error(w, "invalid transform: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Normalize separators so Windows client paths are handled correctly.
	rawName := strings.ReplaceAll(header.Filename, "\\", "/")
	filename := filepath.Base(rawName)
	if filename == "" || filename == "." || filename == "/" {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	targetPath, err := makePath(s.absUlRoot, filename)
	if err != nil {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	sess, wasNew, ok := s.uploadSessions.tryClaim(filename, func() *ulSession { return &ulSession{} })
	if !ok {
		http.Error(w, "upload already in progress for this file", http.StatusConflict)
		return
	}

	if continuationKey == "" {
		s.handleUploadFirstChunk(w, transformer, transformName, file, filename, targetPath, sess, done)
		return
	}

	switch ulValidateKey(sess, continuationKey) {
	case ulKeyCompleted:
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		s.uploadSessions.release(filename, false)
		writeUploadResponse(w, http.StatusOK, filename, "")
	case ulKeyPrevious:
		nextKey := sess.currentKey
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		s.uploadSessions.release(filename, false)
		writeUploadResponse(w, http.StatusOK, filename, nextKey)
	case ulKeyCurrent:
		s.handleUploadContinuation(w, transformer, transformName, file, filename, targetPath, sess, done)
	default: // ulKeyUnrecognized
		// wasNew means nothing legitimate exists for this filename yet, so
		// there's nothing worth keeping; otherwise this stale/unknown key
		// must not disturb whatever session actually is on record.
		s.uploadSessions.release(filename, wasNew)
		http.Error(w, "unrecognized continuation-key", http.StatusBadRequest)
	}
}

// handleUploadFirstChunk handles a request with no continuation-key: it
// always behaves like today's one-shot upload (unconditionally overwriting
// whatever, if anything, was at targetPath — including any stale session
// recorded for filename, which sess may represent).
func (s *Server) handleUploadFirstChunk(w http.ResponseWriter, transformer transform.Transformer, transformName string, file multipart.File, filename, targetPath string, sess *ulSession, done bool) {
	f, err := os.Create(targetPath)
	if err != nil {
		log.Printf("error creating upload file %q: %v", targetPath, err)
		s.uploadSessions.release(filename, true)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	reader, err := transformer.Untransform(file)
	if err != nil {
		f.Close()
		os.Remove(targetPath)
		log.Printf("error applying transform %q to upload %q: %v", transformName, filename, err)
		s.uploadSessions.release(filename, true)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(f, reader); err != nil {
		f.Close()
		os.Remove(targetPath)
		log.Printf("error writing upload file %q: %v", targetPath, err)
		s.uploadSessions.release(filename, true)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	f.Close()

	if done {
		log.Printf("uploaded %q to %q", filename, targetPath)
		s.uploadSessions.release(filename, true)
		writeUploadResponse(w, http.StatusCreated, filename, "")
		return
	}

	key, err := ulNewKey()
	if err != nil {
		os.Remove(targetPath)
		s.uploadSessions.release(filename, true)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess.completed = false
	sess.currentKey = key
	sess.previousKey = ""
	sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
	s.uploadSessions.release(filename, false)
	writeUploadResponse(w, http.StatusCreated, filename, key)
}

// handleUploadContinuation handles a request whose continuation-key matched
// sess.currentKey — the expected next chunk of an already-started upload.
func (s *Server) handleUploadContinuation(w http.ResponseWriter, transformer transform.Transformer, transformName string, file multipart.File, filename, targetPath string, sess *ulSession, done bool) {
	f, err := os.OpenFile(targetPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("error opening upload file %q for continuation: %v", targetPath, err)
		os.Remove(targetPath)
		s.uploadSessions.release(filename, true)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	reader, err := transformer.Untransform(file)
	if err != nil {
		f.Close()
		os.Remove(targetPath)
		log.Printf("error applying transform %q to upload %q: %v", transformName, filename, err)
		s.uploadSessions.release(filename, true)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(f, reader); err != nil {
		f.Close()
		os.Remove(targetPath)
		log.Printf("error appending to upload file %q: %v", targetPath, err)
		s.uploadSessions.release(filename, true)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	f.Close()

	if done {
		log.Printf("uploaded %q to %q (chunked)", filename, targetPath)
		sess.completed = true
		sess.previousKey = ""
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		s.uploadSessions.release(filename, false)
		writeUploadResponse(w, http.StatusOK, filename, "")
		return
	}

	key, err := ulNewKey()
	if err != nil {
		os.Remove(targetPath)
		s.uploadSessions.release(filename, true)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess.previousKey = sess.currentKey
	sess.currentKey = key
	sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
	s.uploadSessions.release(filename, false)
	writeUploadResponse(w, http.StatusOK, filename, key)
}

func (s *Server) handleUlList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	all, err := listDir(s.absUlRoot)
	if err != nil {
		log.Printf("error reading upload directory: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	hidden := s.uploadSessions.lockedNames()
	files := make([]listEntry, 0, len(all))
	for _, e := range all {
		if e.Type != "f" {
			continue
		}
		if _, inProgress := hidden[e.Name]; inProgress {
			continue
		}
		files = append(files, e)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}
