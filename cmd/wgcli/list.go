package main

import (
	"bytes"
	"cmp"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// listEntry mirrors the server's listEntry (list.go); it can't be imported
// directly since the server is package main.
type listEntry struct {
	Name string `json:"name"`
	// Path is Name's root-relative path, filled in by the client so a listing
	// result feeds straight into download/upload; the server sends only Name.
	Path   string `json:"path,omitempty"`
	Type   string `json:"type"`
	Size   *int64 `json:"size,omitempty"`
	MTime  *int64 `json:"mtime,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

func printEntries(g *globalFlags, entries []listEntry, showPath bool) {
	if g.json {
		printJSON(entries)
		return
	}
	for _, e := range entries {
		label := e.Name
		if showPath && e.Path != "" {
			label = e.Path
		}
		if e.Type == "d" {
			fmt.Printf("%-40s <dir>\n", label+"/")
			continue
		}
		size := int64(0)
		if e.Size != nil {
			size = *e.Size
		}
		if e.SHA256 != "" {
			fmt.Printf("%-40s %d %s\n", label, size, e.SHA256)
			continue
		}
		fmt.Printf("%-40s %d\n", label, size)
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
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	ul := fs.Bool("ul", false, "list the upload directory instead of the download root")
	hash := fs.Bool("hash", false, "add each file's SHA-256, computed by the server")
	recursive := fs.Bool("r", false, "list the whole tree below the directory, one entry per line with its path from the root")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}

	dirPath := ""
	if fs.NArg() > 0 {
		dirPath = fs.Arg(0)
	}

	var entries []listEntry
	var err error
	if *recursive {
		entries, err = listTree(g, apiPrefix(*ul), dirPath, *hash)
	} else {
		entries, err = fetchListing(g, apiPrefix(*ul), dirPath, *hash)
		if err == nil {
			annotateListPaths(entries, dirPath)
		}
	}
	if err != nil {
		return err
	}

	printEntries(g, entries, *recursive)
	return nil
}

// annotateListPaths fills each entry's root-relative Path. Listing a single
// file returns that file as the only entry (Name is its base name), so its
// Path is the queried path; a directory's entries are its children.
func annotateListPaths(entries []listEntry, queried string) {
	q := path.Clean("/" + queried)[1:]
	if q != "" && len(entries) == 1 && entries[0].Type != "d" && entries[0].Name == path.Base(q) {
		entries[0].Path = q
		return
	}
	for i := range entries {
		entries[i].Path = path.Join(q, entries[i].Name)
	}
}

// listTree lists everything below p (a directory, or one file); each entry
// keeps its base Name and carries its root-relative Path.
func listTree(g *globalFlags, prefix, p string, hash bool) ([]listEntry, error) {
	p = path.Clean("/" + p)[1:]
	if p != "" {
		e, err := lookupRemote(g, prefix, p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if e.Type != "d" {
			entries, err := fetchListing(g, prefix, p, hash)
			for i := range entries {
				entries[i].Path = p
			}
			return entries, err
		}
	}
	out := []listEntry{}
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > maxRemoteDepth {
			return fmt.Errorf("%s: more than %d directory levels (a symlink loop on the server?)", dir, maxRemoteDepth)
		}
		entries, err := fetchListing(g, prefix, dir, hash)
		if err != nil {
			return fmt.Errorf("%s: %w", cmp.Or(dir, "/"), err)
		}
		for _, e := range entries {
			e.Path = path.Join(dir, e.Name)
			out = append(out, e)
			if e.Type == "d" {
				if err := walk(e.Path, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(p, 0); err != nil {
		return nil, err
	}
	return out, nil
}
