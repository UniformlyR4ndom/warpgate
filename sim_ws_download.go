package main

import (
	"io"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

type wsSimDownloadRequest struct {
	Auth      string `json:"auth"`
	TotalSize int64  `json:"totalSize"`
	FrameSize int64  `json:"frameSize"`
}

// handleSimWSDownload serves one synthesized file (see simReader) over a
// single WebSocket connection, mirroring handleWSDownload's protocol and
// per-request frame splitting, but with no transform step and no filesystem
// access — this endpoint exists purely to benchmark transport behavior.
func (s *Server) handleSimWSDownload(w http.ResponseWriter, r *http.Request) {
	conn, err := s.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("sim ws download: upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	req, err := readWSControl[wsSimDownloadRequest](conn)
	if err != nil {
		log.Printf("sim ws download: reading control message: %v", err)
		return
	}

	if !s.checkWSAuth(req.Auth) {
		writeWSError(conn, websocket.ClosePolicyViolation, "unauthorized")
		return
	}

	frameSize, err := s.resolveFrameSize(req.FrameSize)
	if err != nil {
		writeWSError(conn, websocket.ClosePolicyViolation, "%v", err)
		return
	}

	if err := validateSimTotalSize(req.TotalSize); err != nil {
		writeWSError(conn, websocket.CloseInternalServerErr, "%v", err)
		return
	}

	if err := writeWSOK(conn, wsAck{OK: true, Size: req.TotalSize}); err != nil {
		return
	}

	// Reused across chunk requests; frameSize is fixed for the connection.
	frameBuf := make([]byte, frameSize)

	for {
		chunkReq, err := readWSControl[wsChunkRequest](conn)
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("sim ws download: reading chunk request: %v", err)
			}
			return
		}
		if chunkReq.Done {
			writeWSClose(conn, websocket.CloseNormalClosure, "")
			return
		}
		if chunkReq.Start < 0 || chunkReq.Size <= 0 {
			writeWSError(conn, websocket.CloseInternalServerErr, "size must be positive")
			return
		}

		reader := io.LimitReader(newSimReader(req.TotalSize, chunkReq.Start), chunkReq.Size)

		for {
			n, rerr := io.ReadFull(reader, frameBuf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, frameBuf[:n]); werr != nil {
					log.Printf("sim ws download: writing frame: %v", werr)
					return
				}
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			if rerr != nil {
				log.Printf("sim ws download: reading chunk data: %v", rerr)
				return
			}
		}
	}
}
