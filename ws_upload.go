package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorilla/websocket"

	"warpgate/internal/transform"
)

type wsUploadRequest struct {
	Auth      string          `json:"auth"`
	Filename  string          `json:"filename"`
	Transform string          `json:"transform"`
	Args      json.RawMessage `json:"args"`
	FrameSize int64           `json:"frameSize"`
}

type wsDoneMessage struct {
	Done bool `json:"done"`
}

// handleWSUpload receives one file over a single WebSocket connection, as a
// sequence of binary frame messages (each up to the negotiated frameSize)
// followed by a {"done":true} text message. All frames are reassembled into
// one buffer before a single Untransform call — see the comment above
// `received` below for why.
func (s *Server) handleWSUpload(w http.ResponseWriter, r *http.Request) {
	conn, err := s.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upload: upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	req, err := readWSControl[wsUploadRequest](conn)
	if err != nil {
		log.Printf("ws upload: reading control message: %v", err)
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

	// Normalize separators so Windows client paths are handled correctly.
	rawName := strings.ReplaceAll(req.Filename, "\\", "/")
	filename := filepath.Base(rawName)
	if filename == "" || filename == "." || filename == "/" {
		writeWSError(conn, websocket.CloseInternalServerErr, "invalid filename")
		return
	}

	transformer, err := transform.New(req.Transform, req.Args)
	if err != nil {
		writeWSError(conn, websocket.CloseInternalServerErr, "invalid transform: %v", err)
		return
	}

	targetPath, err := makePath(s.absUlRoot, filename)
	if err != nil {
		writeWSError(conn, websocket.CloseInternalServerErr, "invalid filename")
		return
	}

	f, err := os.Create(targetPath)
	if err != nil {
		log.Printf("ws upload: creating file %q: %v", targetPath, err)
		writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
		return
	}
	defer f.Close()

	if err := writeWSOK(conn, wsAck{OK: true}); err != nil {
		return
	}

	// Frames are a purely transport-level split of one logical upload: XOR
	// and AES-CTR reset their cipher state on every Untransform call, and
	// PDF/html-b64 chunks are self-contained documents, so all frames must
	// be reassembled into one buffer before a single Untransform call —
	// Untransforming each received message independently (as this used to
	// do, back when there was always exactly one message per upload) would
	// silently corrupt anything split across more than one frame.
	var received bytes.Buffer

	for {
		mt, p, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("ws upload: reading message for %q: %v", filename, err)
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

		received.Write(p)
	}

	reader, err := transformer.Untransform(&received)
	if err != nil {
		log.Printf("ws upload: applying transform %q to %q: %v", req.Transform, filename, err)
		writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
		return
	}
	if _, err := io.Copy(f, reader); err != nil {
		log.Printf("ws upload: writing %q: %v", targetPath, err)
		writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
		return
	}

	log.Printf("uploaded %q to %q", filename, targetPath)
	if err := writeWSOK(conn, wsAck{OK: true, Name: filename}); err != nil {
		return
	}
	writeWSClose(conn, websocket.CloseNormalClosure, "")
}
