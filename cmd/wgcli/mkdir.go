package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func runMkdir(args []string) error {
	fs := flag.NewFlagSet("mkdir", flag.ExitOnError)
	g := addGlobalFlags(fs)
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: wgcli mkdir [flags] <remote-dir>")
	}
	dir := fs.Arg(0)
	if err := mkdirRemote(g, dir); err != nil {
		return err
	}
	g.report(map[string]any{"path": dir}, "created %s", dir)
	return nil
}

// mkdirRemote creates one directory in the upload directory.
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
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}
