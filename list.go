package main

import (
	"net/http"
	"os"
)

type listRequest struct {
	Path string `json:"path"`
}

type listEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  *int64 `json:"size,omitempty"`
	MTime *int64 `json:"mtime,omitempty"`
}

func listDir(root string) ([]listEntry, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	result := make([]listEntry, 0, len(entries))
	for _, e := range entries {
		t := "f"
		if e.IsDir() {
			t = "d"
		}
		entry := listEntry{Name: e.Name(), Type: t}
		if info, err := e.Info(); err == nil {
			mtime := info.ModTime().Unix()
			entry.MTime = &mtime
			if !e.IsDir() {
				size := info.Size()
				entry.Size = &size
			}
		}
		result = append(result, entry)
	}
	return result, nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req listRequest
	wasEncrypted, err := s.decodeParams(r.Body, &req)
	if err != nil {
		http.Error(w, "invalid request body", http.StatusUnauthorized)
		return
	}

	target, err := makePath(s.absRoot, req.Path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	entries, err := listDir(target)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "path not found", http.StatusNotFound)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	s.encodeParamsResponse(w, wasEncrypted, http.StatusOK, entries)
}
