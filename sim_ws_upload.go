package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	req, err := readWSControl[wsSimUploadRequest](conn)
	if err != nil {
		log.Printf("sim ws upload: reading control message: %v", err)
		return
	}

	if !s.checkWSAuth(req.Auth) {
		writeWSError(conn, websocket.ClosePolicyViolation, "unauthorized")
		return
	}

	if _, err := s.resolveFrameSize(req.FrameSize); err != nil {
		writeWSError(conn, websocket.ClosePolicyViolation, "%v", err)
		return
	}

	if err := writeWSOK(conn, wsAck{OK: true}); err != nil {
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
			if err := json.Unmarshal(p, &done); err != nil || !done.Done {
				writeWSError(conn, websocket.CloseInternalServerErr, "unexpected control message")
				return
			}
			break
		}

		total += int64(len(p))
		if total > simMaxTotalSize {
			writeWSError(conn, websocket.CloseInternalServerErr, "upload exceeds %d bytes", simMaxTotalSize)
			return
		}
		hasher.Write(p)
	}

	if err := writeWSOK(conn, wsAck{OK: true, Hash: hex.EncodeToString(hasher.Sum(nil))}); err != nil {
		return
	}
	writeWSClose(conn, websocket.CloseNormalClosure, "")
}
