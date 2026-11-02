package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// listEntry mirrors the server's listEntry (list.go); it can't be imported
// directly since the server is package main.
type listEntry struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Size   *int64 `json:"size,omitempty"`
	MTime  *int64 `json:"mtime,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

func printEntries(g *globalFlags, entries []listEntry) {
	if g.json {
		printJSON(entries)
		return
	}
	for _, e := range entries {
		if e.Type == "d" {
			fmt.Printf("%-40s <dir>\n", e.Name+"/")
			continue
		}
		size := int64(0)
		if e.Size != nil {
			size = *e.Size
		}
		fmt.Printf("%-40s %d\n", e.Name, size)
	}
}

// apiPrefix selects the download root (/api/files/) or, with ul, the upload
// directory (/api/ul/) for listing and downloading.
func apiPrefix(ul bool) string {
	if ul {
		return "/api/ul/"
	}
	return "/api/files/"
}

func fetchList(g *globalFlags, prefix, dirPath string) ([]listEntry, error) {
	return fetchListing(g, prefix, dirPath, false)
}

// fetchListing lists p, a directory or a single file; with hash, file
// entries carry their SHA-256.
func fetchListing(g *globalFlags, prefix, p string, hash bool) ([]listEntry, error) {
	body, err := g.encodeBody(struct {
		Path string `json:"path"`
		Hash bool   `json:"hash,omitempty"`
	}{p, hash})
	if err != nil {
		return nil, err
	}

	req, err := g.newRequest(http.MethodPost, prefix+"list", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var entries []listEntry
	if err := g.decodeJSON(respBody, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	g := addGlobalFlags(fs)
	ul := fs.Bool("ul", false, "list the upload directory instead of the download root")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	dirPath := ""
	if fs.NArg() > 0 {
		dirPath = fs.Arg(0)
	}

	entries, err := fetchList(g, apiPrefix(*ul), dirPath)
	if err != nil {
		return err
	}

	printEntries(g, entries)
	return nil
}
