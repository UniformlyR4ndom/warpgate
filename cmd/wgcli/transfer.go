package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"warpgate/internal/filename"
	"warpgate/internal/transform"
)

// defaultDownloadChunkSize is the download loop's -chunk-size when no flag or
// WGCLI_CHUNK_SIZE overrides it; defaultWSFrameSize is the WS frame size used
// when none is requested.
const (
	defaultDownloadChunkSize = 4 << 20
	defaultWSFrameSize       = 1 << 20
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
	Path     string `json:"path"`
	Out      string `json:"out,omitempty"`
	Name     string `json:"name,omitempty"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	Verified bool   `json:"verified"` // the server's hash matched
}

// hashNote describes r's hash for text output.
func hashNote(r fileResult) string {
	if r.Verified {
		return "SHA-256 " + r.SHA256 + " verified"
	}
	return "SHA-256 " + r.SHA256 + " (not verified)"
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
	Transform   string       `json:"transform"`
	Seconds     float64      `json:"seconds"`
}

func newSummary(g *globalFlags, o xferOpts) *xferSummary {
	return &xferSummary{g: g, start: time.Now(), Transform: o.transformName,
		Transferred: []fileResult{}, Skipped: []xferNote{}, Failed: []xferNote{}}
}

func (s *xferSummary) done(label string, r fileResult) {
	s.Transferred = append(s.Transferred, r)
	s.Bytes += r.Bytes
	if !s.g.json {
		p := r.Path
		if dest := cmp.Or(r.Name, r.Out); dest != "" {
			p += " -> " + dest
		}
		fmt.Printf("%-12s %s (%d bytes, %s)\n", label, p, r.Bytes, hashNote(r))
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

// dropLocalCollisions fails every item whose local path is also the target
// of a different remote path (two downloads would overwrite each other) and
// returns the rest; -parents avoids the clash by keeping the directories.
func dropLocalCollisions(sum *xferSummary, items []dlItem) []dlItem {
	remotes := map[string]map[string]bool{}
	var order []string
	for _, it := range items {
		if remotes[it.local] == nil {
			remotes[it.local] = map[string]bool{}
			order = append(order, it.local)
		}
		remotes[it.local][it.remote] = true
	}
	dropped := map[string]bool{}
	for _, local := range order {
		if len(remotes[local]) < 2 {
			continue
		}
		dropped[local] = true
		names := make([]string, 0, len(remotes[local]))
		for r := range remotes[local] {
			names = append(names, r)
		}
		slices.Sort(names)
		for _, r := range names {
			sum.fail(r, fmt.Errorf("maps to the same local file %q as another path (use -parents to keep their directories)", local))
		}
	}
	if len(dropped) == 0 {
		return items
	}
	kept := make([]dlItem, 0, len(items))
	for _, it := range items {
		if !dropped[it.local] {
			kept = append(kept, it)
		}
	}
	return kept
}

// downloadMany puts each argument into outDir under its own name;
// directories need recursive. With parents, each file keeps its full remote
// directory path under outDir instead.
func downloadMany(g *globalFlags, o xferOpts, args []string, outDir string, recursive, force, parents bool) error {
	sum := newSummary(g, o)
	var dirs []string
	var items []dlItem

	// localFor maps a remote path to its local destination: the full remote
	// path under outDir with -parents, else just the base name.
	localFor := func(remote string) string {
		if parents {
			return filepath.Join(outDir, filepath.FromSlash(remote))
		}
		return filepath.Join(outDir, path.Base(remote))
	}

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
		local := localFor(remote)
		switch {
		case e.Type == "f":
			items = append(items, dlItem{remote, local, e})
		case !recursive:
			sum.fail(remote, errors.New("is a directory (use -r)"))
		default:
			walk(remote, local, 0)
		}
	}

	items = dropLocalCollisions(sum, items)

	for _, it := range items {
		dirs = append(dirs, filepath.Dir(it.local))
	}
	for _, d := range dirs {
		if err := g.local.MkdirAll(d, 0o755); err != nil {
			sum.fail(d, err)
		}
	}
	for _, it := range items {
		entry := &it.entry
		if !force {
			if fi, err := g.local.Stat(it.local); err == nil && fi.Mode().IsRegular() && fi.Size() == *it.entry.Size {
				remote, err := fetchFileEntry(g, o.prefix, it.remote, true)
				if err != nil {
					sum.fail(it.remote, err)
					continue
				}
				if local, err := hashLocal(g.local, it.local); err == nil && local == remote.SHA256 {
					_ = g.local.Remove(it.local + ".wgpart") // left by a killed earlier run
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

	sum := newSummary(g, o)
	var dirs []string // remote directories, parents first
	var items []ulItem
	for _, arg := range args {
		fi, err := g.local.Stat(arg)
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
			err := g.local.WalkDir(arg, func(p string, d fs.DirEntry, err error) error {
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
				fi, err := g.local.Stat(p) // follows symlinks
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
					return fmt.Errorf("listing the upload target %q: %w (if it does not exist, pass -p to create it, or run mkdir -p first)", "/"+remoteDir, err)
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
					if local, err := hashLocal(g.local, it.local); err == nil && local == remote.SHA256 {
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

func hashLocal(l *localFS, p string) (string, error) {
	f, err := l.Open(p)
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
