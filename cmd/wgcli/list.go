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
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  *int64 `json:"size,omitempty"`
	MTime *int64 `json:"mtime,omitempty"`
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

func fetchList(g *globalFlags, dirPath string) ([]listEntry, error) {
	body, err := g.encodeBody(map[string]string{"path": dirPath})
	if err != nil {
		return nil, err
	}

	req, err := g.newRequest(http.MethodPost, "/api/files/list", bytes.NewReader(body))
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
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	dirPath := ""
	if fs.NArg() > 0 {
		dirPath = fs.Arg(0)
	}

	entries, err := fetchList(g, dirPath)
	if err != nil {
		return err
	}

	printEntries(g, entries)
	return nil
}
