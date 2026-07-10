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

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	specPart, err := mw.CreateFormFile("transform", "transform.json")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(specPart).Encode(struct {
		Transform string          `json:"transform"`
		Args      json.RawMessage `json:"args,omitempty"`
	}{transformName, transformArgs}); err != nil {
		return err
	}

	filePart, err := mw.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(filePart, encoded); err != nil {
		return fmt.Errorf("writing encoded upload body: %w", err)
	}

	if err := mw.Close(); err != nil {
		return err
	}

	req, err := g.newRequest(http.MethodPost, "/api/files/upload", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	start := time.Now()
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	fmt.Printf("uploaded %s as %q in %s\n", localPath, result.Name, time.Since(start).Round(time.Millisecond))
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
	}{g.authKey, name, transformName, transformArgs, frameSize})
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

	// Frames are a purely transport-level split; an individual message
	// doesn't need to be independently decodable (see CLAUDE.md's WebSocket
	// Transport / Upload protocol).
	writeBufSize := frameSize
	if writeBufSize <= 0 {
		writeBufSize = 1 << 20
	}
	buf := make([]byte, writeBufSize)
	for {
		n, rerr := io.ReadFull(encoded, buf)
		if n > 0 {
			if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return fmt.Errorf("writing frame: %w", werr)
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("reading encoded data: %w", rerr)
		}
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
	return uploadHTTP(g, localPath, name, *transformName, transformArgs, transformer)
}
