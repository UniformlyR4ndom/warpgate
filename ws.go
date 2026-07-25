package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

const wsWriteWait = 10 * time.Second

// wsAck is the JSON envelope sent in response to control messages
// (the initial request and, for uploads, the final "done" message).
type wsAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Name  string `json:"name,omitempty"`
	Hash  string `json:"hash,omitempty"`
	// ChunkSize is sent once per chunk request, before its binary frames,
	// giving the exact transformed byte length so the client knows how many
	// bytes to read — needed because that length isn't always equal to the
	// requested plaintext chunk size (e.g. pdf, html-b64).
	ChunkSize int64 `json:"chunkSize,omitempty"`
}

func (s *Server) checkWSAuth(key string) bool {
	return s.cfg.NoAuth || key == s.authHash
}

// resolveFrameSize validates a client-requested per-transfer WS frame size
// against the server's configured ceiling (-ws-frame-max). A zero value
// means the client didn't specify one, in which case the ceiling itself is
// used (the pre-existing behavior of sending/receiving one unbounded frame
// per chunk).
func (s *Server) resolveFrameSize(requested int64) (int, error) {
	if requested == 0 {
		return s.cfg.WSFrameMax, nil
	}
	if requested < wsFrameSizeMin || requested > int64(s.cfg.WSFrameMax) {
		return 0, fmt.Errorf("frameSize must be between %d and %d bytes", wsFrameSizeMin, s.cfg.WSFrameMax)
	}
	return int(requested), nil
}

// readWSControl reads the next WebSocket message, requires it to be a text
// message, and decodes it as JSON into a value of type T.
func readWSControl[T any](conn *websocket.Conn) (T, error) {
	var v T
	mt, p, err := conn.ReadMessage()
	if err != nil {
		return v, err
	}
	if mt != websocket.TextMessage {
		return v, fmt.Errorf("expected text control message, got binary")
	}
	if err := json.Unmarshal(p, &v); err != nil {
		return v, fmt.Errorf("invalid JSON control message: %w", err)
	}
	return v, nil
}

func writeWSOK(conn *websocket.Conn, v any) error {
	return conn.WriteJSON(v)
}

// writeWSError sends a {"ok":false,"error":...} control message followed by
// a close frame with the given close code.
func writeWSError(conn *websocket.Conn, closeCode int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_ = conn.WriteJSON(wsAck{OK: false, Error: msg})
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(closeCode, msg),
		time.Now().Add(wsWriteWait))
}

func writeWSClose(conn *websocket.Conn, closeCode int, reason string) {
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(closeCode, reason),
		time.Now().Add(wsWriteWait))
}
