package main

import "net/http"

// handleAuthCheck runs behind withAuth, so reaching it means the token is valid.
func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
