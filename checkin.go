package main

import (
	"log"
	"net/http"
)

func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if a := clientIP(r, s.trustedProxies); a.IsValid() {
		ip = a.String()
	}
	log.Printf("checkin from %s", ip)
	w.WriteHeader(http.StatusNoContent)
}
