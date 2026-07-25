package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// multipartOverheadSlack is headroom added on top of simMaxTotalSize for the
// whole-request byte cap, to account for multipart/form-data framing
// (boundary markers, part headers) that isn't part of the file content
// itself. Without it, a file at exactly simMaxTotalSize — the UI's own max,
// so exactly what a user stress-testing the benchmark would pick — always
// overflows the cap once framing is added in, and gets rejected as if it
// were too large. The file content itself is still hard-capped at
// simMaxTotalSize via the io.LimitReader below, regardless of this slack.
// This applies to a single request's body; a chunked run enforces the same
// cap across chunks via simUlSession.totalBytes instead.
const multipartOverheadSlack = 64 * 1024

type simUploadResponse struct {
	// Hash is set only on the response that completes an upload (a plain
	// one-shot request, or a chunked run's final chunk / completed-session
	// replay) — an intermediate chunk's running hash isn't a meaningful
	// "the file's hash" value, unlike ContinuationKey.
	Hash            string `json:"hash,omitempty"`
	ContinuationKey string `json:"continuation-key,omitempty"`
}

// simUlSession is a chunked sim-upload session's payload. Unlike a real
// upload, there's no file to reopen between requests, so the running hash
// itself must be the thing kept alive across chunks.
type simUlSession struct {
	ulSessionCore
	hasher     hash.Hash
	totalBytes int64
}

func writeSimUploadResponse(w http.ResponseWriter, hashHex, continuationKey string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(simUploadResponse{Hash: hashHex, ContinuationKey: continuationKey})
}

// handleSimUpload serves both a plain one-shot benchmark upload (no
// continuation-key, ul-done true or omitted if the "transform" part is
// absent entirely — see below) and each chunk of a multi-request chunked
// benchmark upload, mirroring handleUpload's protocol but hashing bytes
// instead of writing them anywhere. See "Benchmark/Simulation Endpoints" in
// CLAUDE.md for the full protocol.
func (s *Server) handleSimUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetReadDeadline(time.Now().Add(ulChunkProcessTimeout))
	}

	r.Body = http.MaxBytesReader(w, r.Body, simMaxTotalSize+multipartOverheadSlack)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid multipart form or file too large", http.StatusBadRequest)
		return
	}

	var continuationKey string
	// done defaults to true (one-shot) when ul-done is absent entirely, not
	// just falsy — see the matching comment in handleUpload.
	done := true
	if tf, _, err := r.FormFile("transform"); err == nil {
		defer tf.Close()
		var spec struct {
			ContinuationKey string `json:"continuation-key"`
			Done            *bool  `json:"ul-done"`
		}
		if err := json.NewDecoder(tf).Decode(&spec); err != nil {
			http.Error(w, "invalid transform spec", http.StatusBadRequest)
			return
		}
		continuationKey = spec.ContinuationKey
		if spec.Done != nil {
			done = *spec.Done
		}
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	name := header.Filename
	if name == "" {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	sess, wasNew, ok := s.simUploadSessions.tryClaim(name, func() *simUlSession {
		return &simUlSession{hasher: sha256.New()}
	})
	if !ok {
		http.Error(w, "upload already in progress for this session", http.StatusConflict)
		return
	}

	if continuationKey == "" {
		// First chunk always starts a fresh hash, discarding anything
		// previously recorded for this session name — mirrors how a first
		// real-upload chunk unconditionally overwrites its target file.
		// completed/previousKey must reset too, or a stale completed=true
		// from a prior run on this same name makes ulValidateKey
		// misclassify this run's real current-key chunk as a completed-
		// session replay once currentKey happens to coincide.
		sess.hasher = sha256.New()
		sess.totalBytes = 0
		sess.completed = false
		sess.previousKey = ""
		s.handleSimUploadChunk(w, file, name, sess, done, true)
		return
	}

	switch ulValidateKey(sess, continuationKey) {
	case ulKeyCompleted:
		hashHex := hex.EncodeToString(sess.hasher.Sum(nil))
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		s.simUploadSessions.release(name, false)
		writeSimUploadResponse(w, hashHex, "")
	case ulKeyPrevious:
		nextKey := sess.currentKey
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		s.simUploadSessions.release(name, false)
		writeSimUploadResponse(w, "", nextKey)
	case ulKeyCurrent:
		s.handleSimUploadChunk(w, file, name, sess, done, false)
	default: // ulKeyUnrecognized
		s.simUploadSessions.release(name, wasNew)
		http.Error(w, "unrecognized continuation-key", http.StatusBadRequest)
	}
}

// handleSimUploadChunk hashes one chunk's bytes into sess's running hash and
// either finalizes (done) or rotates the continuation-key for the next
// chunk. isFirst is true only for a request with no continuation-key at
// all, used solely to decide whether a done response discards the session
// (a genuine one-shot upload, matching today's behavior exactly) or keeps a
// completed marker around for idempotent replay (a real chunked upload's
// final chunk).
func (s *Server) handleSimUploadChunk(w http.ResponseWriter, file multipart.File, name string, sess *simUlSession, done, isFirst bool) {
	n, err := io.Copy(sess.hasher, io.LimitReader(file, simMaxTotalSize-sess.totalBytes+1))
	if err != nil {
		s.simUploadSessions.release(name, true)
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	sess.totalBytes += n
	if sess.totalBytes > simMaxTotalSize {
		s.simUploadSessions.release(name, true)
		http.Error(w, fmt.Sprintf("upload exceeds %d bytes", simMaxTotalSize), http.StatusBadRequest)
		return
	}

	if done {
		hashHex := hex.EncodeToString(sess.hasher.Sum(nil))
		if isFirst {
			s.simUploadSessions.release(name, true)
			writeSimUploadResponse(w, hashHex, "")
			return
		}
		sess.completed = true
		sess.previousKey = ""
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		s.simUploadSessions.release(name, false)
		writeSimUploadResponse(w, hashHex, "")
		return
	}

	key, err := ulNewKey()
	if err != nil {
		s.simUploadSessions.release(name, true)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess.previousKey = sess.currentKey
	sess.currentKey = key
	sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
	s.simUploadSessions.release(name, false)
	writeSimUploadResponse(w, "", key)
}
