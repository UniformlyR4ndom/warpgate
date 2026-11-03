package main

import (
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
	req, err := g.newRequest(http.MethodGet, g.maybeEncryptedQuery("/api/files/ul-list"), nil)
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

	var entries []listEntry
	if err := g.decodeJSON(body, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func runUlList(args []string) error {
	fs := flag.NewFlagSet("ul-list", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	entries, err := fetchUlList(g)
	if err != nil {
		return err
	}
	annotateListPaths(entries, "")

	printEntries(g, entries, false)
	return nil
}
