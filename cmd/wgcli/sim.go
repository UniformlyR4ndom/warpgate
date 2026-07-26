package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"hash"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// expectedSimHash reproduces sim.go's simReader algorithm locally — the
// decimal totalSize, a space, then 0xFF padding out to totalSize — so a
// downloaded synthetic file's hash can be checked without a second network
// round trip. Hashes in fixed-size chunks instead of materializing the
// whole totalSize-byte file, since totalSize can be up to 1 GiB.
func expectedSimHash(totalSize int64) (string, error) {
	header := []byte(strconv.FormatInt(totalSize, 10) + " ")
	if int64(len(header)) > totalSize {
		return "", fmt.Errorf("totalSize %d too small to fit its own size header", totalSize)
	}

	h := sha256.New()
	h.Write(header)

	remaining := totalSize - int64(len(header))
	pad := bytes.Repeat([]byte{0xff}, 64*1024)
	for remaining > 0 {
		n := int64(len(pad))
		if n > remaining {
			n = remaining
		}
		h.Write(pad[:n])
		remaining -= n
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func printSimResult(direction string, totalSize int64, elapsed time.Duration) {
	throughput := float64(totalSize) / 1e6 / elapsed.Seconds()
	fmt.Printf("%s %d bytes in %s (%.2f MB/s)\n", direction, totalSize, elapsed.Round(time.Millisecond), throughput)
}

// checkSimHash reports (to stdout) or errors on a mismatch between a sim
// transfer's expected and actual hash, matching the browser benchmark tab's
// treatment of a mismatch as a failed run rather than a silently-wrong one.
func checkSimHash(expected, actual string) error {
	if actual != expected {
		return fmt.Errorf("hash mismatch: expected %s, got %s (data corrupted in transit)", expected, actual)
	}
	fmt.Printf("hash OK: %s\n", actual)
	return nil
}

// hashSimChunkHTTP requests one chunk from /api/sim/download and hashes the
// response body directly, without buffering it — there's nothing to write
// to disk, only a running hash to update.
func hashSimChunkHTTP(g *globalFlags, h hash.Hash, totalSize, start, size int64) error {
	reqBody, err := g.encodeBody(struct {
		TotalSize int64 `json:"totalSize"`
		Start     int64 `json:"start"`
		Size      int64 `json:"size"`
	}{totalSize, start, size})
	if err != nil {
		return err
	}

	req, err := g.newRequest(http.MethodPost, "/api/sim/download", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}

	n, err := io.Copy(h, resp.Body)
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("expected %d bytes, got %d", size, n)
	}
	return nil
}

func simDownloadHTTP(g *globalFlags, totalSize, chunkSize int64) error {
	expectedHash, err := expectedSimHash(totalSize)
	if err != nil {
		return err
	}

	h := sha256.New()
	start := time.Now()
	var transferred int64
	for transferred < totalSize {
		reqSize := chunkSize
		if remaining := totalSize - transferred; reqSize > remaining {
			reqSize = remaining
		}
		if err := hashSimChunkHTTP(g, h, totalSize, transferred, reqSize); err != nil {
			return fmt.Errorf("downloading chunk at offset %d: %w", transferred, err)
		}
		transferred += reqSize
	}
	elapsed := time.Since(start)

	printSimResult("downloaded", totalSize, elapsed)
	return checkSimHash(expectedHash, hex.EncodeToString(h.Sum(nil)))
}

func simDownloadWS(g *globalFlags, totalSize, chunkSize, frameSize int64) error {
	conn, err := g.dialWS("/api/sim/ws/download")
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := writeWSJSON(conn, g, struct {
		Auth      string `json:"auth,omitempty"`
		TotalSize int64  `json:"totalSize"`
		FrameSize int64  `json:"frameSize,omitempty"`
	}{g.authToken(), totalSize, frameSize}); err != nil {
		return fmt.Errorf("sending initial request: %w", err)
	}
	if _, err := readWSAck(conn, g); err != nil {
		return err
	}

	expectedHash, err := expectedSimHash(totalSize)
	if err != nil {
		return err
	}

	h := sha256.New()
	start := time.Now()
	var transferred int64
	for transferred < totalSize {
		reqSize := chunkSize
		if remaining := totalSize - transferred; reqSize > remaining {
			reqSize = remaining
		}

		if err := writeWSJSON(conn, g, wsChunkRequest{Start: transferred, Size: reqSize}); err != nil {
			return fmt.Errorf("requesting chunk at offset %d: %w", transferred, err)
		}

		// Unlike /api/files/ws/download, no transform is applied here, so
		// the response is always exactly reqSize bytes — no separate
		// chunkSize ack is needed before reading frames.
		var received int64
		for received < reqSize {
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
			h.Write(p)
			received += int64(len(p))
		}

		transferred += reqSize
	}

	_ = writeWSJSON(conn, g, wsChunkRequest{Done: true})
	elapsed := time.Since(start)

	printSimResult("downloaded", totalSize, elapsed)
	return checkSimHash(expectedHash, hex.EncodeToString(h.Sum(nil)))
}

func runSimDownload(args []string) error {
	fs := flag.NewFlagSet("sim download", flag.ExitOnError)
	g := addGlobalFlags(fs)
	totalSize := fs.Int64("total-size", 0, "size in bytes of the synthetic file to download (required)")
	chunkSize := fs.Int64("chunk-size", 4<<20, "chunk size in bytes for the download loop")
	backend := fs.String("backend", "http", "transport: http|ws")
	frameSize := fs.Int64("frame-size", 0, "WS frame size in bytes (ws backend only; 0 = server ceiling)")
	fs.Parse(args)

	if *totalSize <= 0 {
		return fmt.Errorf("-total-size is required and must be positive")
	}
	if *chunkSize <= 0 {
		return fmt.Errorf("--chunk-size must be positive")
	}
	if *backend != "http" && *backend != "ws" {
		return fmt.Errorf("--backend must be http or ws")
	}

	if *backend == "ws" {
		return simDownloadWS(g, *totalSize, *chunkSize, *frameSize)
	}
	return simDownloadHTTP(g, *totalSize, *chunkSize)
}

// generateSimUploadBuffer fills a totalSize-byte buffer with random data for
// /api/sim/upload and /api/sim/ws/upload — there's no real file to upload,
// only the transport/hash behavior to probe. Unlike the browser's
// crypto.getRandomValues (capped at 64 KiB per call), crypto/rand.Read has
// no per-call size limit, so one call suffices.
func generateSimUploadBuffer(totalSize int64) ([]byte, error) {
	buf := make([]byte, totalSize)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// postSimUploadChunk sends one request to /api/sim/upload — either a whole
// one-shot upload (continuationKey "" and done true) or one chunk of a
// chunked upload — and returns the server's response fields. Unlike
// /api/files/upload there's no transform/args to carry, and every
// successful response is 200 (see sim_upload.go's writeSimUploadResponse —
// nothing is ever "created" here, unlike the real upload endpoint's
// 201-for-first-chunk).
func postSimUploadChunk(g *globalFlags, chunkData io.Reader, continuationKey string, done bool) (hashHex, nextContinuationKey string, err error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	specPart, err := mw.CreateFormFile("transform", "transform.json")
	if err != nil {
		return "", "", err
	}
	specBody, err := g.encodeBody(struct {
		ContinuationKey string `json:"continuation-key,omitempty"`
		Done            bool   `json:"ul-done"`
	}{continuationKey, done})
	if err != nil {
		return "", "", err
	}
	if _, err := specPart.Write(specBody); err != nil {
		return "", "", err
	}

	filePart, err := mw.CreateFormFile("file", "bench.bin")
	if err != nil {
		return "", "", err
	}
	if _, err := io.Copy(filePart, chunkData); err != nil {
		return "", "", fmt.Errorf("writing upload body: %w", err)
	}

	if err := mw.Close(); err != nil {
		return "", "", err
	}

	req, err := g.newRequest(http.MethodPost, "/api/sim/upload", &body)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var result struct {
		Hash            string `json:"hash"`
		ContinuationKey string `json:"continuation-key"`
	}
	if err := g.decodeJSON(respBody, &result); err != nil {
		return "", "", err
	}
	return result.Hash, result.ContinuationKey, nil
}

func simUploadHTTP(g *globalFlags, totalSize int64) error {
	buf, err := generateSimUploadBuffer(totalSize)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(buf)
	expectedHash := hex.EncodeToString(sum[:])

	start := time.Now()
	hashHex, _, err := postSimUploadChunk(g, bytes.NewReader(buf), "", true)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	printSimResult("uploaded", totalSize, elapsed)
	return checkSimHash(expectedHash, hashHex)
}

// simUploadHTTPChunked splits buf into chunkSize-byte pieces and sends them
// one at a time via the continuation-key protocol described in CLAUDE.md's
// "Chunked sim upload" section. HTTP only — there is no WS equivalent.
func simUploadHTTPChunked(g *globalFlags, totalSize, chunkSize int64) error {
	buf, err := generateSimUploadBuffer(totalSize)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(buf)
	expectedHash := hex.EncodeToString(sum[:])

	start := time.Now()
	var transferred int64
	var continuationKey, hashHex string
	for {
		reqSize := chunkSize
		if remaining := totalSize - transferred; reqSize > remaining {
			reqSize = remaining
		}
		done := transferred+reqSize >= totalSize

		hashHex, continuationKey, err = postSimUploadChunk(g, bytes.NewReader(buf[transferred:transferred+reqSize]), continuationKey, done)
		if err != nil {
			return fmt.Errorf("uploading chunk at offset %d: %w", transferred, err)
		}

		transferred += reqSize
		if done {
			break
		}
	}
	elapsed := time.Since(start)

	printSimResult("uploaded", totalSize, elapsed)
	return checkSimHash(expectedHash, hashHex)
}

func simUploadWS(g *globalFlags, totalSize, frameSize int64) error {
	buf, err := generateSimUploadBuffer(totalSize)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(buf)
	expectedHash := hex.EncodeToString(sum[:])

	conn, err := g.dialWS("/api/sim/ws/upload")
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := writeWSJSON(conn, g, struct {
		Auth      string `json:"auth,omitempty"`
		FrameSize int64  `json:"frameSize,omitempty"`
	}{g.authToken(), frameSize}); err != nil {
		return fmt.Errorf("sending initial request: %w", err)
	}
	if _, err := readWSAck(conn, g); err != nil {
		return err
	}

	start := time.Now()
	if err := writeWSBinaryFrames(conn, bytes.NewReader(buf), frameSize); err != nil {
		return err
	}

	if err := writeWSJSON(conn, g, struct {
		Done bool `json:"done"`
	}{true}); err != nil {
		return fmt.Errorf("sending done message: %w", err)
	}

	ack, err := readWSAck(conn, g)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	printSimResult("uploaded", totalSize, elapsed)
	return checkSimHash(expectedHash, ack.Hash)
}

func runSimUpload(args []string) error {
	fs := flag.NewFlagSet("sim upload", flag.ExitOnError)
	g := addGlobalFlags(fs)
	totalSize := fs.Int64("total-size", 0, "size in bytes of the synthetic buffer to upload (required)")
	chunkSize := fs.Int64("chunk-size", 0, "chunk size in bytes for chunked upload via the continuation-key protocol (0 = one-shot; http backend only)")
	backend := fs.String("backend", "http", "transport: http|ws")
	frameSize := fs.Int64("frame-size", 0, "WS frame size in bytes (ws backend only; 0 = 1 MiB default)")
	fs.Parse(args)

	if *totalSize <= 0 {
		return fmt.Errorf("-total-size is required and must be positive")
	}
	if *chunkSize < 0 {
		return fmt.Errorf("--chunk-size must not be negative")
	}
	if *backend != "http" && *backend != "ws" {
		return fmt.Errorf("--backend must be http or ws")
	}
	if *chunkSize > 0 && *backend != "http" {
		return fmt.Errorf("--chunk-size requires --backend=http (no WS equivalent of chunked upload)")
	}

	if *backend == "ws" {
		return simUploadWS(g, *totalSize, *frameSize)
	}
	if *chunkSize > 0 {
		return simUploadHTTPChunked(g, *totalSize, *chunkSize)
	}
	return simUploadHTTP(g, *totalSize)
}

func runSim(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: wgcli sim <download|upload> [flags] [args...]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "download":
		return runSimDownload(rest)
	case "upload":
		return runSimUpload(rest)
	default:
		return fmt.Errorf("unknown sim subcommand %q", sub)
	}
}
