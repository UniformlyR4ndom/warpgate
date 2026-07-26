package main

import (
	"io"
	"log"
	"net/http"
)

type simDownloadRequest struct {
	TotalSize int64 `json:"totalSize"`
	Start     int64 `json:"start"`
	Size      int64 `json:"size"`
}

// handleSimDownload serves a chunk of a synthesized file (see simReader) for
// probing whether a given proxy tolerates a transfer of a given size over
// plain HTTP, without touching the filesystem or any transform.
func (s *Server) handleSimDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req simDownloadRequest
	if _, err := s.decodeParams(r.Body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusUnauthorized)
		return
	}

	if err := validateSimTotalSize(req.TotalSize); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Start < 0 {
		http.Error(w, "start must not be negative", http.StatusBadRequest)
		return
	}
	if req.Size <= 0 {
		http.Error(w, "size must be positive", http.StatusBadRequest)
		return
	}

	reader := io.LimitReader(newSimReader(req.TotalSize, req.Start), req.Size)

	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("error writing sim download response: %v", err)
	}
}
