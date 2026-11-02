// Package filename checks single file and directory names.
package filename

import (
	"runtime"
	"strings"
	"unicode"
)

// MaxLen is the common per-component limit (bytes) of Linux, macOS and
// Windows file systems.
const MaxLen = 255

// Valid reports whether name can be created as a single file or directory
// name on this platform without being reinterpreted.
func Valid(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > MaxLen {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return runtime.GOOS != "windows" || ValidWindows(name)
}

// ValidWindows rejects what Windows would reinterpret: drive/stream
// separators and wildcards, the trailing dots and spaces it strips, and
// device names (with or without an extension).
func ValidWindows(name string) bool {
	if strings.ContainsAny(name, `:*?"<>|`) || strings.TrimRight(name, ". ") != name {
		return false
	}
	base, _, _ := strings.Cut(strings.ToUpper(name), ".")
	base = strings.TrimRight(base, " ")
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
		return false
	}
	return true
}
