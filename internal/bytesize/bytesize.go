// Package bytesize parses byte counts like "4096", "512K" or "4M".
package bytesize

import (
	"fmt"
	"math"
	"strconv"
)

// Parse parses a byte count expressed as a plain integer (bytes) or an
// integer with a case-insensitive "K" (KiB) or "M" (MiB) suffix, e.g.
// "1024", "512K", "4m".
func Parse(s string) (int64, error) {
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
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("too large")
	}
	return n * mult, nil
}

// Flag is a flag.Value holding a byte count that accepts Parse's syntax.
type Flag struct{ N *int64 }

func (f Flag) String() string {
	if f.N == nil {
		return "0"
	}
	return strconv.FormatInt(*f.N, 10)
}

func (f Flag) Set(s string) error {
	n, err := Parse(s)
	if err != nil {
		return err
	}
	*f.N = n
	return nil
}
