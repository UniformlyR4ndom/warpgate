package main

import (
	"log"
	"net"
	"net/http"
)

func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	log.Printf("checkin from %s", ip)
	w.WriteHeader(http.StatusNoContent)
}
