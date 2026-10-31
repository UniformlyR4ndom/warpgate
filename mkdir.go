package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
)

// ulMkdirMaxDepth bounds how deep below the upload root a directory may be
// created; deeper pre-existing directories remain usable.
const ulMkdirMaxDepth = 4

// maxDirNameLen is the common per-component limit (bytes) of Linux, macOS
// and Windows file systems.
const maxDirNameLen = 255

type mkdirRequest struct {
	Path string `json:"path"`
}

// validDirName rejects names that are unusable or ambiguous on some platform:
// control characters, backslashes, and the trailing dots/spaces Windows strips.
func validDirName(name string) bool {
	if name == "" || len(name) > maxDirNameLen || strings.TrimRight(name, ". ") != name {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '\\' {
			return false
		}
	}
	return true
}

// handleMkdir creates one directory below the upload root; its parent must
// already exist.
func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mkdirRequest
	if _, err := s.decodeParams(r.Body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusUnauthorized)
		return
	}

	rel, err := rootRelPath(req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if rel == "." {
		http.Error(w, "path must name a directory below the upload root", http.StatusBadRequest)
		return
	}
	if depth := len(strings.Split(rel, string(filepath.Separator))); depth > ulMkdirMaxDepth {
		http.Error(w, fmt.Sprintf("directories can be created at most %d levels deep", ulMkdirMaxDepth), http.StatusBadRequest)
		return
	}
	if !validDirName(filepath.Base(rel)) {
		http.Error(w, "invalid directory name", http.StatusBadRequest)
		return
	}
	if s.reachesUlTmp(s.ulRoot, filepath.Dir(rel)) {
		http.Error(w, errUlTmpPath, http.StatusForbidden)
		return
	}

	err = s.ulRoot.Mkdir(rel, 0o755)
	switch {
	case err == nil:
		log.Printf("created directory %q", filepath.Join(s.absUlRoot, rel))
		w.WriteHeader(http.StatusCreated)
	case os.IsExist(err):
		http.Error(w, "already exists", http.StatusConflict)
	case os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR):
		http.Error(w, "parent directory not found", http.StatusNotFound)
	case isPathEscape(err):
		http.Error(w, errSymlinkEscape, http.StatusForbidden)
	default:
		log.Printf("mkdir %q: %v", rel, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
