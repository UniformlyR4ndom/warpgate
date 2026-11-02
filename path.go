package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"warpgate/internal/filename"
)

var (
	errInvalidPath  = errors.New("invalid path")
	errReservedName = errors.New("path contains the reserved name " + ulTmpDirName)
)

// isReservedName reports whether a single path segment names the upload temp
// directory, ignoring case and the trailing dots/spaces Windows strips.
func isReservedName(name string) bool {
	return strings.EqualFold(strings.TrimRight(name, ". "), ulTmpDirName)
}

// validName reports whether name can be created as a single file or
// directory name on this platform and isn't the reserved temp directory name.
func validName(name string) bool {
	return filename.Valid(name) && !isReservedName(name)
}

// rootRelPath turns a client-supplied slash-separated path into a cleaned
// path relative to a root ("." for the root itself), rejecting traversal and
// any segment naming the upload temp directory.
func rootRelPath(path string) (string, error) {
	if strings.Contains(path, "../") || strings.Contains(path, `..\`) {
		return "", errInvalidPath
	}
	rel := filepath.Clean(strings.TrimLeft(filepath.FromSlash(path), `/\`))
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || rel == ".." {
		return "", errInvalidPath
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if isReservedName(seg) {
			return "", errReservedName
		}
	}
	return rel, nil
}

const errSymlinkEscape = "symlink points outside the root"

// isPathEscape reports whether err is an os.Root refusal to leave the root,
// e.g. via a symlink. The os package has no exported sentinel for it.
func isPathEscape(err error) bool {
	var pe *fs.PathError
	return errors.As(err, &pe) && pe.Err.Error() == "path escapes from parent"
}

// fileInsideDir reports whether file (which need not exist) lies inside dir,
// comparing directory identity rather than path strings, so symlinks and bind
// mounts on either side are seen through. Hard links are not detected.
func fileInsideDir(file, dir string) (bool, error) {
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	p, err := filepath.Abs(file)
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	} else if resolved, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		p = filepath.Join(resolved, filepath.Base(p))
	}
	for {
		if fi, err := os.Stat(p); err == nil && os.SameFile(fi, dirInfo) {
			return true, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false, nil
		}
		p = parent
	}
}
