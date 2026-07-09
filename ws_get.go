package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
)

type wsDownloadRequest struct {
	Auth      string          `json:"auth"`
	Path      string          `json:"path"`
	Transform string          `json:"transform"`
	Args      json.RawMessage `json:"args"`
}

// wsChunkRequest is sent by the client for each chunk it wants, or with
// Done set to true to end the session.
type wsChunkRequest struct {
	Start int64 `json:"start"`
	Size  int64 `json:"size"`
	Done  bool  `json:"done"`
}

// handleWSDownload serves one file over a single WebSocket connection.
// Auth is not checked by withAuth/header, since browsers can't set custom
// headers on the WS handshake; instead the first control message must carry
// the auth key, checked here after upgrade.
func (s *Server) handleWSDownload(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws download: upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	req, err := readWSControl[wsDownloadRequest](conn)
	if err != nil {
		log.Printf("ws download: reading control message: %v", err)
		return
	}

	if !s.checkWSAuth(req.Auth) {
		writeWSError(conn, websocket.ClosePolicyViolation, "unauthorized")
		return
	}

	target, err := makePath(s.absRoot, req.Path)
	if err != nil {
		log.Printf("ws download: invalid path %q requested", req.Path)
		writeWSError(conn, websocket.CloseInternalServerErr, "invalid path")
		return
	}

	transformer, err := newTransformer(req.Transform, req.Args)
	if err != nil {
		writeWSError(conn, websocket.CloseInternalServerErr, "invalid transform: %v", err)
		return
	}

	f, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) {
			writeWSError(conn, websocket.CloseInternalServerErr, "file not found")
		} else {
			writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
		}
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
		return
	}

	if err := writeWSOK(conn, wsAck{OK: true, Size: info.Size()}); err != nil {
		return
	}

	for {
		chunkReq, err := readWSControl[wsChunkRequest](conn)
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("ws download: reading chunk request for %q: %v", req.Path, err)
			}
			return
		}
		if chunkReq.Done {
			writeWSClose(conn, websocket.CloseNormalClosure, "")
			return
		}
		if chunkReq.Size <= 0 {
			writeWSError(conn, websocket.CloseInternalServerErr, "size must be positive")
			return
		}

		if _, err := f.Seek(chunkReq.Start, io.SeekStart); err != nil {
			writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
			return
		}

		reader, err := transformer.Transform(io.LimitReader(f, chunkReq.Size))
		if err != nil {
			writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
			return
		}

		wr, err := conn.NextWriter(websocket.BinaryMessage)
		if err != nil {
			log.Printf("ws download: getting writer for %q: %v", req.Path, err)
			return
		}
		if _, err := io.Copy(wr, reader); err != nil {
			log.Printf("ws download: writing chunk for %q: %v", req.Path, err)
			wr.Close()
			return
		}
		if err := wr.Close(); err != nil {
			log.Printf("ws download: closing chunk writer for %q: %v", req.Path, err)
			return
		}
	}
}
