package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// multipartOverheadSlack is headroom added on top of simMaxTotalSize for the
// whole-request byte cap, to account for multipart/form-data framing
// (boundary markers, part headers) that isn't part of the file content
// itself. Without it, a file at exactly simMaxTotalSize — the UI's own max,
// so exactly what a user stress-testing the benchmark would pick — always
// overflows the cap once framing is added in, and gets rejected as if it
// were too large. The file content itself is still hard-capped at
// simMaxTotalSize via the io.LimitReader below, regardless of this slack.
const multipartOverheadSlack = 64 * 1024

type simUploadResponse struct {
	Hash string `json:"hash"`
}

// handleSimUpload accepts a file the same way handleUpload does, but never
// writes it to disk: it streams the received bytes straight into a SHA-256
// hash and returns that, so a client can verify a benchmark upload arrived
// intact without the server needing storage for it.
func (s *Server) handleSimUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, simMaxTotalSize+multipartOverheadSlack)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid multipart form or file too large", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	hasher := sha256.New()
	n, err := io.Copy(hasher, io.LimitReader(file, simMaxTotalSize+1))
	if err != nil {
		http.Error(w, "file too large or read error", http.StatusBadRequest)
		return
	}
	if n > simMaxTotalSize {
		http.Error(w, fmt.Sprintf("file exceeds %d bytes", simMaxTotalSize), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(simUploadResponse{Hash: hex.EncodeToString(hasher.Sum(nil))})
}
