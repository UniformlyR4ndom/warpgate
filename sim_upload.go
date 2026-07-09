package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
)

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

	r.Body = http.MaxBytesReader(w, r.Body, simMaxTotalSize)

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
	if _, err := io.Copy(hasher, file); err != nil {
		http.Error(w, "file too large or read error", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(simUploadResponse{Hash: hex.EncodeToString(hasher.Sum(nil))})
}
