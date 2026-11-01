package main

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
)

type settingsResponse struct {
	WSFrameMin int    `json:"wsFrameMin"`
	WSFrameMax int    `json:"wsFrameMax"`
	UlFlat     bool   `json:"ulFlat"`
	KDFSalt    string `json:"kdfSalt"`
}

// handleSettings exposes server-configured limits the frontend needs to
// clamp its own UI inputs, so a user can't pick a value the server would
// reject anyway.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// A failed write means the client is gone; there is nothing to report.
	_ = json.NewEncoder(w).Encode(settingsResponse{
		WSFrameMin: wsFrameSizeMin,
		WSFrameMax: s.cfg.WSFrameMax,
		UlFlat:     s.cfg.UlFlat,
		KDFSalt:    hex.EncodeToString(s.kdfSalt),
	})
}
