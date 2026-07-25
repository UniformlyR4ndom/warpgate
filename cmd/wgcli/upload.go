package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"warpgate/internal/transform"
)

// postUploadChunk sends one request to /api/files/upload — either a whole
// one-shot upload (continuationKey "" and done true) or one chunk of a
// chunked upload — and returns the server's response fields. name is resent
// on every chunk of a chunked upload: the server keys its session store by
// the multipart "file" part's filename on every request, not just the
// first, so an inconsistent name would land a continuation chunk in the
// wrong (or a brand-new) session instead of continuing the real one.
func postUploadChunk(g *globalFlags, chunkData io.Reader, name, transformName string, transformArgs json.RawMessage, continuationKey string, done bool) (respName, nextContinuationKey string, err error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	specPart, err := mw.CreateFormFile("transform", "transform.json")
	if err != nil {
		return "", "", err
	}
	if err := json.NewEncoder(specPart).Encode(struct {
		Transform       string          `json:"transform"`
		Args            json.RawMessage `json:"args,omitempty"`
		ContinuationKey string          `json:"continuation-key,omitempty"`
		// Done must stay a plain (non-omitempty) bool: omitting it on a
		// false value would make the server default ul-done to true,
		// prematurely finishing every non-final chunk.
		Done bool `json:"ul-done"`
	}{transformName, transformArgs, continuationKey, done}); err != nil {
		return "", "", err
	}

	filePart, err := mw.CreateFormFile("file", name)
	if err != nil {
		return "", "", err
	}
	if _, err := io.Copy(filePart, chunkData); err != nil {
		return "", "", fmt.Errorf("writing encoded upload body: %w", err)
	}

	if err := mw.Close(); err != nil {
		return "", "", err
	}

	req, err := g.newRequest(http.MethodPost, "/api/files/upload", &body)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	// The server answers a first-chunk request (whether or not it's also
	// the final one) with 201, and every continuation-key request
	// (final or not) with 200.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}

	var result struct {
		Name            string `json:"name"`
		ContinuationKey string `json:"continuation-key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", err
	}
	return result.Name, result.ContinuationKey, nil
}

func uploadHTTP(g *globalFlags, localPath, name string, transformName string, transformArgs json.RawMessage, transformer transform.Transformer) error {
	inFile, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer inFile.Close()

	encoded, err := transformer.Transform(inFile)
	if err != nil {
		return fmt.Errorf("encoding upload: %w", err)
	}

	start := time.Now()
	respName, _, err := postUploadChunk(g, encoded, name, transformName, transformArgs, "", true)
	if err != nil {
		return err
	}
	fmt.Printf("uploaded %s as %q in %s\n", localPath, respName, time.Since(start).Round(time.Millisecond))
	return nil
}

// uploadHTTPChunked splits localPath into chunkSize-byte plaintext pieces,
// each transformed independently (transformer.Transform is stateless per
// call — see CLAUDE.md's "Transform System"), and sends them one at a time
// via the continuation-key protocol described in CLAUDE.md's "/api/files/
// upload multipart form" section. HTTP only — there is no WS equivalent of
// chunked upload.
func uploadHTTPChunked(g *globalFlags, localPath, name, transformName string, transformArgs json.RawMessage, transformer transform.Transformer, chunkSize int64) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	size := info.Size()

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	start := time.Now()
	var transferred int64
	var continuationKey, respName string
	for {
		reqSize := chunkSize
		if remaining := size - transferred; reqSize > remaining {
			reqSize = remaining
		}
		done := transferred+reqSize >= size

		encoded, err := transformer.Transform(io.LimitReader(f, reqSize))
		if err != nil {
			return fmt.Errorf("encoding chunk at offset %d: %w", transferred, err)
		}

		respName, continuationKey, err = postUploadChunk(g, encoded, name, transformName, transformArgs, continuationKey, done)
		if err != nil {
			return fmt.Errorf("uploading chunk at offset %d: %w", transferred, err)
		}

		transferred += reqSize
		if done {
			break
		}
	}

	fmt.Printf("uploaded %s as %q in %s (%d bytes, chunked)\n", localPath, respName, time.Since(start).Round(time.Millisecond), size)
	return nil
}

func uploadWS(g *globalFlags, localPath, name string, transformName string, transformArgs json.RawMessage, transformer transform.Transformer, frameSize int64) error {
	inFile, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer inFile.Close()

	encoded, err := transformer.Transform(inFile)
	if err != nil {
		return fmt.Errorf("encoding upload: %w", err)
	}

	conn, err := g.dialWS("/api/files/ws/upload")
	if err != nil {
		return err
	}
	defer conn.Close()

	initReq, err := json.Marshal(struct {
		Auth      string          `json:"auth,omitempty"`
		Filename  string          `json:"filename"`
		Transform string          `json:"transform"`
		Args      json.RawMessage `json:"args,omitempty"`
		FrameSize int64           `json:"frameSize,omitempty"`
	}{g.authToken(), name, transformName, transformArgs, frameSize})
	if err != nil {
		return err
	}
	if err := conn.WriteMessage(websocket.TextMessage, initReq); err != nil {
		return fmt.Errorf("sending initial request: %w", err)
	}
	if _, err := readWSAck(conn); err != nil {
		return err
	}

	start := time.Now()

	if err := writeWSBinaryFrames(conn, encoded, frameSize); err != nil {
		return err
	}

	if err := conn.WriteJSON(struct {
		Done bool `json:"done"`
	}{true}); err != nil {
		return fmt.Errorf("sending done message: %w", err)
	}

	ack, err := readWSAck(conn)
	if err != nil {
		return err
	}
	fmt.Printf("uploaded %s as %q in %s\n", localPath, ack.Name, time.Since(start).Round(time.Millisecond))
	return nil
}

func runUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ExitOnError)
	g := addGlobalFlags(fs)
	remoteName := fs.String("remote-name", "", "name to store the file as on the server (defaults to the local file's base name)")
	transformName := fs.String("transform", "id", "transform: id|xor|aes-ctr|pdf|html-b64")
	key := fs.String("key", "", "hex key (xor, aes-ctr, html-b64)")
	iv := fs.String("iv", "", "hex IV (aes-ctr, 16 bytes)")
	filename := fs.String("filename", "", "attachment filename embedded in the PDF (pdf transform only)")
	backend := fs.String("backend", "http", "transport: http|ws")
	frameSize := fs.Int64("frame-size", 0, "WS frame size in bytes (ws backend only; 0 = 1 MiB default)")
	chunkSize := fs.Int64("chunk-size", 0, "chunk size in bytes for chunked upload via the continuation-key protocol (0 = one-shot; http backend only)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: wgcli upload [flags] <local-file>")
	}
	localPath := fs.Arg(0)
	name := *remoteName
	if name == "" {
		name = filepath.Base(localPath)
	}
	if *backend != "http" && *backend != "ws" {
		return fmt.Errorf("--backend must be http or ws")
	}
	if *chunkSize < 0 {
		return fmt.Errorf("--chunk-size must not be negative")
	}
	if *chunkSize > 0 && *backend != "http" {
		return fmt.Errorf("--chunk-size requires --backend=http (no WS equivalent of chunked upload)")
	}

	// Uploads are never sent password-protected (matching the frontend's
	// pdf-lib limitation: it can't produce encrypted PDFs either), so no
	// --password flag is offered here.
	transformArgs, err := buildTransformArgs(*transformName, *key, *iv, "", *filename)
	if err != nil {
		return err
	}
	transformer, err := transform.New(*transformName, transformArgs)
	if err != nil {
		return fmt.Errorf("constructing transform: %w", err)
	}

	if *backend == "ws" {
		return uploadWS(g, localPath, name, *transformName, transformArgs, transformer, *frameSize)
	}
	if *chunkSize > 0 {
		return uploadHTTPChunked(g, localPath, name, *transformName, transformArgs, transformer, *chunkSize)
	}
	return uploadHTTP(g, localPath, name, *transformName, transformArgs, transformer)
}
