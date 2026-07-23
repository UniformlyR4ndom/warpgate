package main

import (
	"fmt"
	"io"
	"strconv"
)

// simMaxTotalSize is the largest synthetic file /api/sim/download will
// generate or /api/sim/upload will accept.
const simMaxTotalSize = 1024 * 1024 * 1024 // 1024 MiB

// validateSimTotalSize checks a client-requested synthetic file size against
// simMaxTotalSize and against the minimum size needed for the "<size> "
// header (see simReader) to fit within the file at all.
func validateSimTotalSize(totalSize int64) error {
	if totalSize <= 0 {
		return fmt.Errorf("totalSize must be positive")
	}
	if totalSize > simMaxTotalSize {
		return fmt.Errorf("totalSize must not exceed %d bytes", simMaxTotalSize)
	}
	minSize := int64(len(strconv.FormatInt(totalSize, 10))) + 1
	if totalSize < minSize {
		return fmt.Errorf("totalSize must be at least %d bytes to fit its own size header", minSize)
	}
	return nil
}

// simReader generates the synthetic file used by /api/sim/download and
// /api/sim/ws/download: the decimal totalSize, a single space, then 0xFF
// padding out to totalSize. It generates bytes for an arbitrary [start, …)
// window directly, without materializing the file, so ranged/chunked
// requests against a large totalSize stay cheap.
type simReader struct {
	header []byte
	pos    int64
	end    int64
}

func newSimReader(totalSize, start int64) *simReader {
	return &simReader{
		header: []byte(strconv.FormatInt(totalSize, 10) + " "),
		pos:    start,
		end:    totalSize,
	}
}

func (r *simReader) Read(p []byte) (int, error) {
	if r.pos >= r.end {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && r.pos < r.end {
		if r.pos < int64(len(r.header)) {
			c := copy(p[n:], r.header[r.pos:])
			n += c
			r.pos += int64(c)
			continue
		}
		remaining := r.end - r.pos
		c := int64(len(p) - n)
		if c > remaining {
			c = remaining
		}
		for i := int64(0); i < c; i++ {
			p[int64(n)+i] = 0xff
		}
		n += int(c)
		r.pos += c
	}
	return n, nil
}
