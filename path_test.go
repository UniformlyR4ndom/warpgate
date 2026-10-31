package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootRelPath(t *testing.T) {
	tests := []struct {
		path    string
		want    string
		wantErr bool
	}{
		{"", ".", false},
		{"/", ".", false},
		{"foo.txt", "foo.txt", false},
		{"/foo.txt", "foo.txt", false},
		{"sub/dir/file.txt", filepath.FromSlash("sub/dir/file.txt"), false},
		{"..", "", true},
		{"../secret", "", true},
		{"foo/../../../etc/passwd", "", true},
		{`foo\..\bar`, "", true},
		{`..\\evil`, "", true},
	}

	for _, tt := range tests {
		got, err := rootRelPath(tt.path)
		if tt.wantErr {
			if err == nil {
				t.Errorf("rootRelPath(%q): expected error, got %q", tt.path, got)
			}
		} else if err != nil {
			t.Errorf("rootRelPath(%q): unexpected error: %v", tt.path, err)
		} else if got != tt.want {
			t.Errorf("rootRelPath(%q): got %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestIsPathEscape pins the os.Root error text isPathEscape matches.
func TestIsPathEscape(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "root"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(dir, "root", "up")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(filepath.Join(dir, "root"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if _, err := root.Open("up"); !isPathEscape(err) {
		t.Fatalf("escaping symlink: isPathEscape(%v) = false", err)
	}
	if _, err := root.Open("missing"); isPathEscape(err) {
		t.Fatalf("missing file: isPathEscape(%v) = true", err)
	}
}
