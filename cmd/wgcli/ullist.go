package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// fetchUlList lists the upload directory. Unlike fetchList, the endpoint
// takes no path — /api/files/ul-list only ever lists the top level of
// -ul-root.
func fetchUlList(g *globalFlags) ([]listEntry, error) {
	req, err := g.newRequest(http.MethodGet, "/api/files/ul-list", nil)
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

	var entries []listEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func runUlList(args []string) error {
	fs := flag.NewFlagSet("ul-list", flag.ExitOnError)
	g := addGlobalFlags(fs)
	fs.Parse(args)

	entries, err := fetchUlList(g)
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
