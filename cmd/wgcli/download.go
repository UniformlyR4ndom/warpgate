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

// checkDownload compares the written data's hash with the server's. With
// recheck, it asks the server again to tell a file changed meanwhile from
// data altered in transit.
func checkDownload(g *globalFlags, prefix, remotePath, want, got string, recheck bool) error {
	if want == "" {
		fmt.Fprintln(os.Stderr, "wgcli: not verified: the server sent no SHA-256 (older warpgate?)")
		return nil
	}
	if got == want {
		return nil
	}
	if recheck {
		if e, err := fetchFileEntry(g, prefix, remotePath, true); err == nil && e.SHA256 != want {
			return fmt.Errorf("verification failed: %q changed on the server during the download; download again", remotePath)
		}
	}
	return fmt.Errorf("verification failed: data altered in transit (SHA-256 %s, server has %s)", got, want)
}

// downloadFile downloads remotePath to out via out+".wgpart", so out
// appears only complete and verified. entry, if known, saves a lookup.
func downloadFile(g *globalFlags, o xferOpts, remotePath, out string, entry *listEntry) (fileResult, error) {
	tmp := out + ".wgpart"
	var size int64
	var sum string
	var err error
	if o.ws {
		size, sum, err = downloadWS(g, o, remotePath, tmp)
	} else {
		size, sum, err = downloadHTTP(g, o, remotePath, tmp, entry)
	}
	if err == nil {
		err = os.Rename(tmp, out)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fileResult{}, err
	}
	return fileResult{Path: remotePath, Out: out, Bytes: size, SHA256: sum}, nil
}

func downloadHTTP(g *globalFlags, o xferOpts, remotePath, dst string, entry *listEntry) (int64, string, error) {
	if entry == nil || (o.verify && entry.SHA256 == "") {
		e, err := fetchFileEntry(g, o.prefix, remotePath, o.verify)
		if err != nil {
			return 0, "", err
		}
		entry = &e
	}
	size := *entry.Size

	f, err := os.Create(dst)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	w := io.MultiWriter(f, h)

	var transferred int64
	for transferred < size {
		reqSize := o.chunkSize
		if remaining := size - transferred; reqSize > remaining {
			reqSize = remaining
		}

		chunk, err := fetchDownloadChunkHTTP(g, o.prefix, remotePath, transferred, reqSize, o.transformName, o.transformArgs)
		if err != nil {
			return 0, "", fmt.Errorf("downloading chunk at offset %d: %w", transferred, err)
		}

		reader, err := o.transformer.Untransform(bytes.NewReader(chunk))
		if err != nil {
			return 0, "", fmt.Errorf("decoding chunk at offset %d: %w", transferred, err)
		}
		if _, err := io.Copy(w, reader); err != nil {
			return 0, "", fmt.Errorf("writing chunk at offset %d: %w", transferred, err)
		}

		transferred += reqSize
	}

	// A write error can surface only at Close.
	if err := f.Close(); err != nil {
		return 0, "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if o.verify {
		if err := checkDownload(g, o.prefix, remotePath, entry.SHA256, sum, true); err != nil {
			return 0, "", err
		}
	}
	return size, sum, nil
}

func downloadWS(g *globalFlags, o xferOpts, remotePath, dst string) (int64, string, error) {
	conn, err := g.dialWS(o.prefix + "ws/download")
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()

	if err := writeWSJSON(conn, g, struct {
		Auth      string          `json:"auth,omitempty"`
		Path      string          `json:"path"`
		Transform string          `json:"transform"`
		Args      json.RawMessage `json:"args,omitempty"`
		FrameSize int64           `json:"frameSize,omitempty"`
		Hash      bool            `json:"hash,omitempty"`
	}{g.authToken(), remotePath, o.transformName, o.transformArgs, o.frameSize, o.verify}); err != nil {
		return 0, "", fmt.Errorf("sending initial request: %w", err)
	}

	ack, err := readWSAck(conn, g)
	if err != nil {
		return 0, "", err
	}
	size := ack.Size

	f, err := os.Create(dst)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	w := io.MultiWriter(f, h)

	var transferred int64
	for transferred < size {
		reqSize := o.chunkSize
		if remaining := size - transferred; reqSize > remaining {
			reqSize = remaining
		}

		if err := writeWSJSON(conn, g, wsChunkRequest{Start: transferred, Size: reqSize}); err != nil {
			return 0, "", fmt.Errorf("requesting chunk at offset %d: %w", transferred, err)
		}

		// The transformed chunk isn't always reqSize bytes long (pdf/html-b64
		// both expand it), so the server announces the actual byte count
		// before sending any frames.
		chunkAck, err := readWSAck(conn, g)
		if err != nil {
			return 0, "", fmt.Errorf("reading chunk ack at offset %d: %w", transferred, err)
		}

		chunk := make([]byte, 0, chunkAck.ChunkSize)
		for int64(len(chunk)) < chunkAck.ChunkSize {
			mt, p, err := conn.ReadMessage()
			if err != nil {
				return 0, "", fmt.Errorf("reading frame at offset %d: %w", transferred, err)
			}
			if mt != websocket.BinaryMessage {
				var errAck wsAck
				if jsonErr := g.decodeJSON(p, &errAck); jsonErr == nil && errAck.Error != "" {
					return 0, "", fmt.Errorf("server error: %s", errAck.Error)
				}
				return 0, "", fmt.Errorf("expected binary frame at offset %d, got message type %d", transferred, mt)
			}
			chunk = append(chunk, p...)
		}

		reader, err := o.transformer.Untransform(bytes.NewReader(chunk))
		if err != nil {
			return 0, "", fmt.Errorf("decoding chunk at offset %d: %w", transferred, err)
		}
		if _, err := io.Copy(w, reader); err != nil {
			return 0, "", fmt.Errorf("writing chunk at offset %d: %w", transferred, err)
		}

		transferred += reqSize
	}

	_ = writeWSJSON(conn, g, wsChunkRequest{Done: true})
	if err := f.Close(); err != nil {
		return 0, "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if o.verify {
		// The server hashed the file it served from, so a mismatch can only
		// come from the transfer.
		if err := checkDownload(g, o.prefix, remotePath, ack.SHA256, sum, false); err != nil {
			return 0, "", err
		}
	}
	return size, sum, nil
}

func printDownloadResult(g *globalFlags, remotePath, out string, size int64, sum string, elapsed time.Duration) {
	throughput := float64(size) / 1e6 / elapsed.Seconds()
	g.report(map[string]any{"path": remotePath, "out": out, "bytes": size, "sha256": sum, "seconds": elapsed.Seconds()},
		"downloaded %d bytes to %s in %s (%.2f MB/s)", size, out, elapsed.Round(time.Millisecond), throughput)
}

func runDownload(args []string) error {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	g := addGlobalFlags(fs)
	out := fs.String("o", "", "local output file; with several paths or -r, the directory receiving them (required)")
	recursive := fs.Bool("r", false, "download directories with their contents")
	force := fs.Bool("force", false, "with several paths or -r: also download files that are unchanged locally")
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
		return fmt.Errorf("usage: wgcli download [flags] -o <local> <remote-path> [<remote-path> ...]")
	}
	if *out == "" {
		return fmt.Errorf("-o is required")
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
	o := xferOpts{
		prefix: apiPrefix(*ul), transformName: *transformName, transformArgs: transformArgs, transformer: transformer,
		chunkSize: *chunkSize, frameSize: *frameSize, ws: *backend == "ws", verify: !*noVerify,
	}

	if fs.NArg() > 1 || *recursive {
		return downloadMany(g, o, fs.Args(), *out, *recursive, *force)
	}
	start := time.Now()
	r, err := downloadFile(g, o, fs.Arg(0), *out, nil)
	if err != nil {
		return err
	}
	printDownloadResult(g, r.Path, r.Out, r.Bytes, r.SHA256, time.Since(start))
	return nil
}
