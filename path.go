package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// pathIsUnder reports whether path is equal to dir or directly inside it.
func pathIsUnder(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// rootRelPath turns a client-supplied slash-separated path into a cleaned
// path relative to a root ("." for the root itself), rejecting traversal.
func rootRelPath(path string) (string, error) {
	if strings.Contains(path, "../") || strings.Contains(path, `..\`) {
		return "", fmt.Errorf("invalid path")
	}
	rel := filepath.Clean(strings.TrimLeft(filepath.FromSlash(path), `/\`))
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || rel == ".." {
		return "", fmt.Errorf("invalid path")
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
