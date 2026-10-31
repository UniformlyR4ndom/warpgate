package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListDirMTime(t *testing.T) {
	root := t.TempDir()
	want := time.Unix(1700000000, 0)
	f := filepath.Join(root, "a.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f, want, want); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	entries, err := listDir(r, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.MTime == nil {
			t.Fatalf("%s: mtime missing", e.Name)
		}
		if e.Name == "a.txt" && *e.MTime != want.Unix() {
			t.Errorf("a.txt: mtime = %d, want %d", *e.MTime, want.Unix())
		}
		if e.Name == "sub" && e.Size != nil {
			t.Errorf("sub: size should be omitted")
		}
	}
}
