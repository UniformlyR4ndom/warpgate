package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"warpgate/internal/filename"
	"warpgate/internal/transform"
)

// xferOpts are the settings every file of a transfer shares.
type xferOpts struct {
	prefix        string // /api/files/ or /api/ul/ (downloads)
	transformName string
	transformArgs json.RawMessage
	transformer   transform.Transformer
	chunkSize     int64
	frameSize     int64
	ws            bool
	verify        bool
}

type fileResult struct {
	Path   string `json:"path"`
	Out    string `json:"out,omitempty"`
	Name   string `json:"name,omitempty"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type xferNote struct {
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// xferSummary collects the outcome of a multi-file transfer; text output
// reports each file as it happens.
type xferSummary struct {
	g           *globalFlags
	start       time.Time
	Transferred []fileResult `json:"transferred"`
	Skipped     []xferNote   `json:"skipped"`
	Failed      []xferNote   `json:"failed"`
	Bytes       int64        `json:"bytes"`
	Seconds     float64      `json:"seconds"`
}

func newSummary(g *globalFlags) *xferSummary {
	return &xferSummary{g: g, start: time.Now(), Transferred: []fileResult{}, Skipped: []xferNote{}, Failed: []xferNote{}}
}

func (s *xferSummary) done(label string, r fileResult) {
	s.Transferred = append(s.Transferred, r)
	s.Bytes += r.Bytes
	if !s.g.json {
		fmt.Printf("%-12s %s (%d bytes)\n", label, r.Path, r.Bytes)
	}
}

func (s *xferSummary) skip(p, reason string) {
	s.Skipped = append(s.Skipped, xferNote{Path: p, Reason: reason})
	if !s.g.json {
		fmt.Printf("%-12s %s (%s)\n", "skipped", p, reason)
	}
}

func (s *xferSummary) fail(p string, err error) {
	s.Failed = append(s.Failed, xferNote{Path: p, Error: err.Error()})
	if !s.g.json {
		fmt.Printf("%-12s %s: %v\n", "FAILED", p, err)
	}
}

// finish prints the summary; it fails if any file did.
func (s *xferSummary) finish() error {
	s.Seconds = time.Since(s.start).Seconds()
	if s.g.json {
		printJSON(s)
	} else {
		fmt.Printf("%d transferred, %d skipped, %d failed: %d bytes in %.1f s\n",
			len(s.Transferred), len(s.Skipped), len(s.Failed), s.Bytes, s.Seconds)
	}
	if n := len(s.Failed); n > 0 {
		return fmt.Errorf("%d of %d files failed", n, n+len(s.Transferred)+len(s.Skipped))
	}
	return nil
}

// maxRemoteDepth bounds the download walk: a symlink to a parent directory
// on the server lists as a directory, so a tree can be endless.
const maxRemoteDepth = 32

type dlItem struct {
	remote, local string
	entry         listEntry
}

// downloadMany puts each argument into outDir under its own name;
// directories need recursive.
func downloadMany(g *globalFlags, o xferOpts, args []string, outDir string, recursive, force bool) error {
	sum := newSummary(g)
	var dirs []string
	var items []dlItem

	var walk func(remoteDir, localDir string, depth int)
	walk = func(remoteDir, localDir string, depth int) {
		if depth > maxRemoteDepth {
			sum.fail(remoteDir, fmt.Errorf("more than %d directory levels (a symlink loop on the server?)", maxRemoteDepth))
			return
		}
		entries, err := fetchList(g, o.prefix, remoteDir)
		if err != nil {
			sum.fail(remoteDir, err)
			return
		}
		dirs = append(dirs, localDir)
		for _, e := range entries {
			p := path.Join(remoteDir, e.Name)
			if !filename.Valid(e.Name) {
				sum.fail(p, errors.New("name not valid on this system"))
				continue
			}
			if e.Type == "d" {
				walk(p, filepath.Join(localDir, e.Name), depth+1)
			} else if e.Size != nil {
				items = append(items, dlItem{p, filepath.Join(localDir, e.Name), e})
			}
		}
	}

	for _, arg := range args {
		remote := path.Clean("/" + arg)[1:]
		if remote == "" {
			if !recursive {
				sum.fail("/", errors.New("is a directory (use -r)"))
				continue
			}
			walk("", outDir, 0)
			continue
		}
		e, err := lookupRemote(g, o.prefix, remote)
		if err != nil {
			sum.fail(remote, err)
			continue
		}
		local := filepath.Join(outDir, path.Base(remote))
		switch {
		case e.Type == "f":
			items = append(items, dlItem{remote, local, e})
		case !recursive:
			sum.fail(remote, errors.New("is a directory (use -r)"))
		default:
			walk(remote, local, 0)
		}
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			sum.fail(d, err)
		}
	}
	for _, it := range items {
		entry := &it.entry
		if !force {
			if fi, err := os.Stat(it.local); err == nil && fi.Mode().IsRegular() && fi.Size() == *it.entry.Size {
				remote, err := fetchFileEntry(g, o.prefix, it.remote, true)
				if err != nil {
					sum.fail(it.remote, err)
					continue
				}
				if local, err := hashLocal(it.local); err == nil && local == remote.SHA256 {
					_ = os.Remove(it.local + ".wgpart") // left by a killed earlier run
					sum.skip(it.remote, "unchanged")
					continue
				}
				entry = &remote
			}
		}
		r, err := downloadFile(g, o, it.remote, it.local, entry)
		if err != nil {
			sum.fail(it.remote, err)
			continue
		}
		sum.done("downloaded", r)
	}
	return sum.finish()
}

// lookupRemote finds p's entry in its parent's listing.
func lookupRemote(g *globalFlags, prefix, p string) (listEntry, error) {
	dir := path.Dir(p)
	if dir == "." {
		dir = ""
	}
	entries, err := fetchList(g, prefix, dir)
	if err != nil {
		return listEntry{}, err
	}
	for _, e := range entries {
		if e.Name == path.Base(p) {
			return e, nil
		}
	}
	return listEntry{}, errors.New("not found")
}

// maxUlCreateDepth mirrors the server's limit on how deep below the upload
// root a directory may be created.
const maxUlCreateDepth = 4

type ulItem struct {
	local, dir, name string
	size             int64
}

// uploadMany puts each argument into remoteDir under its own name;
// directories need recursive.
func uploadMany(g *globalFlags, o xferOpts, args []string, remoteDir string, recursive, force bool) error {
	settings, err := g.fetchSettings()
	if err != nil {
		return err
	}
	if !settings.Uploads {
		return errors.New("the server has uploads disabled")
	}
	remoteDir = path.Clean("/" + remoteDir)[1:]
	if settings.UlFlat && remoteDir != "" {
		return errors.New("the server keeps the upload directory flat: no -dir")
	}

	sum := newSummary(g)
	var dirs []string // remote directories, parents first
	var items []ulItem
	for _, arg := range args {
		fi, err := os.Stat(arg)
		if err != nil {
			sum.fail(arg, err)
			continue
		}
		base := filepath.Base(filepath.Clean(arg))
		switch {
		case fi.Mode().IsRegular():
			items = append(items, ulItem{arg, remoteDir, base, fi.Size()})
		case !fi.IsDir():
			sum.skip(arg, "not a regular file")
		case !recursive:
			sum.fail(arg, errors.New("is a directory (use -r)"))
		case settings.UlFlat:
			sum.fail(arg, errors.New("the server keeps the upload directory flat: no directories"))
		default:
			top := path.Join(remoteDir, base)
			dirs = append(dirs, top)
			err := filepath.WalkDir(arg, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					sum.fail(p, err)
					return nil
				}
				rel, _ := filepath.Rel(arg, p)
				if rel == "." {
					return nil
				}
				remote := path.Join(top, filepath.ToSlash(rel))
				if d.IsDir() {
					dirs = append(dirs, remote)
					return nil
				}
				fi, err := os.Stat(p) // follows symlinks
				switch {
				case err != nil:
					sum.fail(p, err)
				case fi.Mode().IsRegular():
					items = append(items, ulItem{p, path.Dir(remote), d.Name(), fi.Size()})
				case fi.IsDir():
					sum.skip(p, "symlink to a directory")
				default:
					sum.skip(p, "not a regular file")
				}
				return nil
			})
			if err != nil {
				sum.fail(arg, err)
			}
		}
	}

	// Which directories exist decides what must be created (within the
	// server's depth limit) and which files may be unchanged.
	remoteEntries := map[string]map[string]listEntry{}
	exists := func(d string) bool { _, ok := remoteEntries[d]; return ok }
	var create []string
	if !settings.UlFlat {
		for _, d := range append([]string{remoteDir}, dirs...) {
			if d != remoteDir && !exists(path.Dir(d)) && path.Dir(d) != "." {
				create = append(create, d)
				continue
			}
			entries, err := fetchList(g, "/api/ul/", d)
			if err != nil {
				if d == remoteDir {
					return fmt.Errorf("listing the upload target %q: %w", "/"+remoteDir, err)
				}
				create = append(create, d)
				continue
			}
			m := map[string]listEntry{}
			for _, e := range entries {
				m[e.Name] = e
			}
			remoteEntries[d] = m
		}
	}
	for _, d := range create {
		if n := strings.Count(d, "/") + 1; n > maxUlCreateDepth {
			return fmt.Errorf("would create %q, %d levels below the upload root; the server creates at most %d", d, n, maxUlCreateDepth)
		}
	}

	failedDirs := map[string]error{}
	for _, d := range create {
		if err := failedParent(failedDirs, d); err != nil {
			failedDirs[d] = err
			continue
		}
		if err := mkdirRemote(g, d); err != nil {
			failedDirs[d] = fmt.Errorf("creating directory %q: %w", d, err)
		}
	}
	for _, it := range items {
		p := path.Join(it.dir, it.name)
		if err := failedParent(failedDirs, p); err != nil {
			sum.fail(it.local, err)
			continue
		}
		if !force {
			if e, ok := remoteEntries[it.dir][it.name]; ok && e.Type == "f" && e.Size != nil && *e.Size == it.size {
				remote, err := fetchFileEntry(g, "/api/ul/", p, true)
				if err == nil {
					if local, err := hashLocal(it.local); err == nil && local == remote.SHA256 {
						sum.skip(it.local, "unchanged")
						continue
					}
				}
			}
		}
		r, err := uploadFile(g, o, it.local, it.dir, it.name)
		if err != nil {
			sum.fail(it.local, err)
			continue
		}
		sum.done("uploaded", r)
	}
	return sum.finish()
}

// failedParent returns the error of a failed directory p lies in.
func failedParent(failed map[string]error, p string) error {
	for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
		if err, ok := failed[d]; ok {
			return err
		}
	}
	return nil
}

func hashLocal(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
