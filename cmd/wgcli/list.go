package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// listEntry mirrors the server's listEntry (list.go); it can't be imported
// directly since the server is package main.
type listEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size *int64 `json:"size,omitempty"`
}

func fetchList(g *globalFlags, dirPath string) ([]listEntry, error) {
	body, err := json.Marshal(map[string]string{"path": dirPath})
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

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(errBody)))
	}

	var entries []listEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	g := addGlobalFlags(fs)
	fs.Parse(args)

	dirPath := ""
	if fs.NArg() > 0 {
		dirPath = fs.Arg(0)
	}

	entries, err := fetchList(g, dirPath)
	if err != nil {
		return err
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
	return nil
}
