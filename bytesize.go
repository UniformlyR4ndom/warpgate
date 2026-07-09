package main

import (
	"fmt"
	"strconv"
)

// parseByteSize parses a byte count expressed as a plain integer (bytes) or
// an integer with a case-insensitive "K" (KiB) or "M" (MiB) suffix, e.g.
// "1024", "512K", "4m".
func parseByteSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}

	mult := int64(1)
	numPart := s
	switch s[len(s)-1] {
	case 'k', 'K':
		mult = 1024
		numPart = s[:len(s)-1]
	case 'm', 'M':
		mult = 1024 * 1024
		numPart = s[:len(s)-1]
	}

	n, err := strconv.ParseInt(numPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", numPart)
	}
	if n < 0 {
		return 0, fmt.Errorf("must not be negative")
	}
	return n * mult, nil
}
