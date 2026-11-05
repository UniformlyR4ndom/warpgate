package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// localFS mediates wgcli's access to the local filesystem. With a working
// directory set (-root / WGCLI_ROOT) every path is confined to it through
// os.Root, which rejects paths that escape it. A working directory is
// required: without one, a local file operation fails, unless the caller
// opted out explicitly (-no-root / WGCLI_NO_ROOT), which runs unconfined.
// The goal is to keep an agent from touching files outside the directory it
// meant to use, not to withstand a hostile one.
type localFS struct {
	root    *os.Root // nil: unconfined (require says which kind)
	absRoot string   // cleaned absolute path of root, for translating absolute inputs
	require bool     // no root and no explicit opt-out: local file access must fail
}

// errNoRoot is returned by every local file operation when neither a working
// directory nor the explicit opt-out was given.
var errNoRoot = errors.New("local file access needs a working directory: set -root (WGCLI_ROOT), or -no-root (WGCLI_NO_ROOT) to run unconfined")

// openLocalFS builds the localFS from the -root / -no-root choice. A root
// must already exist; the two options are mutually exclusive.
func openLocalFS(dir string, noRoot bool) (*localFS, error) {
	if dir != "" && noRoot {
		return nil, errors.New("-root and -no-root are mutually exclusive")
	}
	if dir == "" {
		return &localFS{require: !noRoot}, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("opening working directory: %w", err)
	}
	return &localFS{root: root, absRoot: abs}, nil
}

// rel turns an input path into one relative to the root: an absolute path is
// made relative to absRoot, a relative one is cleaned; either way a path that
// leaves the root is refused. os.Root re-checks the result and fails closed if
// this translation is ever wrong.
func (l *localFS) rel(p string) (string, error) {
	var r string
	if filepath.IsAbs(p) {
		var err error
		if r, err = filepath.Rel(l.absRoot, filepath.Clean(p)); err != nil {
			return "", l.outside(p)
		}
	} else {
		r = filepath.Clean(p)
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", l.outside(p)
	}
	return r, nil
}

func (l *localFS) outside(p string) error {
	return fmt.Errorf("%s is outside the working directory (WGCLI_ROOT %s)", p, l.absRoot)
}

func (l *localFS) openFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	if l.require {
		return nil, errNoRoot
	}
	if l.root == nil {
		return os.OpenFile(name, flag, perm)
	}
	r, err := l.rel(name)
	if err != nil {
		return nil, err
	}
	return l.root.OpenFile(r, flag, perm)
}

// outputFile is written under a temporary name and replaces its target only
// on Commit, so the target holds either its previous content or the complete
// new one.
type outputFile struct {
	*os.File
	l        *localFS
	tmp, out string
	done     bool
}

// CreateOutput creates a new, uniquely named sibling of out to write into;
// it never opens an existing file.
func (l *localFS) CreateOutput(out string) (*outputFile, error) {
	for {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		tmp := out + "." + hex.EncodeToString(b[:]) + ".wgpart"
		f, err := l.openFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &outputFile{File: f, l: l, tmp: tmp, out: out}, nil
	}
}

// Commit closes the file and renames it over the target.
func (o *outputFile) Commit() error {
	if o.done {
		return errors.New("output file already committed or discarded")
	}
	o.done = true
	err := o.Close()
	if err == nil {
		err = o.l.Rename(o.tmp, o.out)
	}
	if err != nil {
		_ = o.l.Remove(o.tmp)
	}
	return err
}

// Discard closes and deletes the file unless it was committed.
func (o *outputFile) Discard() {
	if o.done {
		return
	}
	o.done = true
	o.Close()
	_ = o.l.Remove(o.tmp)
}

func (l *localFS) Open(name string) (*os.File, error) {
	if l.require {
		return nil, errNoRoot
	}
	if l.root == nil {
		return os.Open(name)
	}
	r, err := l.rel(name)
	if err != nil {
		return nil, err
	}
	return l.root.Open(r)
}

func (l *localFS) Stat(name string) (os.FileInfo, error) {
	if l.require {
		return nil, errNoRoot
	}
	if l.root == nil {
		return os.Stat(name)
	}
	r, err := l.rel(name)
	if err != nil {
		return nil, err
	}
	return l.root.Stat(r)
}

func (l *localFS) ReadFile(name string) ([]byte, error) {
	if l.require {
		return nil, errNoRoot
	}
	if l.root == nil {
		return os.ReadFile(name)
	}
	r, err := l.rel(name)
	if err != nil {
		return nil, err
	}
	return l.root.ReadFile(r)
}

func (l *localFS) MkdirAll(name string, perm os.FileMode) error {
	if l.require {
		return errNoRoot
	}
	if l.root == nil {
		return os.MkdirAll(name, perm)
	}
	r, err := l.rel(name)
	if err != nil {
		return err
	}
	return l.root.MkdirAll(r, perm)
}

func (l *localFS) Rename(oldPath, newPath string) error {
	if l.require {
		return errNoRoot
	}
	if l.root == nil {
		return os.Rename(oldPath, newPath)
	}
	oldRel, err := l.rel(oldPath)
	if err != nil {
		return err
	}
	newRel, err := l.rel(newPath)
	if err != nil {
		return err
	}
	return l.root.Rename(oldRel, newRel)
}

func (l *localFS) Remove(name string) error {
	if l.require {
		return errNoRoot
	}
	if l.root == nil {
		return os.Remove(name)
	}
	r, err := l.rel(name)
	if err != nil {
		return err
	}
	return l.root.Remove(r)
}

// WalkDir walks arg like filepath.WalkDir, but confined to the root. The paths
// passed to fn stay expressed under arg (not root-relative), so callers can
// reopen them through this same localFS and compute paths relative to arg.
func (l *localFS) WalkDir(arg string, fn fs.WalkDirFunc) error {
	if l.require {
		return errNoRoot
	}
	if l.root == nil {
		return filepath.WalkDir(arg, fn)
	}
	relArg, err := l.rel(arg)
	if err != nil {
		return err
	}
	fsRoot := filepath.ToSlash(relArg)
	return fs.WalkDir(l.root.FS(), fsRoot, func(fp string, d fs.DirEntry, err error) error {
		p := arg
		if fp != fsRoot {
			p = filepath.Join(arg, filepath.FromSlash(strings.TrimPrefix(fp, fsRoot+"/")))
		}
		return fn(p, d, err)
	})
}
