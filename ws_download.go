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
	Hash      bool            `json:"hash"`
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
func (s *Server) handleWSDownload(w http.ResponseWriter, r *http.Request, root *os.Root) {
	conn, err := s.upgradeWS(w, r)
	if err != nil {
		log.Printf("ws download: upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	req, encrypted, err := readWSHandshake[wsDownloadRequest](s, conn, r)
	if err != nil {
		log.Printf("ws download: reading control message: %v", err)
		return
	}

	if !s.authorizeWS(conn, r, encrypted, req.Auth) {
		return
	}

	frameSize, err := s.resolveFrameSize(req.FrameSize)
	if err != nil {
		s.writeWSError(conn, encrypted, websocket.ClosePolicyViolation, "%v", err)
		return
	}

	rel, err := rootRelPath(req.Path)
	if err != nil {
		log.Printf("ws download: invalid path %q requested", req.Path)
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "%v", err)
		return
	}

	transformer, err := transform.New(req.Transform, req.Args)
	if err != nil {
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "invalid transform: %v", err)
		return
	}

	if s.reachesUlTmp(root, rel) {
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, errUlTmpPath)
		return
	}

	f, err := root.Open(rel)
	if err != nil {
		if os.IsNotExist(err) {
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "file not found")
		} else if isPathEscape(err) {
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, errSymlinkEscape)
		} else {
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
		}
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
		return
	}

	ack := wsAck{OK: true, Size: info.Size()}
	if req.Hash {
		// Hashes the open file, so it describes exactly the bytes served
		// even if an upload replaces the file meanwhile.
		if ack.SHA256, err = hashReader(io.NewSectionReader(f, 0, info.Size())); err != nil {
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
			return
		}
	}
	if err := s.writeWSOK(conn, encrypted, ack); err != nil {
		return
	}

	// Reused across chunk requests; frameSize is fixed for the connection.
	frameBuf := make([]byte, frameSize)

	for {
		chunkReq, _, err := readWSControl[wsChunkRequest](s, conn)
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
		if !validRange(chunkReq.Start, chunkReq.Size) {
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, errInvalidRange)
			return
		}

		// Clamp the requested range to the file size announced at handshake,
		// so the length announced below is always one we can actually
		// deliver. A request reaching past EOF yields a shorter (possibly
		// empty) chunk, mirroring the HTTP download path, which streams
		// whatever bytes exist in the range. Without this the loop would
		// announce EncodedLen(chunkReq.Size) but send fewer bytes, leaving
		// the client blocked for data that never comes until the idle timeout.
		effSize := chunkReq.Size
		if avail := info.Size() - chunkReq.Start; effSize > avail {
			effSize = max(avail, 0)
		}

		if _, err := f.Seek(chunkReq.Start, io.SeekStart); err != nil {
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
			return
		}

		reader, err := transformer.Transform(io.LimitReader(f, effSize))
		if err != nil {
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
			return
		}

		// The client needs the exact transformed length before it can know
		// how many bytes to read off the wire for this chunk (it isn't
		// always effSize — e.g. pdf, html-b64). Transforms that can
		// compute it without materializing their output do so (streaming is
		// preserved); the rest are fully buffered here so the length can be
		// measured directly.
		var chunkLen int64
		if st, ok := transformer.(transform.SizedTransformer); ok {
			chunkLen = st.EncodedLen(effSize)
		} else {
			data, err := io.ReadAll(reader)
			if err != nil {
				s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "internal error")
				return
			}
			chunkLen = int64(len(data))
			reader = bytes.NewReader(data)
		}
		if err := s.writeWSOK(conn, encrypted, wsAck{OK: true, ChunkSize: chunkLen}); err != nil {
			return
		}

		// Manually split the chunk's transformed output into frameSize-sized
		// WS messages instead of relying on gorilla's connection-wide buffer
		// to auto-fragment one big message: this makes frame size a
		// per-request value instead of a fixed per-connection one.
		var sent int64
		for {
			n, rerr := io.ReadFull(reader, frameBuf)
			if n > 0 {
				if werr := wsWriteMessage(conn, websocket.BinaryMessage, frameBuf[:n]); werr != nil {
					log.Printf("ws download: writing frame for %q: %v", req.Path, werr)
					return
				}
				sent += int64(n)
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			if rerr != nil {
				log.Printf("ws download: reading chunk data for %q: %v", req.Path, rerr)
				return
			}
		}

		// The announced length is exact for the buffered branch and for the
		// sized transforms under a stable file. A shortfall here means the
		// file was truncated after the handshake stat: the client is still
		// waiting for the missing bytes, so fail the transfer instead of
		// returning to the request loop and deadlocking until the idle timeout.
		if sent != chunkLen {
			log.Printf("ws download: %q delivered %d of %d announced bytes (file changed during transfer)", req.Path, sent, chunkLen)
			s.writeWSError(conn, encrypted, websocket.CloseInternalServerErr, "file changed during transfer")
			return
		}
	}
}
