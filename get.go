package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
)

type getRequest struct {
	Path      string          `json:"path"`
	Start     int64           `json:"start"`
	Size      int64           `json:"size"`
	Transform string          `json:"transform"`
	Args      json.RawMessage `json:"args"`
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req getRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if req.Size <= 0 {
		http.Error(w, "size must be positive", http.StatusBadRequest)
		return
	}

	target, err := makePath(s.absRoot, req.Path)
	if err != nil {
		log.Printf("invalid path %q requested", req.Path)
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	transformer, err := newTransformer(req.Transform, req.Args)
	if err != nil {
		http.Error(w, "invalid transform: "+err.Error(), http.StatusBadRequest)
		return
	}

	f, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
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

	ct := "application/octet-stream"
	type contentTyper interface{ ContentType() string }
	if ctr, ok := transformer.(contentTyper); ok {
		ct = ctr.ContentType()
	}
	w.Header().Set("Content-Type", ct)
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("error writing response for %q: %v", req.Path, err)
	}
}
