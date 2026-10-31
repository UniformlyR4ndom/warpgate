package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"

	"warpgate/internal/transform"
)

type wsUploadRequest struct {
	Auth      string          `json:"auth"`
	Filename  string          `json:"filename"`
	Dir       string          `json:"dir"`
	Transform string          `json:"transform"`
	Args      json.RawMessage `json:"args"`
	FrameSize int64           `json:"frameSize"`
}

type wsDoneMessage struct {
	Done bool `json:"done"`
}

// handleWSUpload receives one file over a single WebSocket connection, as a
// sequence of binary frame messages (each up to the negotiated frameSize)
// followed by a {"done":true} text message. The target is replaced only
// once the whole file has been received and decoded.
func (s *Server) handleWSUpload(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgradeWS(w, r)
	if err != nil {
		log.Printf("ws upload: upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	req, encrypted, err := readWSHandshake[wsUploadRequest](s, conn)
	if err != nil {
		log.Printf("ws upload: reading control message: %v", err)
		return
	}

	if !s.authorizeWS(conn, encrypted, req.Auth) {
		return
	}

	frameSize, err := s.resolveFrameSize(req.FrameSize)
	if err != nil {
		s.writeWSError(conn, encrypted, websocket.ClosePolicyViolation, "%v", err)
		return
	}
	conn.SetReadLimit(int64(frameSize))

	filename, _, err := s.ulTarget(req.Dir, req.Filename)
	if err != nil {
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "%v", err)
		return
	}

	transformer, err := transform.New(req.Transform, req.Args)
	if err != nil {
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "invalid transform: %v", err)
		return
	}

	sess, _, ok := s.uploadSessions.tryClaim(filename, func() *ulSession { return &ulSession{} })
	if !ok {
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "upload already in progress for this file")
		return
	}
	defer s.uploadSessions.release(filename, true)
	// Like a first HTTP chunk, this upload supersedes any session on record.
	s.removeUlTemp(sess.tmpPath)
	sess.tmpPath = ""

	// Frames are a purely transport-level split of one logical upload: XOR
	// and AES-CTR reset their cipher state on every Untransform call, and
	// PDF/html-b64 chunks are self-contained documents, so all frames are
	// collected in rawPath before a single Untransform call.
	rawPath, err := s.createUlTemp()
	if err != nil {
		log.Printf("ws upload: creating temp file: %v", err)
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
		return
	}
	defer s.removeUlTemp(rawPath)
	raw, err := s.ulRoot.OpenFile(rawPath, os.O_RDWR, 0)
	if err != nil {
		log.Printf("ws upload: opening temp file: %v", err)
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
		return
	}
	defer raw.Close()

	if err := s.writeWSOK(conn, encrypted, wsAck{OK: true}); err != nil {
		return
	}

	for {
		mt, p, err := wsReadMessage(conn)
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("ws upload: reading message for %q: %v", filename, err)
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

		if _, err := raw.Write(p); err != nil {
			log.Printf("ws upload: writing temp file for %q: %v", filename, err)
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
			return
		}
	}

	outPath, err := s.createUlTemp()
	if err != nil {
		log.Printf("ws upload: creating temp file: %v", err)
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
		return
	}
	defer s.removeUlTemp(outPath)
	if _, err = raw.Seek(0, io.SeekStart); err == nil {
		err = appendUploadChunk(s.ulRoot, outPath, transformer, raw)
	}
	if err == nil {
		err = s.publishUpload(outPath, filename)
	}
	if err != nil {
		log.Printf("ws upload: processing %q (transform %q): %v", filename, req.Transform, err)
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
		return
	}

	if err := s.writeWSOK(conn, encrypted, wsAck{OK: true, Name: filepath.ToSlash(filename)}); err != nil {
		return
	}
	writeWSClose(conn, websocket.CloseNormalClosure, "")
}
