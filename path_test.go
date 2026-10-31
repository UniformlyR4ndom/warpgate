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
		{ulTmpDirName, "", true},
		{"a/.Warpgate-TMP/b", "", true},
		{"a/" + ulTmpDirName + " .", "", true},
		{ulTmpDirName + "x", ulTmpDirName + "x", false},
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

func TestFileInsideDir(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	other := filepath.Join(base, "other")
	for _, d := range []string{filepath.Join(root, "sub"), other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "key.pem"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinks := true
	if err := os.Symlink(root, filepath.Join(base, "rootlink")); err != nil {
		symlinks = false
	}

	tests := []struct {
		file, dir string
		want      bool
		symlink   bool
	}{
		{filepath.Join(root, "sub", "key.pem"), root, true, false},
		{filepath.Join(root, "new.sqlite"), root, true, false},
		{filepath.Join(other, "new.sqlite"), root, false, false},
		{filepath.Join(base, "root-sibling.sqlite"), root, false, false},
		{filepath.Join(base, "rootlink", "sub", "key.pem"), root, true, true},
		{filepath.Join(root, "sub", "key.pem"), filepath.Join(base, "rootlink"), true, true},
		{filepath.Join(base, "rootlink", "fresh.sqlite"), root, true, true},
	}
	for _, tt := range tests {
		if tt.symlink && !symlinks {
			continue
		}
		got, err := fileInsideDir(tt.file, tt.dir)
		if err != nil || got != tt.want {
			t.Errorf("fileInsideDir(%q, %q) = %v, %v; want %v", tt.file, tt.dir, got, err, tt.want)
		}
	}
}
