package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"warpgate/internal/transform"
)

func fetchDownloadChunkHTTP(g *globalFlags, prefix, remotePath string, start, size int64, transformName string, transformArgs json.RawMessage) ([]byte, error) {
	reqBody, err := g.encodeBody(struct {
		Path      string          `json:"path"`
		Start     int64           `json:"start"`
		Size      int64           `json:"size"`
		Transform string          `json:"transform"`
		Args      json.RawMessage `json:"args,omitempty"`
	}{remotePath, start, size, transformName, transformArgs})
	if err != nil {
		return nil, err
	}

	req, err := g.newRequest(http.MethodPost, prefix+"download", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}
	return io.ReadAll(resp.Body)
}

func downloadHTTP(g *globalFlags, prefix, remotePath, out string, transformName string, transformArgs json.RawMessage, transformer transform.Transformer, chunkSize int64) error {
	dirPath := path.Dir(remotePath)
	if dirPath == "." {
		dirPath = ""
	}
	baseName := path.Base(remotePath)

	entries, err := fetchList(g, prefix, dirPath)
	if err != nil {
		return fmt.Errorf("listing %q to determine file size: %w", dirPath, err)
	}
	size := int64(-1)
	for _, e := range entries {
		if e.Name == baseName && e.Type == "f" && e.Size != nil {
			size = *e.Size
			break
		}
	}
	if size < 0 {
		return fmt.Errorf("file %q not found under %q", baseName, dirPath)
	}

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	start := time.Now()
	var transferred int64
	for transferred < size {
		reqSize := chunkSize
		if remaining := size - transferred; reqSize > remaining {
			reqSize = remaining
		}

		chunk, err := fetchDownloadChunkHTTP(g, prefix, remotePath, transferred, reqSize, transformName, transformArgs)
		if err != nil {
			return fmt.Errorf("downloading chunk at offset %d: %w", transferred, err)
		}

		reader, err := transformer.Untransform(bytes.NewReader(chunk))
		if err != nil {
			return fmt.Errorf("decoding chunk at offset %d: %w", transferred, err)
		}
		if _, err := io.Copy(f, reader); err != nil {
			return fmt.Errorf("writing chunk at offset %d: %w", transferred, err)
		}

		transferred += reqSize
	}

	// A write error can surface only at Close.
	if err := f.Close(); err != nil {
		return err
	}
	printDownloadResult(g, remotePath, out, size, time.Since(start))
	return nil
}

func downloadWS(g *globalFlags, prefix, remotePath, out string, transformName string, transformArgs json.RawMessage, transformer transform.Transformer, chunkSize, frameSize int64) error {
	conn, err := g.dialWS(prefix + "ws/download")
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := writeWSJSON(conn, g, struct {
		Auth      string          `json:"auth,omitempty"`
		Path      string          `json:"path"`
		Transform string          `json:"transform"`
		Args      json.RawMessage `json:"args,omitempty"`
		FrameSize int64           `json:"frameSize,omitempty"`
	}{g.authToken(), remotePath, transformName, transformArgs, frameSize}); err != nil {
		return fmt.Errorf("sending initial request: %w", err)
	}

	ack, err := readWSAck(conn, g)
	if err != nil {
		return err
	}
	size := ack.Size

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()

	start := time.Now()
	var transferred int64
	for transferred < size {
		reqSize := chunkSize
		if remaining := size - transferred; reqSize > remaining {
			reqSize = remaining
		}

		if err := writeWSJSON(conn, g, wsChunkRequest{Start: transferred, Size: reqSize}); err != nil {
			return fmt.Errorf("requesting chunk at offset %d: %w", transferred, err)
		}

		// The transformed chunk isn't always reqSize bytes long (pdf/html-b64
		// both expand it), so the server announces the actual byte count
		// before sending any frames.
		chunkAck, err := readWSAck(conn, g)
		if err != nil {
			return fmt.Errorf("reading chunk ack at offset %d: %w", transferred, err)
		}

		chunk := make([]byte, 0, chunkAck.ChunkSize)
		for int64(len(chunk)) < chunkAck.ChunkSize {
			mt, p, err := conn.ReadMessage()
			if err != nil {
				return fmt.Errorf("reading frame at offset %d: %w", transferred, err)
			}
			if mt != websocket.BinaryMessage {
				var errAck wsAck
				if jsonErr := g.decodeJSON(p, &errAck); jsonErr == nil && errAck.Error != "" {
					return fmt.Errorf("server error: %s", errAck.Error)
				}
				return fmt.Errorf("expected binary frame at offset %d, got message type %d", transferred, mt)
			}
			chunk = append(chunk, p...)
		}

		reader, err := transformer.Untransform(bytes.NewReader(chunk))
		if err != nil {
			return fmt.Errorf("decoding chunk at offset %d: %w", transferred, err)
		}
		if _, err := io.Copy(f, reader); err != nil {
			return fmt.Errorf("writing chunk at offset %d: %w", transferred, err)
		}

		transferred += reqSize
	}

	_ = writeWSJSON(conn, g, wsChunkRequest{Done: true})
	if err := f.Close(); err != nil {
		return err
	}
	printDownloadResult(g, remotePath, out, size, time.Since(start))
	return nil
}

func printDownloadResult(g *globalFlags, remotePath, out string, size int64, elapsed time.Duration) {
	throughput := float64(size) / 1e6 / elapsed.Seconds()
	g.report(map[string]any{"path": remotePath, "out": out, "bytes": size, "seconds": elapsed.Seconds()},
		"downloaded %d bytes to %s in %s (%.2f MB/s)", size, out, elapsed.Round(time.Millisecond), throughput)
}

func runDownload(args []string) error {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	g := addGlobalFlags(fs)
	out := fs.String("o", "", "local output file path (required)")
	transformName := fs.String("transform", "id", "transform: id|xor|aes-ctr|pdf|html-b64")
	key := fs.String("key", "", "hex key (xor, aes-ctr, html-b64); random if omitted")
	iv := fs.String("iv", "", "hex IV (aes-ctr, 16 bytes); random if omitted")
	password := fs.String("password", "", "PDF password (pdf transform; only if the file was encrypted)")
	chunkSize := fs.Int64("chunk-size", 4<<20, "chunk size in bytes for the download loop")
	backend := fs.String("backend", "http", "transport: http|ws")
	frameSize := fs.Int64("frame-size", 0, "WS frame size in bytes (ws backend only; 0 = server ceiling)")
	ul := fs.Bool("ul", false, "download from the upload directory instead of the download root")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: wgcli download [flags] <remote-path>")
	}
	remotePath := fs.Arg(0)
	if *out == "" {
		return fmt.Errorf("-o <local-file> is required")
	}
	if *chunkSize <= 0 {
		return fmt.Errorf("--chunk-size must be positive")
	}
	if *backend != "http" && *backend != "ws" {
		return fmt.Errorf("--backend must be http or ws")
	}

	transformArgs, err := buildTransformArgs(*transformName, *key, *iv, *password, "")
	if err != nil {
		return err
	}
	transformer, err := transform.New(*transformName, transformArgs)
	if err != nil {
		return fmt.Errorf("constructing transform: %w", err)
	}

	if *backend == "ws" {
		return downloadWS(g, apiPrefix(*ul), remotePath, *out, *transformName, transformArgs, transformer, *chunkSize, *frameSize)
	}
	return downloadHTTP(g, apiPrefix(*ul), remotePath, *out, *transformName, transformArgs, transformer, *chunkSize)
}
