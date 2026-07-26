package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

type wsSimUploadRequest struct {
	Auth      string `json:"auth"`
	FrameSize int64  `json:"frameSize"`
}

// handleSimWSUpload receives a synthesized upload over a single WebSocket
// connection and returns its SHA-256 hash instead of writing it to disk.
// Unlike handleWSUpload there is no transform step requiring the full
// contents in one place, so frames are hashed incrementally as they arrive.
func (s *Server) handleSimWSUpload(w http.ResponseWriter, r *http.Request) {
	conn, err := s.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("sim ws upload: upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	req, encrypted, err := readWSControl[wsSimUploadRequest](s, conn)
	if err != nil {
		log.Printf("sim ws upload: reading control message: %v", err)
		return
	}

	if !s.checkWSAuth(req.Auth) {
		s.writeWSError(conn, encrypted, websocket.ClosePolicyViolation, "unauthorized")
		return
	}

	if _, err := s.resolveFrameSize(req.FrameSize); err != nil {
		s.writeWSError(conn, encrypted, websocket.ClosePolicyViolation, "%v", err)
		return
	}

	if err := s.writeWSOK(conn, encrypted, wsAck{OK: true}); err != nil {
		return
	}

	hasher := sha256.New()
	var total int64

	for {
		mt, p, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("sim ws upload: reading message: %v", err)
			}
			return
		}

		if mt == websocket.TextMessage {
			var done wsDoneMessage
			if _, err := s.decodeParams(bytes.NewReader(p), &done); err != nil || !done.Done {
				s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "unexpected control message")
				return
			}
			break
		}

		total += int64(len(p))
		if total > simMaxTotalSize {
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "upload exceeds %d bytes", simMaxTotalSize)
			return
		}
		hasher.Write(p)
	}

	if err := s.writeWSOK(conn, encrypted, wsAck{OK: true, Hash: hex.EncodeToString(hasher.Sum(nil))}); err != nil {
		return
	}
	writeWSClose(conn, websocket.CloseNormalClosure, "")
}
