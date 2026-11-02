package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
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

// fetchFileEntry looks up the file remotePath, with its SHA-256 if hash.
func fetchFileEntry(g *globalFlags, prefix, remotePath string, hash bool) (listEntry, error) {
	entries, err := fetchListing(g, prefix, remotePath, hash)
	if err != nil {
		return listEntry{}, fmt.Errorf("looking up %q: %w", remotePath, err)
	}
	if len(entries) != 1 || entries[0].Type != "f" || entries[0].Size == nil {
		return listEntry{}, fmt.Errorf("%q is not a file", remotePath)
	}
	return entries[0], nil
}

// checkDownload compares the written file's hash with the server's and
// removes the file on a mismatch. With recheck, it asks the server again to
// tell a file changed meanwhile from data altered in transit.
func checkDownload(g *globalFlags, prefix, remotePath, out, want, got string, recheck bool) error {
	if want == "" {
		fmt.Fprintln(os.Stderr, "wgcli: not verified: the server sent no SHA-256 (older warpgate?)")
		return nil
	}
	if got == want {
		return nil
	}
	_ = os.Remove(out)
	if recheck {
		if e, err := fetchFileEntry(g, prefix, remotePath, true); err == nil && e.SHA256 != want {
			return fmt.Errorf("verification failed: %q changed on the server during the download; %s removed, download again", remotePath, out)
		}
	}
	return fmt.Errorf("verification failed: data altered in transit (SHA-256 %s, server has %s); %s removed", got, want, out)
}

func downloadHTTP(g *globalFlags, prefix, remotePath, out string, transformName string, transformArgs json.RawMessage, transformer transform.Transformer, chunkSize int64, verify bool) error {
	entry, err := fetchFileEntry(g, prefix, remotePath, verify)
	if err != nil {
		return err
	}
	size := *entry.Size

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	w := io.MultiWriter(f, h)

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
		if _, err := io.Copy(w, reader); err != nil {
			return fmt.Errorf("writing chunk at offset %d: %w", transferred, err)
		}

		transferred += reqSize
	}

	// A write error can surface only at Close.
	if err := f.Close(); err != nil {
		return err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if verify {
		if err := checkDownload(g, prefix, remotePath, out, entry.SHA256, sum, true); err != nil {
			return err
		}
	}
	printDownloadResult(g, remotePath, out, size, sum, time.Since(start))
	return nil
}

func downloadWS(g *globalFlags, prefix, remotePath, out string, transformName string, transformArgs json.RawMessage, transformer transform.Transformer, chunkSize, frameSize int64, verify bool) error {
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
		Hash      bool            `json:"hash,omitempty"`
	}{g.authToken(), remotePath, transformName, transformArgs, frameSize, verify}); err != nil {
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
	h := sha256.New()
	w := io.MultiWriter(f, h)

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
		if _, err := io.Copy(w, reader); err != nil {
			return fmt.Errorf("writing chunk at offset %d: %w", transferred, err)
		}

		transferred += reqSize
	}

	_ = writeWSJSON(conn, g, wsChunkRequest{Done: true})
	if err := f.Close(); err != nil {
		return err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if verify {
		// The server hashed the file it served from, so a mismatch can only
		// come from the transfer.
		if err := checkDownload(g, prefix, remotePath, out, ack.SHA256, sum, false); err != nil {
			return err
		}
	}
	printDownloadResult(g, remotePath, out, size, sum, time.Since(start))
	return nil
}

func printDownloadResult(g *globalFlags, remotePath, out string, size int64, sum string, elapsed time.Duration) {
	throughput := float64(size) / 1e6 / elapsed.Seconds()
	g.report(map[string]any{"path": remotePath, "out": out, "bytes": size, "sha256": sum, "seconds": elapsed.Seconds()},
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
	chunkSize := sizeFlag(fs, "chunk-size", 4<<20, "chunk size in bytes for the download loop")
	backend := fs.String("backend", "http", "transport: http|ws")
	frameSize := sizeFlag(fs, "frame-size", 0, "WS frame size in bytes (ws backend only; 0 = server ceiling)")
	ul := fs.Bool("ul", false, "download from the upload directory instead of the download root")
	noVerify := fs.Bool("no-verify", false, "skip comparing the result's SHA-256 with the server's")
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
		return downloadWS(g, apiPrefix(*ul), remotePath, *out, *transformName, transformArgs, transformer, *chunkSize, *frameSize, !*noVerify)
	}
	return downloadHTTP(g, apiPrefix(*ul), remotePath, *out, *transformName, transformArgs, transformer, *chunkSize, !*noVerify)
}
