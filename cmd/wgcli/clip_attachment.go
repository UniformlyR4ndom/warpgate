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
	"strconv"
	"strings"

	"warpgate/internal/transform"
)

// mimeForExt is a tiny image-extension → MIME map; wgcli's attachment command
// is a test tool, so it covers the common cases rather than sniffing content.
func mimeForExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return "application/octet-stream"
	}
}

func postClipAttachment(g *globalFlags, noteID int64, localPath, caption, transformName string, args json.RawMessage, t transform.Transformer) (int64, error) {
	raw, err := os.ReadFile(localPath)
	if err != nil {
		return 0, err
	}
	encoded, err := t.Transform(bytes.NewReader(raw))
	if err != nil {
		return 0, fmt.Errorf("encoding attachment: %w", err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	specPart, err := mw.CreateFormFile("transform", "transform.json")
	if err != nil {
		return 0, err
	}
	spec, err := g.encodeBody(struct {
		Transform string          `json:"transform"`
		Args      json.RawMessage `json:"args"`
		NoteID    int64           `json:"note_id"`
		Mime      string          `json:"mime"`
		Caption   string          `json:"caption,omitempty"`
	}{transformName, args, noteID, mimeForExt(localPath), caption})
	if err != nil {
		return 0, err
	}
	if _, err := specPart.Write(spec); err != nil {
		return 0, err
	}
	filePart, err := mw.CreateFormFile("file", filepath.Base(localPath))
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(filePart, encoded); err != nil {
		return 0, err
	}
	mw.Close()

	req, err := g.newRequest(http.MethodPost, "/api/clip/attachment", &body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var meta clipAttachmentMeta
	if err := g.decodeJSON(respBody, &meta); err != nil {
		return 0, err
	}
	return meta.ID, nil
}

func getClipAttachment(g *globalFlags, id int64, out, transformName string, args json.RawMessage, t transform.Transformer) error {
	body, err := g.encodeBody(struct {
		ID        int64           `json:"id"`
		Transform string          `json:"transform"`
		Args      json.RawMessage `json:"args"`
	}{id, transformName, args})
	if err != nil {
		return err
	}
	req, err := g.newRequest(http.MethodPost, "/api/clip/attachment/download", bytes.NewReader(body))
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
	encoded, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	reader, err := t.Untransform(bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("decoding attachment: %w", err)
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, reader); err != nil {
		return err
	}
	return nil
}

func runClipAttach(args []string) error {
	fs := flag.NewFlagSet("clip attach", flag.ExitOnError)
	g := addGlobalFlags(fs)
	transformName := fs.String("transform", "id", "transform to disguise the image bytes with")
	key := fs.String("key", "", "transform key (hex), where applicable")
	iv := fs.String("iv", "", "transform IV (hex), for aes-ctr")
	caption := fs.String("caption", "", "image caption")
	fs.Parse(args)

	if fs.NArg() < 2 {
		return fmt.Errorf("usage: wgcli clip attach [flags] <note-id> <image-file>")
	}
	noteID, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid note id %q: %w", fs.Arg(0), err)
	}
	targs, err := buildTransformArgs(*transformName, *key, *iv, "", "attachment")
	if err != nil {
		return err
	}
	t, err := transform.New(*transformName, targs)
	if err != nil {
		return err
	}
	id, err := postClipAttachment(g, noteID, fs.Arg(1), *caption, *transformName, targs, t)
	if err != nil {
		return err
	}
	fmt.Printf("attached image %d to note %d\n", id, noteID)
	return nil
}

func runClipGetAttachment(args []string) error {
	fs := flag.NewFlagSet("clip get-attachment", flag.ExitOnError)
	g := addGlobalFlags(fs)
	out := fs.String("o", "", "output file (required)")
	transformName := fs.String("transform", "id", "transform the server should disguise the bytes with")
	key := fs.String("key", "", "transform key (hex), where applicable")
	iv := fs.String("iv", "", "transform IV (hex), for aes-ctr")
	fs.Parse(args)

	if fs.NArg() < 1 || *out == "" {
		return fmt.Errorf("usage: wgcli clip get-attachment [flags] -o <file> <attachment-id>")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid attachment id %q: %w", fs.Arg(0), err)
	}
	targs, err := buildTransformArgs(*transformName, *key, *iv, "", "attachment")
	if err != nil {
		return err
	}
	t, err := transform.New(*transformName, targs)
	if err != nil {
		return err
	}
	if err := getClipAttachment(g, id, *out, *transformName, targs, t); err != nil {
		return err
	}
	fmt.Printf("wrote attachment %d to %s\n", id, *out)
	return nil
}
