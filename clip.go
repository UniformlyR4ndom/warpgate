package main

import (
	"encoding/base64"
	"net/http"
	"unicode"
	"unicode/utf8"
)

const clipTitleMaxLen = 200

// validClipTitle accepts a single line of at most clipTitleMaxLen characters.
func validClipTitle(t string) bool {
	if !utf8.ValidString(t) || utf8.RuneCountInString(t) > clipTitleMaxLen {
		return false
	}
	for _, r := range t {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Server) handleClipNew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Title string `json:"title"`
		Data  string `json:"data"`
	}
	wasEncrypted, err := s.decodeParams(r.Body, &req)
	if err != nil {
		http.Error(w, "invalid request body", http.StatusUnauthorized)
		return
	}
	if !validClipTitle(req.Title) {
		http.Error(w, "invalid title", http.StatusBadRequest)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		http.Error(w, "data is not valid base64", http.StatusBadRequest)
		return
	}
	id, ts, err := s.db.InsertClipboard(req.Title, raw)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	s.encodeParamsResponse(w, wasEncrypted, http.StatusOK, struct {
		ID int64 `json:"id"`
		TS int64 `json:"ts"`
	}{id, ts})
}

func (s *Server) handleClipEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID    int64   `json:"id"`
		Title *string `json:"title"`
		Data  string  `json:"data"`
	}
	if _, err := s.decodeParams(r.Body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusUnauthorized)
		return
	}
	if req.Title != nil && !validClipTitle(*req.Title) {
		http.Error(w, "invalid title", http.StatusBadRequest)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		http.Error(w, "data is not valid base64", http.StatusBadRequest)
		return
	}
	found, err := s.db.UpdateClipboard(req.ID, req.Title, raw)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClipDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if _, err := s.decodeParams(r.Body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusUnauthorized)
		return
	}
	found, err := s.db.DeleteClipboard(req.ID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClipList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := s.db.ListClipboard()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []ClipItem{}
	}
	encrypted := !s.cfg.NoAuth && r.URL.Query().Has("e")
	s.encodeParamsResponse(w, encrypted, http.StatusOK, items)
}
