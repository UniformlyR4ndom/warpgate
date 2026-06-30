package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type uploadResponse struct {
	Name string `json:"name"`
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}

	// Read optional transform spec from the "transform" file part.
	transformName := "id"
	var transformArgs json.RawMessage
	if tf, _, err := r.FormFile("transform"); err == nil {
		defer tf.Close()
		var spec struct {
			Transform string          `json:"transform"`
			Args      json.RawMessage `json:"args"`
		}
		if err := json.NewDecoder(tf).Decode(&spec); err != nil {
			http.Error(w, "invalid transform spec", http.StatusBadRequest)
			return
		}
		transformName = spec.Transform
		transformArgs = spec.Args
	}

	transformer, err := newTransformer(transformName, transformArgs)
	if err != nil {
		http.Error(w, "invalid transform: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Normalize separators so Windows client paths are handled correctly.
	rawName := strings.ReplaceAll(header.Filename, "\\", "/")
	filename := filepath.Base(rawName)
	if filename == "" || filename == "." || filename == "/" {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	reader, err := transformer.Untransform(file)
	if err != nil {
		log.Printf("error applying transform %q to upload %q: %v", transformName, filename, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	targetPath, err := makePath(s.absUlRoot, filename)
	if err != nil {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	f, err := os.Create(targetPath)
	if err != nil {
		log.Printf("error creating upload file %q: %v", targetPath, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	if _, err := io.Copy(f, reader); err != nil {
		log.Printf("error writing upload file %q: %v", targetPath, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	log.Printf("uploaded %q to %q", filename, targetPath)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(uploadResponse{Name: filename})
}

func (s *Server) handleUlList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	all, err := listDir(s.absUlRoot)
	if err != nil {
		log.Printf("error reading upload directory: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	files := make([]listEntry, 0, len(all))
	for _, e := range all {
		if e.Type == "f" {
			files = append(files, e)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}
