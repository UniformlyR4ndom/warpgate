package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
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
	// SHA256 is the stored file's hash, set once the upload completed.
	SHA256 string `json:"sha256,omitempty"`
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
	sha256  string // of the published file, for replays of the final chunk
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
	if err := s.ulRoot.RemoveAll(ulTmpDirName); err != nil {
		return fmt.Errorf("clearing upload temp dir: %w", err)
	}
	if err := s.ulRoot.Mkdir(ulTmpDirName, 0o700); err != nil {
		return fmt.Errorf("creating upload temp dir: %w", err)
	}
	info, err := s.ulRoot.Stat(ulTmpDirName)
	if err != nil {
		return fmt.Errorf("creating upload temp dir: %w", err)
	}
	s.ulTmpInfo = info
	return nil
}

const errUlTmpPath = "path refers to the upload temp directory"

// isUlTmpDir reports whether rel inside root is the upload temp directory,
// by identity, so other spellings (case, short names, symlinks) are caught.
func (s *Server) isUlTmpDir(root *os.Root, rel string) bool {
	if s.ulTmpInfo == nil {
		return false
	}
	fi, err := root.Stat(rel)
	return err == nil && os.SameFile(fi, s.ulTmpInfo)
}

// reachesUlTmp reports whether rel inside root is or passes through the
// upload temp directory, which must never be listed, read or written.
func (s *Server) reachesUlTmp(root *os.Root, rel string) bool {
	if s.ulTmpInfo == nil || rel == "." {
		return false
	}
	prefix := ""
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		prefix = filepath.Join(prefix, seg)
		fi, err := root.Stat(prefix)
		if err != nil {
			return false
		}
		if os.SameFile(fi, s.ulTmpInfo) {
			return true
		}
	}
	return false
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

// ulBaseName validates a client-supplied file name, keeping only its base
// name. Both separators are honored whatever the server's platform, and a
// Windows drive prefix is not stripped but rejected by validName.
func ulBaseName(clientName string) (string, error) {
	filename := path.Base(strings.ReplaceAll(clientName, "\\", "/"))
	if !validName(filename) {
		return "", fmt.Errorf("invalid filename")
	}
	return filename, nil
}

// rawPartFilename returns the file name as the client sent it. FileHeader's
// Filename has already been through filepath.Base, which on Windows silently
// drops a "c:"-style prefix that ulBaseName should reject instead.
func rawPartFilename(h *multipart.FileHeader) string {
	if _, params, err := mime.ParseMediaType(h.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		return params["filename"]
	}
	return h.Filename
}

// ulTarget resolves an upload's target, relative to the upload root, from
// the client's directory (an existing directory below the upload root, ""
// for the root itself) and file name. On failure it also returns the HTTP
// status to report.
func (s *Server) ulTarget(dir, clientName string) (string, int, error) {
	name, err := ulBaseName(clientName)
	if err != nil {
		return "", http.StatusBadRequest, err
	}
	if s.cfg.UlFlat && dir != "" {
		return "", http.StatusBadRequest, errors.New("subdirectories are disabled (-ul-flat)")
	}
	rel, err := rootRelPath(dir)
	if err != nil {
		return "", http.StatusBadRequest, err
	}
	if s.reachesUlTmp(s.ulRoot, rel) {
		return "", http.StatusForbidden, errors.New(errUlTmpPath)
	}
	fi, err := s.ulRoot.Stat(rel)
	switch {
	case isPathEscape(err):
		return "", http.StatusForbidden, errors.New(errSymlinkEscape)
	case err != nil || !fi.IsDir():
		return "", http.StatusNotFound, errors.New("directory not found")
	}
	target := filepath.Join(rel, name)
	if fi, err := s.ulRoot.Lstat(target); err == nil && fi.IsDir() {
		return "", http.StatusConflict, errors.New("a directory with this name exists")
	}
	return target, 0, nil
}

// publishUpload replaces filename with the finished temp file and returns
// its SHA-256. Renaming replaces a symlink at filename rather than writing
// through it.
func (s *Server) publishUpload(tmpPath, filename string) (string, error) {
	sum, err := hashFile(s.ulRoot, tmpPath)
	if err != nil {
		return "", err
	}
	if err := s.ulRoot.Rename(tmpPath, filename); err != nil {
		return "", err
	}
	log.Printf("uploaded %q to %q", filename, filepath.Join(s.absUlRoot, filename))
	return sum, nil
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

func (s *Server) writeUploadResponse(w http.ResponseWriter, encrypted bool, status int, filename, continuationKey, sha256 string) {
	s.encodeParamsResponse(w, encrypted, status, uploadResponse{Name: filepath.ToSlash(filename), ContinuationKey: continuationKey, SHA256: sha256})
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

	if !parseUploadForm(w, r, 0) {
		return
	}

	// Read optional transform spec (plus chunking fields) from the
	// "transform" file part.
	transformName := "id"
	var transformArgs json.RawMessage
	var dir string
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
			Dir             string          `json:"dir"`
			ContinuationKey string          `json:"continuation-key"`
			Done            *bool           `json:"ul-done"`
		}
		wasEncrypted, err = s.decodeParams(tf, &spec)
		if err != nil {
			s.writeParamError(w, err)
			return
		}
		transformName = spec.Transform
		transformArgs = spec.Args
		dir = spec.Dir
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

	filename, status, err := s.ulTarget(dir, rawPartFilename(header))
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	ack, ok := withClaim(s.uploadSessions, filename, newUlSession, func(sess *ulSession, wasNew bool) (ulAck, bool) {
		if continuationKey == "" {
			return s.uploadFirstChunk(transformer, file, filename, sess, done)
		}
		switch ulValidateKey(sess, continuationKey) {
		case ulKeyCompleted:
			sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
			return ulAck{status: http.StatusOK, sha256: sess.sha256}, false
		case ulKeyPrevious:
			sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
			return ulAck{status: http.StatusOK, key: sess.currentKey}, false
		case ulKeyCurrent:
			return s.uploadContinuation(transformer, file, filename, sess, done)
		default: // ulKeyUnrecognized
			// wasNew means nothing legitimate exists for this filename yet, so
			// there's nothing worth keeping; otherwise this stale/unknown key
			// must not disturb whatever session actually is on record.
			return ulAck{status: http.StatusBadRequest, errMsg: "unrecognized continuation-key"}, wasNew
		}
	})
	if !ok {
		http.Error(w, "upload already in progress for this file", http.StatusConflict)
		return
	}
	if ack.errMsg != "" {
		http.Error(w, ack.errMsg, ack.status)
		return
	}
	s.writeUploadResponse(w, wasEncrypted, ack.status, filename, ack.key, ack.sha256)
}

func newUlSession() *ulSession { return &ulSession{} }

// ulAck is the response to one upload request, built while the session claim
// is held.
type ulAck struct {
	status int
	key    string
	sha256 string
	errMsg string // set: plain-text error response
}

// uploadFailed tears down the whole session for filename after a processing
// failure: there is no partial-failure recovery.
func (s *Server) uploadFailed(filename string, sess *ulSession, err error) (ulAck, bool) {
	log.Printf("upload %q failed: %v", filename, err)
	s.removeUlTemp(sess.tmpPath)
	return ulAck{status: http.StatusInternalServerError, errMsg: "internal error"}, true
}

// uploadFirstChunk handles a request with no continuation-key: it starts
// over, discarding any session recorded for filename. The file is replaced
// only once the upload completes.
func (s *Server) uploadFirstChunk(transformer transform.Transformer, file multipart.File, filename string, sess *ulSession, done bool) (ulAck, bool) {
	s.removeUlTemp(sess.tmpPath)
	tmpPath, err := s.createUlTemp()
	if err != nil {
		sess.tmpPath = ""
		return s.uploadFailed(filename, sess, err)
	}
	sess.tmpPath = tmpPath

	if err := appendUploadChunk(s.ulRoot, tmpPath, transformer, file); err != nil {
		return s.uploadFailed(filename, sess, err)
	}

	if done {
		sum, err := s.publishUpload(tmpPath, filename)
		if err != nil {
			return s.uploadFailed(filename, sess, err)
		}
		return ulAck{status: http.StatusCreated, sha256: sum}, true
	}

	key, err := ulNewKey()
	if err != nil {
		return s.uploadFailed(filename, sess, err)
	}
	sess.completed = false
	sess.currentKey = key
	sess.previousKey = ""
	sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
	return ulAck{status: http.StatusCreated, key: key}, false
}

// uploadContinuation handles a request whose continuation-key matched
// sess.currentKey — the expected next chunk of an already-started upload.
func (s *Server) uploadContinuation(transformer transform.Transformer, file multipart.File, filename string, sess *ulSession, done bool) (ulAck, bool) {
	if err := appendUploadChunk(s.ulRoot, sess.tmpPath, transformer, file); err != nil {
		return s.uploadFailed(filename, sess, err)
	}

	if done {
		sum, err := s.publishUpload(sess.tmpPath, filename)
		if err != nil {
			return s.uploadFailed(filename, sess, err)
		}
		sess.tmpPath = ""
		sess.sha256 = sum
		sess.completed = true
		sess.previousKey = ""
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		return ulAck{status: http.StatusOK, sha256: sum}, false
	}

	key, err := ulNewKey()
	if err != nil {
		return s.uploadFailed(filename, sess, err)
	}
	sess.previousKey = sess.currentKey
	sess.currentKey = key
	sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
	return ulAck{status: http.StatusOK, key: key}, false
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
