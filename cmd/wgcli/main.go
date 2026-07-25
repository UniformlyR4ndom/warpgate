// wgcli is a command-line client for the warpgate API, mainly intended for
// exercising/testing a running server without a browser.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "list":
		err = runList(args)
	case "ul-list":
		err = runUlList(args)
	case "download":
		err = runDownload(args)
	case "upload":
		err = runUpload(args)
	case "clip":
		err = runClip(args)
	case "sim":
		err = runSim(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "wgcli: unknown command %q\n\n", cmd)
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "wgcli:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `wgcli - command-line client for the warpgate API

Usage:
  wgcli <command> [flags] [args...]

Note: flags must come before positional arguments (Go's flag package stops
parsing at the first non-flag argument).

Commands:
  list      [flags] [remote-dir]              list files/directories
  ul-list   [flags]                            list the upload directory
  download  [flags] <remote-path>              download, reversing the transform
  upload    [flags] <local-file>                upload, applying the transform
  clip      list|new|edit|delete               manage clipboard notes
  sim       download|upload [flags]             probe transport via a synthetic file/buffer (no filesystem I/O, no transform)

Run "wgcli <command> -h" for command-specific flags.
`)
}
