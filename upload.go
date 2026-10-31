package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
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

// ulTmpDirName is the subdirectory of -ul-root holding uploads until they
// complete. Uploads always land flat in -ul-root, so this one name is the
// only one a temp file could ever collide with.
const ulTmpDirName = ".warpgate-tmp"

// ulSession is a chunked real-file upload's session payload: the temp file
// (relative to the upload root) the chunks are appended to until the final
// chunk publishes it.
type ulSession struct {
	ulSessionCore
	tmpPath string
}

// abortUploadSession deletes the temp file of an expired or failed,
// incomplete chunked upload.
func (s *Server) abortUploadSession(_ string, sess *ulSession) {
	s.removeUlTemp(sess.tmpPath)
}

func (s *Server) removeUlTemp(path string) {
	if path == "" {
		return
	}
	if err := s.ulRoot.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("removing upload temp file %q: %v", path, err)
	}
}

// initUlTmpDir recreates the empty temp directory, discarding leftovers of
// uploads interrupted by a restart.
func (s *Server) initUlTmpDir() error {
	s.ulTmpDir = filepath.Join(s.absUlRoot, ulTmpDirName)
	if err := s.ulRoot.RemoveAll(ulTmpDirName); err != nil {
		return fmt.Errorf("clearing upload temp dir: %w", err)
	}
	if err := s.ulRoot.Mkdir(ulTmpDirName, 0o700); err != nil {
		return fmt.Errorf("creating upload temp dir: %w", err)
	}
	return nil
}

// isUlTmpPath reports whether the absolute path is inside the upload temp
// directory, which must never be listed or downloaded.
func (s *Server) isUlTmpPath(path string) bool {
	return s.ulTmpDir != "" && pathIsUnder(path, s.ulTmpDir)
}

// createUlTemp creates an empty temp file and returns its path relative to
// the upload root. It honors the umask, so the published file gets the same
// mode as os.Create gives.
func (s *Server) createUlTemp() (string, error) {
	for {
		path := filepath.Join(ulTmpDirName, "ul-"+rand.Text())
		f, err := s.ulRoot.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		return path, f.Close()
	}
}

// ulTarget derives the stored filename, relative to the upload root, from a
// client-supplied name, keeping only the base name.
func ulTarget(clientName string) (string, error) {
	// Normalize separators so Windows client paths are handled correctly.
	filename := filepath.Base(strings.ReplaceAll(clientName, "\\", "/"))
	if filename == "" || filename == "." || filename == ".." || filename == "/" || filename == ulTmpDirName {
		return "", fmt.Errorf("invalid filename")
	}
	return filename, nil
}

// publishUpload replaces filename with the finished temp file. Renaming
// replaces a symlink at filename rather than writing through it.
func (s *Server) publishUpload(tmpPath, filename string) error {
	if err := s.ulRoot.Rename(tmpPath, filename); err != nil {
		return err
	}
	log.Printf("uploaded %q to %q", filename, filepath.Join(s.absUlRoot, filename))
	return nil
}

// appendUploadChunk untransforms one chunk and appends it to path inside root.
func appendUploadChunk(root *os.Root, path string, transformer transform.Transformer, src io.Reader) error {
	f, err := root.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	reader, err := transformer.Untransform(src)
	if err != nil {
		f.Close()
		return fmt.Errorf("applying transform: %w", err)
	}
	if _, err := io.Copy(f, reader); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *Server) writeUploadResponse(w http.ResponseWriter, encrypted bool, status int, filename, continuationKey string) {
	s.encodeParamsResponse(w, encrypted, status, uploadResponse{Name: filename, ContinuationKey: continuationKey})
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
	// wasEncrypted stays false when there's no "transform" part at all (a
	// legacy client): no signal either way means a plaintext response.
	wasEncrypted := false
	if tf, _, err := r.FormFile("transform"); err == nil {
		defer tf.Close()
		var spec struct {
			Transform       string          `json:"transform"`
			Args            json.RawMessage `json:"args"`
			ContinuationKey string          `json:"continuation-key"`
			Done            *bool           `json:"ul-done"`
		}
		wasEncrypted, err = s.decodeParams(tf, &spec)
		if err != nil {
			http.Error(w, "invalid transform spec", http.StatusUnauthorized)
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

	filename, err := ulTarget(header.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess, wasNew, ok := s.uploadSessions.tryClaim(filename, func() *ulSession { return &ulSession{} })
	if !ok {
		http.Error(w, "upload already in progress for this file", http.StatusConflict)
		return
	}

	if continuationKey == "" {
		s.handleUploadFirstChunk(w, transformer, file, filename, sess, done, wasEncrypted)
		return
	}

	switch ulValidateKey(sess, continuationKey) {
	case ulKeyCompleted:
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		s.uploadSessions.release(filename, false)
		s.writeUploadResponse(w, wasEncrypted, http.StatusOK, filename, "")
	case ulKeyPrevious:
		nextKey := sess.currentKey
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		s.uploadSessions.release(filename, false)
		s.writeUploadResponse(w, wasEncrypted, http.StatusOK, filename, nextKey)
	case ulKeyCurrent:
		s.handleUploadContinuation(w, transformer, file, filename, sess, done, wasEncrypted)
	default: // ulKeyUnrecognized
		// wasNew means nothing legitimate exists for this filename yet, so
		// there's nothing worth keeping; otherwise this stale/unknown key
		// must not disturb whatever session actually is on record.
		s.uploadSessions.release(filename, wasNew)
		http.Error(w, "unrecognized continuation-key", http.StatusBadRequest)
	}
}

// failUpload tears down the whole session for filename after a processing
// failure: there is no partial-failure recovery.
func (s *Server) failUpload(w http.ResponseWriter, filename string, sess *ulSession, err error) {
	log.Printf("upload %q failed: %v", filename, err)
	s.removeUlTemp(sess.tmpPath)
	s.uploadSessions.release(filename, true)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// handleUploadFirstChunk handles a request with no continuation-key: it
// starts over, discarding any session recorded for filename. The file is
// replaced only once the upload completes.
func (s *Server) handleUploadFirstChunk(w http.ResponseWriter, transformer transform.Transformer, file multipart.File, filename string, sess *ulSession, done, encrypted bool) {
	s.removeUlTemp(sess.tmpPath)
	tmpPath, err := s.createUlTemp()
	if err != nil {
		sess.tmpPath = ""
		s.failUpload(w, filename, sess, err)
		return
	}
	sess.tmpPath = tmpPath

	if err := appendUploadChunk(s.ulRoot, tmpPath, transformer, file); err != nil {
		s.failUpload(w, filename, sess, err)
		return
	}

	if done {
		if err := s.publishUpload(tmpPath, filename); err != nil {
			s.failUpload(w, filename, sess, err)
			return
		}
		s.uploadSessions.release(filename, true)
		s.writeUploadResponse(w, encrypted, http.StatusCreated, filename, "")
		return
	}

	key, err := ulNewKey()
	if err != nil {
		s.failUpload(w, filename, sess, err)
		return
	}
	sess.completed = false
	sess.currentKey = key
	sess.previousKey = ""
	sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
	s.uploadSessions.release(filename, false)
	s.writeUploadResponse(w, encrypted, http.StatusCreated, filename, key)
}

// handleUploadContinuation handles a request whose continuation-key matched
// sess.currentKey — the expected next chunk of an already-started upload.
func (s *Server) handleUploadContinuation(w http.ResponseWriter, transformer transform.Transformer, file multipart.File, filename string, sess *ulSession, done, encrypted bool) {
	if err := appendUploadChunk(s.ulRoot, sess.tmpPath, transformer, file); err != nil {
		s.failUpload(w, filename, sess, err)
		return
	}

	if done {
		if err := s.publishUpload(sess.tmpPath, filename); err != nil {
			s.failUpload(w, filename, sess, err)
			return
		}
		sess.tmpPath = ""
		sess.completed = true
		sess.previousKey = ""
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		s.uploadSessions.release(filename, false)
		s.writeUploadResponse(w, encrypted, http.StatusOK, filename, "")
		return
	}

	key, err := ulNewKey()
	if err != nil {
		s.failUpload(w, filename, sess, err)
		return
	}
	sess.previousKey = sess.currentKey
	sess.currentKey = key
	sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
	s.uploadSessions.release(filename, false)
	s.writeUploadResponse(w, encrypted, http.StatusOK, filename, key)
}

func (s *Server) handleUlList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	all, err := listDir(s.ulRoot, ".")
	if err != nil {
		log.Printf("error reading upload directory: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	files := make([]listEntry, 0, len(all))
	for _, e := range all {
		if e.Type == "f" {
			files = append(files, e)
		}
	}

	encrypted := !s.cfg.NoAuth && r.URL.Query().Has("e")
	s.encodeParamsResponse(w, encrypted, http.StatusOK, files)
}
