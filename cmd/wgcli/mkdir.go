package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// errDirExists reports a mkdir that failed only because the directory was
// already there (HTTP 409), so "mkdir -p" can treat it as success.
var errDirExists = errors.New("already exists")

func runMkdir(args []string) error {
	fs := flag.NewFlagSet("mkdir", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	parents := fs.Bool("p", false, "create missing parent directories, and don't fail if the directory exists (like mkdir -p)")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: wgcli mkdir [flags] <remote-dir>")
	}
	dir := fs.Arg(0)

	if *parents {
		created, err := mkdirParents(g, dir)
		if err != nil {
			return err
		}
		if len(created) == 0 {
			g.report(map[string]any{"path": dir, "created": created}, "%s already present", dir)
		} else {
			g.report(map[string]any{"path": dir, "created": created}, "created %s", strings.Join(created, ", "))
		}
		return nil
	}

	if err := mkdirRemote(g, dir); err != nil {
		if errors.Is(err, errDirExists) {
			return fmt.Errorf("%s already exists (use -p to ignore)", dir)
		}
		return err
	}
	g.report(map[string]any{"path": dir}, "created %s", dir)
	return nil
}

// mkdirParents creates dir and each missing ancestor, shallowest first;
// directories that already exist are skipped. It returns the ones it made.
// The server still enforces the depth limit and rejects escapes.
func mkdirParents(g *globalFlags, dir string) ([]string, error) {
	clean := path.Clean("/" + dir)[1:]
	if clean == "" || clean == "." {
		return nil, fmt.Errorf("path must name a directory below the upload root")
	}
	parts := strings.Split(clean, "/")
	var created []string
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		switch err := mkdirRemote(g, prefix); {
		case err == nil:
			created = append(created, prefix)
		case errors.Is(err, errDirExists):
		default:
			return created, fmt.Errorf("creating %s: %w", prefix, err)
		}
	}
	return created, nil
}

// mkdirRemote creates one directory in the upload directory, returning
// errDirExists when it was already there.
func mkdirRemote(g *globalFlags, dir string) error {
	body, err := g.encodeBody(map[string]string{"path": dir})
	if err != nil {
		return err
	}
	req, err := g.newRequest(http.MethodPost, "/api/ul/mkdir", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated:
		return nil
	case http.StatusConflict:
		return errDirExists
	default:
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
}
