// wgcli is a command-line client for the warpgate API, for scripts, agents
// and testing a running server without a browser.
package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
)

//go:embed guide.md
var guide string

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	all, err := hoistGlobalFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "wgcli:", err)
		os.Exit(1)
	}
	if len(all) == 0 {
		usage()
		os.Exit(1)
	}
	cmd := all[0]
	args := all[1:]

	switch cmd {
	case "status":
		err = runStatus(args)
	case "list":
		err = runList(args)
	case "ul-list":
		err = runUlList(args)
	case "download":
		err = runDownload(args)
	case "upload":
		err = runUpload(args)
	case "mkdir":
		err = runMkdir(args)
	case "clip":
		err = runClip(args)
	case "sim":
		err = runSim(args)
	case "register":
		err = runRegister(args)
	case "derive-key":
		err = runDeriveKey(args)
	case "connect-info":
		err = runConnectInfo(args)
	case "wait-file":
		err = runWaitFile(args)
	case "guide":
		fmt.Print(guide)
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "wgcli: unknown command %q\n\n", cmd)
		usage()
		os.Exit(1)
	}

	if errors.Is(err, errHelpShown) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "wgcli:", err)
		// 2 means only "timed out": flag errors exit with 1 like any other.
		if errors.Is(err, errWaitTimeout) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// hoistGlobalFlags moves global flags given before the command to just after
// it (after the subcommand for clip and sim), where the command parses them.
func hoistGlobalFlags(args []string) ([]string, error) {
	globals := flag.NewFlagSet("wgcli", flag.ContinueOnError)
	addGlobalFlags(globals)
	var lead []string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' || a == "--" || a == "-h" || a == "--help" {
			break
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := globals.Lookup(name)
		if f == nil {
			return nil, fmt.Errorf("%s before the command is not a global flag; command flags go after the command, e.g. \"wgcli download %s …\"", a, a)
		}
		lead = append(lead, a)
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); hasValue || (ok && bf.IsBoolFlag()) {
			continue
		}
		if i+1 == len(args) {
			return nil, fmt.Errorf("flag needs an argument: %s", a)
		}
		i++
		lead = append(lead, args[i])
	}
	rest := args[i:]
	if len(lead) == 0 || len(rest) == 0 {
		return rest, nil
	}
	at := 1
	if (rest[0] == "clip" || rest[0] == "sim") && len(rest) > 1 {
		at = 2
	}
	return slices.Concat(rest[:at], lead, rest[at:]), nil
}

func usage() {
	fmt.Fprint(os.Stderr, usageText)
}

const usageText = `wgcli - command-line client for the warpgate API

Usage:
  wgcli [global flags] <command> [flags] [args...]

Flags may come before or after positional arguments; "--" ends flag
parsing (for an argument starting with "-"). Only global flags may
precede the command.

Global flags fall back to environment variables when not given:
  WGCLI_URL, WGCLI_AUTH_KEY, WGCLI_AUTH_K, WGCLI_INSECURE, WGCLI_PARAM_CRYPT,
  WGCLI_CERT, WGCLI_CERT_KEY, WGCLI_JSON, WGCLI_CONNECT, WGCLI_HOST, WGCLI_ROOT,
  WGCLI_NO_ROOT
-backend, -frame-size, -chunk-size and -transform default to WGCLI_BACKEND,
WGCLI_FRAME_SIZE, WGCLI_CHUNK_SIZE and WGCLI_TRANSFORM in every command.
Transform keys/IVs left empty are generated randomly per invocation.

Commands:
  status    [flags]                            check the connection: reachable (registers if blocked), uploads, key
  list      [flags] [remote-dir]              list files/directories (-ul: in the upload directory)
  ul-list   [flags]                            list the top level of the upload directory (works with -ul-flat)
  download  [flags] <remote-path>              download, reversing the transform (-ul: from the upload directory)
  upload    [flags] <local-file>                upload, applying the transform (-dir: into a subdirectory)
  mkdir     [flags] <remote-dir>                create a directory in the upload directory (-p: with parents)
  clip      list|get|new|ask|edit|delete|attach|get-attachment|delete-attachment|caption|wait
            manage clipboard notes/images (newest first; "ask" posts a note and waits for a reply)
  wait-file [flags] [path]                      wait for a file, or a new file in a directory (exit 2 on timeout)
  sim       download|upload [flags]             probe transport via a synthetic file/buffer (no filesystem I/O, no transform)
  register  [flags] [registration-url]          register this client's IP with the IP firewall (default URL: -url)
  derive-key [flags]                            print the key derived from -auth-key (for -auth-k / WGCLI_AUTH_K)
  connect-info [token]                          decode a connect token (default: WGCLI_CONNECT)
  guide                                         full usage guide for scripts and agents (Markdown, stdout)

Run "wgcli <command> -h" for command-specific flags.
`
