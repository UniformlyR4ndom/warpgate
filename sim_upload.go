package main

import (
	"crypto/sha256"
	"encoding/hex"
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

func (s *Server) writeSimUploadResponse(w http.ResponseWriter, encrypted bool, hashHex, continuationKey string) {
	s.encodeParamsResponse(w, encrypted, http.StatusOK, simUploadResponse{Hash: hashHex, ContinuationKey: continuationKey})
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

	if !parseUploadForm(w, r, simMaxTotalSize+multipartOverheadSlack) {
		return
	}

	var continuationKey string
	// done defaults to true (one-shot) when ul-done is absent entirely, not
	// just falsy — see the matching comment in handleUpload.
	done := true
	// wasEncrypted stays false when there's no "transform" part at all: no
	// signal either way means a plaintext response.
	wasEncrypted := false
	if tf, _, err := r.FormFile("transform"); err == nil {
		defer tf.Close()
		var spec struct {
			ContinuationKey string `json:"continuation-key"`
			Done            *bool  `json:"ul-done"`
		}
		wasEncrypted, err = s.decodeParams(tf, &spec)
		if err != nil {
			s.writeParamError(w, err)
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

	ack, ok := withClaim(s.simUploadSessions, name, func() *simUlSession {
		return &simUlSession{hasher: sha256.New()}
	}, func(sess *simUlSession, wasNew bool) (simUlAck, bool) {
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
			return simUploadChunk(file, sess, done, true)
		}
		switch ulValidateKey(sess, continuationKey) {
		case ulKeyCompleted:
			sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
			return simUlAck{hash: hex.EncodeToString(sess.hasher.Sum(nil))}, false
		case ulKeyPrevious:
			sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
			return simUlAck{key: sess.currentKey}, false
		case ulKeyCurrent:
			return simUploadChunk(file, sess, done, false)
		default: // ulKeyUnrecognized
			return simUlAck{status: http.StatusBadRequest, errMsg: "unrecognized continuation-key"}, wasNew
		}
	})
	if !ok {
		http.Error(w, "upload already in progress for this session", http.StatusConflict)
		return
	}
	if ack.errMsg != "" {
		http.Error(w, ack.errMsg, ack.status)
		return
	}
	s.writeSimUploadResponse(w, wasEncrypted, ack.hash, ack.key)
}

// simUlAck is the response to one sim upload request, built while the session
// claim is held. Every success is a 200.
type simUlAck struct {
	hash   string
	key    string
	status int    // of an error
	errMsg string // set: plain-text error response
}

// simUploadChunk hashes one chunk's bytes into sess's running hash and
// either finalizes (done) or rotates the continuation-key for the next
// chunk. isFirst is true only for a request with no continuation-key at
// all, used solely to decide whether a done response discards the session
// (a genuine one-shot upload) or keeps a completed marker around for
// idempotent replay (a real chunked upload's final chunk).
func simUploadChunk(file multipart.File, sess *simUlSession, done, isFirst bool) (simUlAck, bool) {
	n, err := io.Copy(sess.hasher, io.LimitReader(file, simMaxTotalSize-sess.totalBytes+1))
	if err != nil {
		return simUlAck{status: http.StatusInternalServerError, errMsg: "read error"}, true
	}
	sess.totalBytes += n
	if sess.totalBytes > simMaxTotalSize {
		return simUlAck{status: http.StatusBadRequest, errMsg: fmt.Sprintf("upload exceeds %d bytes", simMaxTotalSize)}, true
	}

	if done {
		hashHex := hex.EncodeToString(sess.hasher.Sum(nil))
		if isFirst {
			return simUlAck{hash: hashHex}, true
		}
		sess.completed = true
		sess.previousKey = ""
		sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
		return simUlAck{hash: hashHex}, false
	}

	key, err := ulNewKey()
	if err != nil {
		return simUlAck{status: http.StatusInternalServerError, errMsg: "internal error"}, true
	}
	sess.previousKey = sess.currentKey
	sess.currentKey = key
	sess.expiresAt = time.Now().Add(ulSessionIdleExpiry)
	return simUlAck{key: key}, false
}
