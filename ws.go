package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"warpgate/internal/paramcrypt"
)

const (
	wsWriteWait  = 10 * time.Second
	wsBufferSize = 64 << 10
)

// wsAuthTimeout bounds how long a fresh connection may take to send its
// authenticating first control message. A var so tests can shorten it.
var wsAuthTimeout = 10 * time.Second

// wsIdleTimeout closes an authenticated connection once no data has arrived
// for this long, or once a single outgoing message has taken this long to
// send. A var so tests can shorten it.
var wsIdleTimeout = 5 * time.Minute

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

// upgradeWS upgrades the request and applies the pre-auth limits: control
// messages are capped at maxParamBodySize and the first one must arrive
// within wsAuthTimeout.
func (s *Server) upgradeWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	conn, err := s.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(maxParamBodySize)
	_ = conn.SetReadDeadline(time.Now().Add(wsAuthTimeout))
	return conn, nil
}

// authorizeWS checks the handshake's auth token. On failure it sends the
// error and returns false; on success it lifts the pre-auth read deadline.
func (s *Server) authorizeWS(conn *websocket.Conn, r *http.Request, encrypted bool, key string) bool {
	if !s.cfg.NoAuth && !tokenEqual(key, s.authToken) {
		s.authLimit.failToken(s.authClient(r), key)
		s.writeWSError(conn, encrypted, websocket.ClosePolicyViolation, "unauthorized")
		return false
	}
	_ = conn.SetReadDeadline(time.Time{})
	return true
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

// readWSHandshake reads and decodes the first control message, still under
// the pre-auth deadline set by upgradeWS. A blocked client is refused, and a
// message that doesn't decode counts as a failed authentication, since an
// encrypted one fails to decode under the wrong key.
func readWSHandshake[T any](s *Server, conn *websocket.Conn, r *http.Request) (T, bool, error) {
	var v T
	mt, p, err := conn.ReadMessage()
	if err != nil {
		return v, false, err
	}
	if s.cfg.NoAuth {
		return decodeWSControl[T](s, mt, p)
	}
	client := s.authClient(r)
	if s.authLimit.blocked(client) {
		s.writeWSError(conn, false, websocket.ClosePolicyViolation, authBlockedMsg)
		return v, false, errors.New(authBlockedMsg)
	}
	v, encrypted, err := decodeWSControl[T](s, mt, p)
	if err != nil {
		s.authLimit.fail(client)
	}
	return v, encrypted, err
}

// readWSControl reads and decodes a control message after authentication.
func readWSControl[T any](s *Server, conn *websocket.Conn) (T, bool, error) {
	mt, p, err := wsReadMessage(conn)
	if err != nil {
		var v T
		return v, false, err
	}
	return decodeWSControl[T](s, mt, p)
}

// decodeWSControl requires a text message and decodes it into T, as plain
// JSON or as a paramcrypt blob (see Server.decodeParams). The returned bool
// reports whether it was encrypted, so a handler can encrypt its acks the
// same way for the rest of the connection.
func decodeWSControl[T any](s *Server, mt int, p []byte) (T, bool, error) {
	var v T
	if mt != websocket.TextMessage {
		return v, false, fmt.Errorf("expected text control message, got binary")
	}
	encrypted, err := s.decodeParams(bytes.NewReader(p), &v)
	if err != nil {
		return v, false, fmt.Errorf("invalid control message: %w", err)
	}
	return v, encrypted, nil
}

// wsReadMessage reads one message, failing once no data has arrived for
// wsIdleTimeout, so a large frame on a slow link is not cut off.
func wsReadMessage(conn *websocket.Conn) (int, []byte, error) {
	_ = conn.SetReadDeadline(time.Now().Add(wsIdleTimeout))
	mt, r, err := conn.NextReader()
	if err != nil {
		return mt, nil, err
	}
	p, err := io.ReadAll(idleReader{conn, r})
	return mt, p, err
}

type idleReader struct {
	conn *websocket.Conn
	r    io.Reader
}

func (ir idleReader) Read(p []byte) (int, error) {
	_ = ir.conn.SetReadDeadline(time.Now().Add(wsIdleTimeout))
	return ir.r.Read(p)
}

func wsWriteMessage(conn *websocket.Conn, mt int, data []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(wsIdleTimeout))
	return conn.WriteMessage(mt, data)
}

// writeWSOK sends v as a control message: as an encrypted paramcrypt blob
// when encrypted is true (only ever set from a connection whose handshake
// itself arrived encrypted — see readWSControl), or as plain JSON otherwise.
func (s *Server) writeWSOK(conn *websocket.Conn, encrypted bool, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if encrypted {
		blob, err := paramcrypt.Encrypt(s.paramKeys, body)
		if err != nil {
			return err
		}
		body = []byte(blob)
	}
	return wsWriteMessage(conn, websocket.TextMessage, body)
}

// writeWSError sends a {"ok":false,"error":...} control message followed by
// a close frame with the given close code.
func (s *Server) writeWSError(conn *websocket.Conn, encrypted bool, closeCode int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_ = s.writeWSOK(conn, encrypted, wsAck{OK: false, Error: msg})
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(closeCode, msg),
		time.Now().Add(wsWriteWait))
}

func writeWSClose(conn *websocket.Conn, closeCode int, reason string) {
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(closeCode, reason),
		time.Now().Add(wsWriteWait))
}
