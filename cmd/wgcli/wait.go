package main

import (
	"cmp"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"syscall"
	"time"
)

const waitInterval = 2 * time.Second

// errWaitTimeout makes wgcli exit with status 2.
var errWaitTimeout = errors.New("timed out")

// pollUntil calls check every waitInterval until it reports done, it fails
// with an error that isn't transient, or timeout (0 = none) passes.
func pollUntil(timeout time.Duration, check func() (bool, error)) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		done, err := check()
		if err != nil && !isTransient(err) {
			return err
		}
		if done && err == nil {
			return nil
		}
		if !deadline.IsZero() && time.Now().Add(waitInterval).After(deadline) {
			return errWaitTimeout
		}
		time.Sleep(waitInterval)
	}
}

// isTransient reports errors a restarting or briefly unreachable server
// causes.
func isTransient(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func runClipWait(args []string) error {
	fs := flag.NewFlagSet("clip wait", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	since := fs.Int64("since", -1, "wait for notes created or edited after this revision (default: from now)")
	timeout := durationFlag(fs, "timeout", 2*time.Minute, "give up after this long, exit status 2 (0 = wait indefinitely)")
	search := fs.String("search", "", "only notes containing this (case-insensitive)")
	scope := fs.String("scope", "all", "where -search looks: all|title|content (content includes image captions)")
	anySource := fs.Bool("any-source", false, "also wake on notes written by wgcli (source cli)")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	if *scope != "all" && *scope != "title" && *scope != "content" {
		return fmt.Errorf("-scope must be all, title or content")
	}

	found, last, rev, err := pollClipReply(g, *since, *timeout, *search, *scope, *anySource)
	if errors.Is(err, errWaitTimeout) {
		if last != nil {
			last.Reset = false
			_ = printClipNotes(g, last, nil, false)
		}
		return fmt.Errorf("%w after %s; continue with -since %d", errWaitTimeout, *timeout, rev)
	}
	if err != nil {
		return err
	}
	slices.SortFunc(found, func(a, b clipNoteMeta) int {
		return cmp.Or(cmp.Compare(b.TS, a.TS), cmp.Compare(b.ID, a.ID))
	})
	return printClipNotes(g, last, found, false)
}

// pollClipReply waits from revision start (negative: from the clipboard's
// current revision) until a note is created or edited, skipping wgcli's own
// notes unless anySource and, with search, requiring a match in scope. It
// returns the matching notes, the last state seen (for a timeout printout)
// and the revision reached.
func pollClipReply(g *globalFlags, start int64, timeout time.Duration, search, scope string, anySource bool) ([]clipNoteMeta, *clipStateResponse, int64, error) {
	rev := start
	var last *clipStateResponse
	var found []clipNoteMeta
	err := pollUntil(timeout, func() (bool, error) {
		if rev < 0 {
			state, err := fetchClipState(g, 0)
			if err != nil {
				return false, err
			}
			rev, last = state.Rev, state
			return false, nil
		}
		state, err := fetchClipState(g, rev)
		if err != nil {
			return false, err
		}
		last = state
		if state.Reset {
			fmt.Fprintln(os.Stderr, "wgcli: the clipboard database was replaced; waiting from its current revision")
			rev = state.Rev
			return false, nil
		}
		for _, n := range state.Changed {
			if n.Source == clipSourceCLI && !anySource {
				continue
			}
			if search != "" {
				data, err := base64.StdEncoding.DecodeString(n.Data)
				if err != nil || !clipMatches(n, string(data), search, scope) {
					continue
				}
			}
			found = append(found, n)
		}
		rev = state.Rev
		return len(found) > 0, nil
	})
	return found, last, rev, err
}

// fileVersion identifies a version of a file well enough to notice a new
// one; the result's hash settles what actually arrived.
type fileVersion struct{ size, mtime int64 }

func versionOf(e listEntry) fileVersion {
	var v fileVersion
	if e.Size != nil {
		v.size = *e.Size
	}
	if e.MTime != nil {
		v.mtime = *e.MTime
	}
	return v
}

type waitFileOut struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
	SHA256 string `json:"sha256,omitempty"`
}

func runWaitFile(args []string) error {
	fs := flag.NewFlagSet("wait-file", flag.ContinueOnError)
	g := addGlobalFlags(fs)
	ul := fs.Bool("ul", false, "watch the upload directory instead of the download root")
	newOnly := fs.Bool("new", false, "for a file that exists already: wait for a newer version")
	since := fs.Int64("since", 0, "count files modified at or after this Unix time as new (the ts of \"clip new\")")
	timeout := durationFlag(fs, "timeout", 2*time.Minute, "give up after this long, exit status 2 (0 = wait indefinitely)")
	if err := parseFlags(fs, g, args); err != nil {
		return err
	}
	if *newOnly && *since > 0 {
		return errors.New("-new and -since exclude each other")
	}
	prefix := apiPrefix(*ul)
	target := path.Clean("/" + fs.Arg(0))[1:]

	// The parent's listing tells whether target is a file, a directory or
	// missing (then it is waited for as a file).
	dir, name := target, ""
	if target != "" {
		dir, name = path.Dir(target), path.Base(target)
		if dir == "." {
			dir = ""
		}
		parent, err := fetchList(g, prefix, dir)
		if err != nil {
			return err
		}
		for _, e := range parent {
			if e.Name == name && e.Type == "d" {
				dir, name = target, ""
			}
		}
	}

	var baseline map[string]fileVersion
	var found []listEntry
	err := pollUntil(*timeout, func() (bool, error) {
		entries, err := fetchList(g, prefix, dir)
		if err != nil {
			return false, err
		}
		current := map[string]fileVersion{}
		for _, e := range entries {
			if e.Type == "f" && (name == "" || e.Name == name) {
				current[e.Name] = versionOf(e)
			}
		}
		if *since > 0 {
			for n, v := range current {
				if v.mtime >= *since {
					found = append(found, filesNamed(entries, n)...)
				}
			}
			return len(found) > 0, nil
		}
		if baseline == nil {
			baseline = current
			// A file that exists already is the answer, unless -new asks
			// for a newer version.
			if _, ok := current[name]; name != "" && ok && !*newOnly {
				found = filesNamed(entries, name)
				return true, nil
			}
			return false, nil
		}
		for n, v := range current {
			if old, ok := baseline[n]; !ok || old != v {
				found = append(found, filesNamed(entries, n)...)
			}
		}
		return len(found) > 0, nil
	})
	if errors.Is(err, errWaitTimeout) {
		if g.json {
			printJSON([]waitFileOut{})
		}
		return fmt.Errorf("%w after %s", errWaitTimeout, *timeout)
	}
	if err != nil {
		return err
	}

	out := make([]waitFileOut, 0, len(found))
	for _, e := range found {
		p := path.Join(dir, e.Name)
		o := waitFileOut{Path: p, Size: versionOf(e).size, MTime: versionOf(e).mtime}
		if h, err := fetchFileEntry(g, prefix, p, true); err == nil {
			o.SHA256 = h.SHA256
		}
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b waitFileOut) int { return cmp.Compare(a.Path, b.Path) })
	if g.json {
		printJSON(out)
		return nil
	}
	for _, o := range out {
		fmt.Printf("%-40s %d %s\n", o.Path, o.Size, o.SHA256)
	}
	return nil
}

func filesNamed(entries []listEntry, name string) []listEntry {
	for _, e := range entries {
		if e.Name == name && e.Type == "f" {
			return []listEntry{e}
		}
	}
	return nil
}

// durationFlag defines a duration flag that also takes plain seconds.
func durationFlag(fs *flag.FlagSet, name string, def time.Duration, usage string) *time.Duration {
	d := def
	fs.Var((*secondsOrDuration)(&d), name, usage+" (e.g. 90s, 5m; a plain number is seconds)")
	return &d
}

type secondsOrDuration time.Duration

func (d *secondsOrDuration) String() string { return time.Duration(*d).String() }

func (d *secondsOrDuration) Set(s string) error {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*d = secondsOrDuration(time.Duration(n) * time.Second)
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return errors.New("not a duration")
	}
	*d = secondsOrDuration(v)
	return nil
}
