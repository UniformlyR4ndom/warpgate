package main

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"
)

const clipTitleMaxLen = 200

// Note sources: "" means created in the UI. They are hints, not an
// authenticated property.
const (
	clipSourceOutbox = "outbox" // pushed from a local outbox folder
	clipSourceCLI    = "cli"    // created by wgcli
)

// validClipTitle accepts a single line of at most clipTitleMaxLen characters
// (note titles and image captions).
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
		Title  string `json:"title"`
		Source string `json:"source"`
		Data   string `json:"data"`
	}
	wasEncrypted, err := s.decodeParams(r.Body, &req)
	if err != nil {
		s.writeParamError(w, err)
		return
	}
	if !validClipTitle(req.Title) {
		http.Error(w, "invalid title", http.StatusBadRequest)
		return
	}
	if req.Source != "" && req.Source != clipSourceOutbox && req.Source != clipSourceCLI {
		http.Error(w, "invalid source", http.StatusBadRequest)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		http.Error(w, "data is not valid base64", http.StatusBadRequest)
		return
	}
	id, ts, err := s.db.InsertClipboard(req.Title, req.Source, raw)
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
		s.writeParamError(w, err)
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
		s.writeParamError(w, err)
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

// clipStateResponse is the incremental-sync feed; see CLAUDE.md's clipboard
// sync section. Ids is null when absent but [] when no notes are live, so a
// delete of the last note still propagates.
type clipStateResponse struct {
	Rev     int64      `json:"rev"`
	Changed []NoteMeta `json:"changed,omitempty"`
	Ids     []int64    `json:"ids"`
	Reset   bool       `json:"reset,omitempty"`
}

// defaultClipPollWait is how long a clip/state request with wait is held while
// nothing changes; kept under the idle timeouts common in inspecting proxies.
const defaultClipPollWait = 25 * time.Second

func (s *Server) handleClipState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "invalid since", http.StatusBadRequest)
			return
		}
		since = n
	}
	revChanged := s.db.RevChanged()
	rev, changed, ids, reset, err := s.db.ClipState(since)
	if err == nil && rev == since && r.URL.Query().Has("wait") {
		timer := time.NewTimer(s.cfg.clipPollWait)
		select {
		case <-revChanged:
		case <-timer.C:
		case <-r.Context().Done():
		case <-s.shuttingDown:
		}
		timer.Stop()
		rev, changed, ids, reset, err = s.db.ClipState(since)
	}
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	encrypted := !s.cfg.NoAuth && r.URL.Query().Has("e")
	s.encodeParamsResponse(w, encrypted, http.StatusOK,
		clipStateResponse{Rev: rev, Changed: changed, Ids: ids, Reset: reset})
}
