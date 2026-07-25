package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

// wsAck mirrors the server's wsAck (ws.go) — the JSON envelope sent in
// response to every WS control message.
type wsAck struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Name      string `json:"name,omitempty"`
	Hash      string `json:"hash,omitempty"`
	ChunkSize int64  `json:"chunkSize,omitempty"`
}

// wsChunkRequest mirrors the server's wsChunkRequest (ws_download.go).
type wsChunkRequest struct {
	Start int64 `json:"start"`
	Size  int64 `json:"size"`
	Done  bool  `json:"done,omitempty"`
}

func (g *globalFlags) wsURL(path string) (string, error) {
	u, err := url.Parse(g.url)
	if err != nil {
		return "", fmt.Errorf("invalid --url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported --url scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u.String(), nil
}

func (g *globalFlags) dialWS(path string) (*websocket.Conn, error) {
	target, err := g.wsURL(path)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{}
	if g.insecure {
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	conn, _, err := dialer.Dial(target, nil)
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", target, err)
	}
	return conn, nil
}

// readWSAck reads one JSON control message and treats a false "ok" as an error.
func readWSAck(conn *websocket.Conn) (wsAck, error) {
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil {
		return ack, fmt.Errorf("reading control message: %w", err)
	}
	if !ack.OK {
		return ack, fmt.Errorf("server error: %s", ack.Error)
	}
	return ack, nil
}

// writeWSBinaryFrames splits r into frameSize-sized binary WS frames (1 MiB
// if frameSize is unset). Frames are a purely transport-level split — an
// individual message doesn't need to be independently decodable (see
// CLAUDE.md's WebSocket Transport / Upload protocol).
func writeWSBinaryFrames(conn *websocket.Conn, r io.Reader, frameSize int64) error {
	if frameSize <= 0 {
		frameSize = 1 << 20
	}
	buf := make([]byte, frameSize)
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return fmt.Errorf("writing frame: %w", werr)
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("reading data: %w", rerr)
		}
	}
}
