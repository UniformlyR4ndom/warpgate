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
)

type wsUploadRequest struct {
	Auth      string          `json:"auth"`
	Filename  string          `json:"filename"`
	Transform string          `json:"transform"`
	Args      json.RawMessage `json:"args"`
}

type wsDoneMessage struct {
	Done bool `json:"done"`
}

// handleWSUpload receives one file over a single WebSocket connection, as a
// sequence of binary chunk messages followed by a {"done":true} text
// message. Each chunk is independently transformed, matching how
// Untransform is already applied per-chunk on the HTTP download path.
func (s *Server) handleWSUpload(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
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

	// Normalize separators so Windows client paths are handled correctly.
	rawName := strings.ReplaceAll(req.Filename, "\\", "/")
	filename := filepath.Base(rawName)
	if filename == "" || filename == "." || filename == "/" {
		writeWSError(conn, websocket.CloseInternalServerErr, "invalid filename")
		return
	}

	transformer, err := newTransformer(req.Transform, req.Args)
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
			log.Printf("uploaded %q to %q", filename, targetPath)
			if err := writeWSOK(conn, wsAck{OK: true, Name: filename}); err != nil {
				return
			}
			writeWSClose(conn, websocket.CloseNormalClosure, "")
			return
		}

		reader, err := transformer.Untransform(bytes.NewReader(p))
		if err != nil {
			log.Printf("ws upload: applying transform %q to %q: %v", req.Transform, filename, err)
			writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
			return
		}
		if _, err := io.Copy(f, reader); err != nil {
			log.Printf("ws upload: writing chunk for %q: %v", targetPath, err)
			writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
			return
		}
	}
}
