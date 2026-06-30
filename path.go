package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// pathIsUnder reports whether path is equal to dir or directly inside it.
func pathIsUnder(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func makePath(root, path string) (string, error) {
	if strings.Contains(path, "../") || strings.Contains(path, `..\`) {
		return "", fmt.Errorf("invalid path")
	}

	result := filepath.Join(root, filepath.FromSlash(path))
	if !strings.HasPrefix(result, root) {
		return "", fmt.Errorf("invalid path")
	}

	return result, nil
}
