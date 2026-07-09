package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{}

const wsWriteWait = 10 * time.Second

// wsAck is the JSON envelope sent in response to control messages
// (the initial request and, for uploads, the final "done" message).
type wsAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Name  string `json:"name,omitempty"`
}

func (s *Server) checkWSAuth(key string) bool {
	return s.cfg.NoAuth || key == s.cfg.Key
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
