package main

import (
	"encoding/json"
	"net/http"
)

type settingsResponse struct {
	WSFrameMin int `json:"wsFrameMin"`
	WSFrameMax int `json:"wsFrameMax"`
}

// handleSettings exposes server-configured limits the frontend needs to
// clamp its own UI inputs (currently just the WS frame size bounds), so a
// user can't pick a value the server would reject anyway.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settingsResponse{
		WSFrameMin: wsFrameSizeMin,
		WSFrameMax: s.cfg.WSFrameMax,
	})
}
