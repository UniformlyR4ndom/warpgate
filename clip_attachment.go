package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"warpgate/internal/transform"
)

// clipAttachmentMaxSize caps a single stored image.
const clipAttachmentMaxSize = 20 << 20 // 20 MiB

// clipAttachmentMaxBody caps the request carrying one: room for a
// clipAttachmentMaxSize image after html-b64's 4/3 expansion, or in a PDF,
// plus multipart framing.
const clipAttachmentMaxBody = clipAttachmentMaxSize/3*4 + 1<<20

// validImageMime accepts a well-formed "image/..." content type.
func validImageMime(s string) bool {
	return strings.HasPrefix(s, "image/") && validContentType(s)
}

// handleClipAttachmentNew stores one pasted image for a note. The multipart
// body mirrors /api/files/upload: an optional encrypted "transform" part
// (JSON {transform,args,note_id,mime,caption}) and a "file" part carrying the
// transform-encoded image bytes. The server reverses the transform and stores
// the raw bytes as a BLOB.
func (s *Server) handleClipAttachmentNew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !parseUploadForm(w, r, clipAttachmentMaxBody) {
		return
	}

	var spec struct {
		Transform string          `json:"transform"`
		Args      json.RawMessage `json:"args"`
		NoteID    int64           `json:"note_id"`
		Mime      string          `json:"mime"`
		Caption   string          `json:"caption"`
	}
	tf, _, err := r.FormFile("transform")
	if err != nil {
		http.Error(w, "missing transform part", http.StatusBadRequest)
		return
	}
	defer tf.Close()
	wasEncrypted, err := s.decodeParams(tf, &spec)
	if err != nil {
		s.writeParamError(w, err)
		return
	}
	if !validImageMime(spec.Mime) {
		http.Error(w, "invalid mime", http.StatusBadRequest)
		return
	}
	if !validClipTitle(spec.Caption) {
		http.Error(w, "invalid caption", http.StatusBadRequest)
		return
	}

	transformer, err := transform.New(spec.Transform, spec.Args)
	if err != nil {
		http.Error(w, "invalid transform: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	reader, err := transformer.Untransform(file)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data, err := io.ReadAll(io.LimitReader(reader, clipAttachmentMaxSize+1))
	if err != nil {
		http.Error(w, "invalid image data", http.StatusBadRequest)
		return
	}
	if len(data) > clipAttachmentMaxSize {
		http.Error(w, "image too large", http.StatusRequestEntityTooLarge)
		return
	}

	id, err := s.db.InsertAttachment(spec.NoteID, spec.Mime, spec.Caption, data)
	if err == errNoteNotFound {
		http.Error(w, "note not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	s.encodeParamsResponse(w, wasEncrypted, http.StatusOK,
		AttachmentMeta{ID: id, Mime: spec.Mime, Size: int64(len(data)), Caption: spec.Caption})
}

// handleClipAttachmentDownload returns one image's bytes, transform-disguised
// the same way /api/files/download disguises a file. The raw-bytes response
// is not param-crypted (the transform already covers it).
func (s *Server) handleClipAttachmentDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID          int64           `json:"id"`
		Transform   string          `json:"transform"`
		Args        json.RawMessage `json:"args"`
		ContentType string          `json:"contentType"`
	}
	if _, err := s.decodeParams(r.Body, &req); err != nil {
		s.writeParamError(w, err)
		return
	}
	transformer, err := transform.New(req.Transform, req.Args)
	if err != nil {
		http.Error(w, "invalid transform: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ContentType != "" && !validContentType(req.ContentType) {
		http.Error(w, "invalid contentType", http.StatusBadRequest)
		return
	}

	mime, data, err := s.db.GetAttachment(req.ID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		http.Error(w, "attachment not found", http.StatusNotFound)
		return
	}

	reader, err := transformer.Transform(bytes.NewReader(data))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", resolveContentType(transformer, req.ContentType, mime))
	if _, err := io.Copy(w, reader); err != nil {
		return
	}
}

func (s *Server) handleClipAttachmentEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID      int64  `json:"id"`
		Caption string `json:"caption"`
	}
	if _, err := s.decodeParams(r.Body, &req); err != nil {
		s.writeParamError(w, err)
		return
	}
	if !validClipTitle(req.Caption) {
		http.Error(w, "invalid caption", http.StatusBadRequest)
		return
	}
	found, err := s.db.UpdateAttachmentCaption(req.ID, req.Caption)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClipAttachmentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if _, err := s.decodeParams(r.Body, &req); err != nil {
		s.writeParamError(w, err)
		return
	}
	found, err := s.db.DeleteAttachment(req.ID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
