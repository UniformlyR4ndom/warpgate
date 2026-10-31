package main

import (
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// clipAttachmentMeta mirrors the server's AttachmentMeta (db.go).
type clipAttachmentMeta struct {
	ID      int64  `json:"id"`
	Mime    string `json:"mime"`
	Size    int64  `json:"size"`
	Caption string `json:"caption"`
}

// clipNoteMeta mirrors the server's NoteMeta (db.go).
type clipNoteMeta struct {
	ID          int64                `json:"id"`
	TS          int64                `json:"ts"`
	Title       string               `json:"title"`
	Data        string               `json:"data"`
	Attachments []clipAttachmentMeta `json:"attachments"`
}

type clipStateResponse struct {
	Rev     int64          `json:"rev"`
	Changed []clipNoteMeta `json:"changed"`
	Ids     []int64        `json:"ids"`
	Reset   bool           `json:"reset"`
}

// fetchClipList pulls the full note set via a since=0 sync snapshot.
func fetchClipList(g *globalFlags) ([]clipNoteMeta, error) {
	path := "/api/clip/state?since=0"
	if _, enabled := g.paramKeys(); enabled {
		path += "&e=1"
	}
	req, err := g.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var state clipStateResponse
	if err := g.decodeJSON(body, &state); err != nil {
		return nil, err
	}
	return state.Changed, nil
}

func postClipNew(g *globalFlags, title string, data []byte) (id, ts int64, err error) {
	body, err := g.encodeBody(struct {
		Title string `json:"title,omitempty"`
		Data  string `json:"data"`
	}{title, base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return 0, 0, err
	}

	req, err := g.newRequest(http.MethodPost, "/api/clip/new", bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var result struct {
		ID int64 `json:"id"`
		TS int64 `json:"ts"`
	}
	if err := g.decodeJSON(respBody, &result); err != nil {
		return 0, 0, err
	}
	return result.ID, result.TS, nil
}

// postClipEdit leaves the note's title unchanged when title is nil.
func postClipEdit(g *globalFlags, id int64, title *string, data []byte) error {
	body, err := g.encodeBody(struct {
		ID    int64   `json:"id"`
		Title *string `json:"title,omitempty"`
		Data  string  `json:"data"`
	}{id, title, base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return err
	}

	req, err := g.newRequest(http.MethodPost, "/api/clip/edit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}
	return nil
}

func deleteClip(g *globalFlags, id int64) error {
	body, err := g.encodeBody(struct {
		ID int64 `json:"id"`
	}{id})
	if err != nil {
		return err
	}

	req, err := g.newRequest(http.MethodDelete, "/api/clip/delete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}
	return nil
}

// clipTextArg resolves a note's content either from -f <file> or from the
// positional argument at argIndex.
func clipTextArg(fs *flag.FlagSet, file string, argIndex int) ([]byte, error) {
	if file != "" {
		return os.ReadFile(file)
	}
	if fs.NArg() <= argIndex {
		return nil, fmt.Errorf("provide note text as an argument or use -f <file>")
	}
	return []byte(fs.Arg(argIndex)), nil
}

func runClipList(args []string) error {
	fs := flag.NewFlagSet("clip list", flag.ExitOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	items, err := fetchClipList(g)
	if err != nil {
		return err
	}
	for _, it := range items {
		data, err := base64.StdEncoding.DecodeString(it.Data)
		if err != nil {
			return fmt.Errorf("decoding note %d: %w", it.ID, err)
		}
		ts := time.Unix(it.TS, 0).Format(time.RFC3339)
		head := fmt.Sprintf("[%d] %s", it.ID, ts)
		if it.Title != "" {
			head += " " + it.Title
		}
		if n := len(it.Attachments); n > 0 {
			head += fmt.Sprintf(" (%d image(s): ", n)
			for i, a := range it.Attachments {
				if i > 0 {
					head += ", "
				}
				head += fmt.Sprintf("#%d %s %dB", a.ID, a.Mime, a.Size)
				if a.Caption != "" {
					head += fmt.Sprintf(" %q", a.Caption)
				}
			}
			head += ")"
		}
		fmt.Printf("%s\n%s\n\n", head, data)
	}
	return nil
}

func runClipNew(args []string) error {
	fs := flag.NewFlagSet("clip new", flag.ExitOnError)
	g := addGlobalFlags(fs)
	file := fs.String("f", "", "read note content from this file instead of the positional argument")
	title := fs.String("title", "", "note title (optional)")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	data, err := clipTextArg(fs, *file, 0)
	if err != nil {
		return err
	}

	id, ts, err := postClipNew(g, *title, data)
	if err != nil {
		return err
	}
	fmt.Printf("created note %d at %s\n", id, time.Unix(ts, 0).Format(time.RFC3339))
	return nil
}

func runClipEdit(args []string) error {
	fs := flag.NewFlagSet("clip edit", flag.ExitOnError)
	g := addGlobalFlags(fs)
	file := fs.String("f", "", "read note content from this file instead of the positional argument")
	titleFlag := fs.String("title", "", "new note title; empty clears it, omitting the flag keeps the current one")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	var title *string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "title" {
			title = titleFlag
		}
	})

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: wgcli clip edit [flags] <id> [text]")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid note id %q: %w", fs.Arg(0), err)
	}
	data, err := clipTextArg(fs, *file, 1)
	if err != nil {
		return err
	}

	if err := postClipEdit(g, id, title, data); err != nil {
		return err
	}
	fmt.Printf("updated note %d\n", id)
	return nil
}

func runClipDelete(args []string) error {
	fs := flag.NewFlagSet("clip delete", flag.ExitOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: wgcli clip delete [flags] <id>")
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid note id %q: %w", fs.Arg(0), err)
	}

	if err := deleteClip(g, id); err != nil {
		return err
	}
	fmt.Printf("deleted note %d\n", id)
	return nil
}

func runClip(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: wgcli clip <list|new|edit|delete|attach|get-attachment> [flags] [args...]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runClipList(rest)
	case "new":
		return runClipNew(rest)
	case "edit":
		return runClipEdit(rest)
	case "delete":
		return runClipDelete(rest)
	case "attach":
		return runClipAttach(rest)
	case "get-attachment":
		return runClipGetAttachment(rest)
	default:
		return fmt.Errorf("unknown clip subcommand %q", sub)
	}
}
