package main

import (
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

type listRequest struct {
	Path string `json:"path"`
}

type listEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  *int64 `json:"size,omitempty"`
	MTime *int64 `json:"mtime,omitempty"`
}

// listDir lists the directory rel inside root. Symlinks are described by
// their target; one that is unusable through root is left out and logged.
func listDir(root *os.Root, rel string) ([]listEntry, error) {
	d, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return nil, err
	}
	result := make([]listEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if e.Type()&fs.ModeSymlink != 0 {
			info, err = root.Stat(filepath.Join(rel, e.Name()))
			if err != nil {
				reason := "broken symlink"
				if isPathEscape(err) {
					reason = errSymlinkEscape
				}
				log.Printf("list: hiding %s: %s", filepath.Join(root.Name(), rel, e.Name()), reason)
				continue
			}
		}
		entry := listEntry{Name: e.Name(), Type: "f"}
		if err == nil {
			if info.IsDir() {
				entry.Type = "d"
			}
			mtime := info.ModTime().Unix()
			entry.MTime = &mtime
			if !info.IsDir() {
				size := info.Size()
				entry.Size = &size
			}
		}
		result = append(result, entry)
	}
	return result, nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req listRequest
	wasEncrypted, err := s.decodeParams(r.Body, &req)
	if err != nil {
		http.Error(w, "invalid request body", http.StatusUnauthorized)
		return
	}

	rel, err := rootRelPath(req.Path)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	target := filepath.Join(s.absRoot, rel)
	if s.isUlTmpPath(target) {
		http.Error(w, "path not found", http.StatusNotFound)
		return
	}

	all, err := listDir(s.root, rel)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "path not found", http.StatusNotFound)
		} else if isPathEscape(err) {
			http.Error(w, errSymlinkEscape, http.StatusForbidden)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	entries := make([]listEntry, 0, len(all))
	for _, e := range all {
		if !s.isUlTmpPath(filepath.Join(target, e.Name)) {
			entries = append(entries, e)
		}
	}

	s.encodeParamsResponse(w, wasEncrypted, http.StatusOK, entries)
}
