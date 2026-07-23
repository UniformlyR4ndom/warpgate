package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// clipItem mirrors the server's ClipItem (db.go); it can't be imported
// directly since the server is package main.
type clipItem struct {
	ID   int64  `json:"id"`
	TS   int64  `json:"ts"`
	Data string `json:"data"` // base64-encoded
}

func fetchClipList(g *globalFlags) ([]clipItem, error) {
	req, err := g.newRequest(http.MethodGet, "/api/clip/list", nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}
	var items []clipItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	return items, nil
}

func postClipNew(g *globalFlags, data []byte) (id, ts int64, err error) {
	body, err := json.Marshal(struct {
		Data string `json:"data"`
	}{base64.StdEncoding.EncodeToString(data)})
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

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return 0, 0, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}
	var result struct {
		ID int64 `json:"id"`
		TS int64 `json:"ts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, 0, err
	}
	return result.ID, result.TS, nil
}

func postClipEdit(g *globalFlags, id int64, data []byte) error {
	body, err := json.Marshal(struct {
		ID   int64  `json:"id"`
		Data string `json:"data"`
	}{id, base64.StdEncoding.EncodeToString(data)})
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
	body, err := json.Marshal(struct {
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
	fs.Parse(args)

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
		fmt.Printf("[%d] %s\n%s\n\n", it.ID, ts, data)
	}
	return nil
}

func runClipNew(args []string) error {
	fs := flag.NewFlagSet("clip new", flag.ExitOnError)
	g := addGlobalFlags(fs)
	file := fs.String("f", "", "read note content from this file instead of the positional argument")
	fs.Parse(args)

	data, err := clipTextArg(fs, *file, 0)
	if err != nil {
		return err
	}

	id, ts, err := postClipNew(g, data)
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
	fs.Parse(args)

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

	if err := postClipEdit(g, id, data); err != nil {
		return err
	}
	fmt.Printf("updated note %d\n", id)
	return nil
}

func runClipDelete(args []string) error {
	fs := flag.NewFlagSet("clip delete", flag.ExitOnError)
	g := addGlobalFlags(fs)
	fs.Parse(args)

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
		return fmt.Errorf("usage: wgcli clip <list|new|edit|delete> [flags] [args...]")
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
	default:
		return fmt.Errorf("unknown clip subcommand %q", sub)
	}
}
