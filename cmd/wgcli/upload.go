package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

	"warpgate/internal/transform"
)

// postUploadChunk sends one request to /api/files/upload — either a whole
// one-shot upload (continuationKey "" and done true) or one chunk of a
// chunked upload — and returns the server's response fields. name is resent
// on every chunk of a chunked upload: the server keys its session store by
// the multipart "file" part's filename on every request, not just the
// first, so an inconsistent name would land a continuation chunk in the
// wrong (or a brand-new) session instead of continuing the real one. dir
// ("" = upload root) is resent for the same reason.
func postUploadChunk(g *globalFlags, chunkData io.Reader, dir, name, transformName string, transformArgs json.RawMessage, continuationKey string, done bool) (uploadResult, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	specPart, err := mw.CreateFormFile("transform", "transform.json")
	if err != nil {
		return uploadResult{}, err
	}
	specBody, err := g.encodeBody(struct {
		Transform       string          `json:"transform"`
		Args            json.RawMessage `json:"args,omitempty"`
		Dir             string          `json:"dir,omitempty"`
		ContinuationKey string          `json:"continuation-key,omitempty"`
		// Done must stay a plain (non-omitempty) bool: omitting it on a
		// false value would make the server default ul-done to true,
		// prematurely finishing every non-final chunk.
		Done bool `json:"ul-done"`
	}{transformName, transformArgs, dir, continuationKey, done})
	if err != nil {
		return uploadResult{}, err
	}
	if _, err := specPart.Write(specBody); err != nil {
		return uploadResult{}, err
	}

	filePart, err := mw.CreateFormFile("file", name)
	if err != nil {
		return uploadResult{}, err
	}
	if _, err := io.Copy(filePart, chunkData); err != nil {
		return uploadResult{}, fmt.Errorf("writing encoded upload body: %w", err)
	}

	if err := mw.Close(); err != nil {
		return uploadResult{}, err
	}

	req, err := g.newRequest(http.MethodPost, "/api/files/upload", &body)
	if err != nil {
		return uploadResult{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return uploadResult{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return uploadResult{}, err
	}

	// The server answers a first-chunk request (whether or not it's also
	// the final one) with 201, and every continuation-key request
	// (final or not) with 200.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return uploadResult{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var result uploadResult
	if err := g.decodeJSON(respBody, &result); err != nil {
		return uploadResult{}, err
	}
	return result, nil
}

type uploadResult struct {
	Name            string `json:"name"`
	ContinuationKey string `json:"continuation-key"`
	SHA256          string `json:"sha256"`
}

// checkUpload compares the local file's hash with the one the server
// computed over what it stored.
func checkUpload(local, remote string) (verified bool, err error) {
	if remote == "" {
		fmt.Fprintln(os.Stderr, "wgcli: not verified: the server sent no SHA-256 (older warpgate?)")
		return false, nil
	}
	if local != remote {
		return false, fmt.Errorf("verification failed: the server stored data with SHA-256 %s, the local file has %s (altered in transit); upload again", remote, local)
	}
	return true, nil
}

// uploadFile uploads localPath as dir/name and verifies the stored hash.
func uploadFile(g *globalFlags, o xferOpts, localPath, dir, name string) (fileResult, error) {
	var res uploadResult
	var sum string
	var err error
	switch {
	case o.ws:
		res, sum, err = uploadWS(g, o, localPath, dir, name)
	case o.chunkSize > 0:
		res, sum, err = uploadHTTPChunked(g, o, localPath, dir, name)
	default:
		res, sum, err = uploadHTTP(g, o, localPath, dir, name)
	}
	var verified bool
	if err == nil {
		verified, err = checkUpload(sum, res.SHA256)
	}
	if err != nil {
		return fileResult{}, err
	}
	var size int64
	if fi, err := os.Stat(localPath); err == nil {
		size = fi.Size()
	}
	return fileResult{Path: localPath, Name: res.Name, Bytes: size, SHA256: sum, Verified: verified}, nil
}

func uploadHTTP(g *globalFlags, o xferOpts, localPath, dir, name string) (uploadResult, string, error) {
	inFile, err := os.Open(localPath)
	if err != nil {
		return uploadResult{}, "", err
	}
	defer inFile.Close()

	h := sha256.New()
	encoded, err := o.transformer.Transform(io.TeeReader(inFile, h))
	if err != nil {
		return uploadResult{}, "", fmt.Errorf("encoding upload: %w", err)
	}
	res, err := postUploadChunk(g, encoded, dir, name, o.transformName, o.transformArgs, "", true)
	return res, hex.EncodeToString(h.Sum(nil)), err
}

// uploadHTTPChunked splits localPath into chunkSize-byte plaintext pieces,
// each transformed independently (transformer.Transform is stateless per
// call — see CLAUDE.md's "Transform System"), and sends them one at a time
// via the continuation-key protocol described in CLAUDE.md's "Uploads"
// section. HTTP only — there is no WS equivalent of
// chunked upload.
func uploadHTTPChunked(g *globalFlags, o xferOpts, localPath, dir, name string) (uploadResult, string, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return uploadResult{}, "", err
	}
	size := info.Size()

	f, err := os.Open(localPath)
	if err != nil {
		return uploadResult{}, "", err
	}
	defer f.Close()

	h := sha256.New()
	src := io.TeeReader(f, h)
	var transferred int64
	var res uploadResult
	for {
		reqSize := o.chunkSize
		if remaining := size - transferred; reqSize > remaining {
			reqSize = remaining
		}
		done := transferred+reqSize >= size

		encoded, err := o.transformer.Transform(io.LimitReader(src, reqSize))
		if err != nil {
			return uploadResult{}, "", fmt.Errorf("encoding chunk at offset %d: %w", transferred, err)
		}

		res, err = postUploadChunk(g, encoded, dir, name, o.transformName, o.transformArgs, res.ContinuationKey, done)
		if err != nil {
			return uploadResult{}, "", fmt.Errorf("uploading chunk at offset %d: %w", transferred, err)
		}

		transferred += reqSize
		if done {
			break
		}
	}

	return res, hex.EncodeToString(h.Sum(nil)), nil
}

func uploadWS(g *globalFlags, o xferOpts, localPath, dir, name string) (uploadResult, string, error) {
	fail := func(err error) (uploadResult, string, error) { return uploadResult{}, "", err }
	inFile, err := os.Open(localPath)
	if err != nil {
		return fail(err)
	}
	defer inFile.Close()

	h := sha256.New()
	encoded, err := o.transformer.Transform(io.TeeReader(inFile, h))
	if err != nil {
		return fail(fmt.Errorf("encoding upload: %w", err))
	}

	conn, err := g.dialWS("/api/files/ws/upload")
	if err != nil {
		return fail(err)
	}
	defer conn.Close()

	if err := writeWSJSON(conn, g, struct {
		Auth      string          `json:"auth,omitempty"`
		Filename  string          `json:"filename"`
		Dir       string          `json:"dir,omitempty"`
		Transform string          `json:"transform"`
		Args      json.RawMessage `json:"args,omitempty"`
		FrameSize int64           `json:"frameSize,omitempty"`
	}{g.authToken(), name, dir, o.transformName, o.transformArgs, o.frameSize}); err != nil {
		return fail(fmt.Errorf("sending initial request: %w", err))
	}
	if _, err := readWSAck(conn, g); err != nil {
		return fail(err)
	}
	if err := writeWSBinaryFrames(conn, encoded, o.frameSize); err != nil {
		return fail(err)
	}
	if err := writeWSJSON(conn, g, struct {
		Done bool `json:"done"`
	}{true}); err != nil {
		return fail(fmt.Errorf("sending done message: %w", err))
	}
	ack, err := readWSAck(conn, g)
	if err != nil {
		return fail(err)
	}
	return uploadResult{Name: ack.Name, SHA256: ack.SHA256}, hex.EncodeToString(h.Sum(nil)), nil
}

func printUploadResult(g *globalFlags, r fileResult, transformName string, elapsed time.Duration) {
	g.report(map[string]any{"path": r.Path, "name": r.Name, "bytes": r.Bytes, "sha256": r.SHA256, "verified": r.Verified, "transform": transformName, "seconds": elapsed.Seconds()},
		"uploaded %s as %q (%d bytes) in %s, %s", r.Path, r.Name, r.Bytes, elapsed.Round(time.Millisecond), hashNote(r))
}

func runUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	remoteName := fs.String("remote-name", "", "name to store the file as on the server (one file only; default: the local base name)")
	dir := fs.String("dir", "", "existing directory below the upload root to upload into (default: the upload root)")
	recursive := fs.Bool("r", false, "upload directories with their contents")
	force := fs.Bool("force", false, "with several paths or -r: also upload files that are unchanged on the server")
	transformName := fs.String("transform", "id", "transform: id|xor|aes-ctr|pdf|html-b64")
	key := fs.String("key", "", "hex key (xor, aes-ctr, html-b64); random if omitted")
	iv := fs.String("iv", "", "hex IV (aes-ctr, 16 bytes); random if omitted")
	filename := fs.String("filename", "", "attachment filename embedded in the PDF (pdf transform only)")
	backend := fs.String("backend", "http", "transport: http|ws")
	frameSize := sizeFlag(fs, "frame-size", 0, "WS frame size in bytes (ws backend only; 0 = 1 MiB default)")
	chunkSize := sizeFlag(fs, "chunk-size", 0, "chunk size in bytes for chunked upload via the continuation-key protocol (0 = one-shot; http backend only)")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: wgcli upload [flags] <local> [<local> ...]")
	}
	many := fs.NArg() > 1 || *recursive
	if many && *remoteName != "" {
		return fmt.Errorf("-remote-name needs exactly one file and no -r")
	}
	if *backend != "http" && *backend != "ws" {
		return fmt.Errorf("--backend must be http or ws")
	}
	if *chunkSize < 0 {
		return fmt.Errorf("--chunk-size must not be negative")
	}
	if *chunkSize > 0 && *backend != "http" && g.fromEnv["chunk-size"] {
		*chunkSize = 0 // WGCLI_CHUNK_SIZE doesn't apply to WS uploads
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
	o := xferOpts{
		transformName: *transformName, transformArgs: transformArgs, transformer: transformer,
		chunkSize: *chunkSize, frameSize: *frameSize, ws: *backend == "ws",
	}

	if many {
		return uploadMany(g, o, fs.Args(), *dir, *recursive, *force)
	}
	localPath := fs.Arg(0)
	if fi, err := os.Stat(localPath); err == nil && fi.IsDir() {
		return fmt.Errorf("%s is a directory (use -r)", localPath)
	}
	name := *remoteName
	if name == "" {
		name = filepath.Base(localPath)
	}
	start := time.Now()
	r, err := uploadFile(g, o, localPath, *dir, name)
	if err != nil {
		return err
	}
	printUploadResult(g, r, o.transformName, time.Since(start))
	return nil
}
