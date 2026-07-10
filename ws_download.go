package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/websocket"

	"warpgate/internal/transform"
)

type wsDownloadRequest struct {
	Auth      string          `json:"auth"`
	Path      string          `json:"path"`
	Transform string          `json:"transform"`
	Args      json.RawMessage `json:"args"`
	FrameSize int64           `json:"frameSize"`
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
	conn, err := s.wsUpgrader.Upgrade(w, r, nil)
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

	frameSize, err := s.resolveFrameSize(req.FrameSize)
	if err != nil {
		writeWSError(conn, websocket.ClosePolicyViolation, "%v", err)
		return
	}

	target, err := makePath(s.absRoot, req.Path)
	if err != nil {
		log.Printf("ws download: invalid path %q requested", req.Path)
		writeWSError(conn, websocket.CloseInternalServerErr, "invalid path")
		return
	}

	transformer, err := transform.New(req.Transform, req.Args)
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

	// Reused across chunk requests; frameSize is fixed for the connection.
	frameBuf := make([]byte, frameSize)

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

		// The client needs the exact transformed length before it can know
		// how many bytes to read off the wire for this chunk (it isn't
		// always chunkReq.Size — e.g. pdf, html-b64). Transforms that can
		// compute it without materializing their output do so (streaming is
		// preserved); the rest are fully buffered here so the length can be
		// measured directly.
		var chunkLen int64
		if st, ok := transformer.(transform.SizedTransformer); ok {
			chunkLen = st.EncodedLen(chunkReq.Size)
		} else {
			data, err := io.ReadAll(reader)
			if err != nil {
				writeWSError(conn, websocket.CloseInternalServerErr, "internal error")
				return
			}
			chunkLen = int64(len(data))
			reader = bytes.NewReader(data)
		}
		if err := writeWSOK(conn, wsAck{OK: true, ChunkSize: chunkLen}); err != nil {
			return
		}

		// Manually split the chunk's transformed output into frameSize-sized
		// WS messages instead of relying on gorilla's connection-wide buffer
		// to auto-fragment one big message: this makes frame size a
		// per-request value instead of a fixed per-connection one.
		for {
			n, rerr := io.ReadFull(reader, frameBuf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, frameBuf[:n]); werr != nil {
					log.Printf("ws download: writing frame for %q: %v", req.Path, werr)
					return
				}
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			if rerr != nil {
				log.Printf("ws download: reading chunk data for %q: %v", req.Path, rerr)
				return
			}
		}
	}
}
