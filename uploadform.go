package main

import (
	"errors"
	"io"
	"net/http"
	"time"
)

// ulBodyIdleTimeout is how long an upload body may stall before the client is
// presumed gone. A slow but progressing upload never hits it.
var ulBodyIdleTimeout = 300 * time.Second

// idleDeadlineBody extends the connection's read deadline before every read.
type idleDeadlineBody struct {
	io.ReadCloser
	rc *http.ResponseController
}

func (b idleDeadlineBody) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(time.Now().Add(ulBodyIdleTimeout))
	return b.ReadCloser.Read(p)
}

// parseUploadForm parses a multipart body of at most limit bytes (0: no limit),
// failing one that stalls for ulBodyIdleTimeout. On failure it has written the
// response: 413 for an oversized body, else 400.
func parseUploadForm(w http.ResponseWriter, r *http.Request, limit int64) bool {
	r.Body = idleDeadlineBody{ReadCloser: r.Body, rc: http.NewResponseController(w)}
	if limit > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid multipart form", http.StatusBadRequest)
		}
		return false
	}
	return true
}
