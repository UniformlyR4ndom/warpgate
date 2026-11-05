package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLocalFSRel(t *testing.T) {
	dir := t.TempDir()
	l, err := openLocalFS(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	ok := []struct{ in, want string }{
		{"a/b", filepath.Join("a", "b")},
		{"./a/../a/b", filepath.Join("a", "b")},
		{filepath.Join(dir, "a/b"), filepath.Join("a", "b")},
		{dir, "."},
	}
	for _, c := range ok {
		got, err := l.rel(c.in)
		if err != nil || got != c.want {
			t.Errorf("rel(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"../x", "a/../../x", "/etc/passwd", filepath.Dir(dir)} {
		if got, err := l.rel(in); err == nil {
			t.Errorf("rel(%q) = %q, nil; want an error", in, got)
		}
	}
}

func TestLocalFSConfine(t *testing.T) {
	dir := t.TempDir()
	l, err := openLocalFS(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.MkdirAll("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := l.CreateOutput("sub/f")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("hi"); err != nil {
		t.Fatal(err)
	}
	if err := f.Commit(); err != nil {
		t.Fatal(err)
	}
	if b, err := l.ReadFile("sub/f"); err != nil || string(b) != "hi" {
		t.Errorf("ReadFile = %q, %v", b, err)
	}

	// A path escaping the root is refused, directly or through a symlink.
	if _, err := l.CreateOutput(filepath.Join(filepath.Dir(dir), "evil")); err == nil {
		t.Error("CreateOutput outside the root succeeded")
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Open("link"); err == nil {
		t.Error("Open through an escaping symlink succeeded")
	}
}

func TestLocalFSWalkDir(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"top/a.txt", "top/sub/b.txt"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l, err := openLocalFS(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	// Walk using an absolute arg; paths must come back expressed under it.
	arg := filepath.Join(dir, "top")
	var files []string
	err = l.WalkDir(arg, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(arg, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	if want := []string{"a.txt", "sub/b.txt"}; !slices.Equal(files, want) {
		t.Errorf("walk files = %v; want %v", files, want)
	}
}

func TestLocalFSUnconfined(t *testing.T) {
	l, err := openLocalFS("", true)
	if err != nil || l.root != nil || l.require {
		t.Fatalf("openLocalFS(\"\", true) = %+v, %v", l, err)
	}
	dir := t.TempDir()
	if _, err := l.Stat(dir); err != nil {
		t.Errorf("unconfined Stat of an absolute path: %v", err)
	}
}

func TestLocalFSRequireRoot(t *testing.T) {
	l, err := openLocalFS("", false)
	if err != nil || !l.require {
		t.Fatalf("openLocalFS(\"\", false) = %+v, %v", l, err)
	}
	dir := t.TempDir()
	if _, err := l.Stat(dir); !errors.Is(err, errNoRoot) {
		t.Errorf("Stat without a root = %v; want errNoRoot", err)
	}
	if err := l.MkdirAll("x", 0o755); !errors.Is(err, errNoRoot) {
		t.Errorf("MkdirAll without a root = %v; want errNoRoot", err)
	}
	if _, err := l.CreateOutput(filepath.Join(dir, "x")); !errors.Is(err, errNoRoot) {
		t.Errorf("CreateOutput without a root = %v; want errNoRoot", err)
	}

	if _, err := openLocalFS(dir, true); err == nil {
		t.Error("openLocalFS with both a root and -no-root succeeded; want an error")
	}
}

func TestCreateOutput(t *testing.T) {
	dir := t.TempDir()
	l, err := openLocalFS(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	write("out", "old")
	write("out.wgpart", "unrelated")

	a, err := l.CreateOutput("out")
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.CreateOutput("out")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = a.WriteString("from a")
	_, _ = b.WriteString("from b")
	b.Discard()
	if got := read("out"); got != "old" {
		t.Errorf("after Discard, out = %q", got)
	}
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	a.Discard()
	if got := read("out"); got != "from a" {
		t.Errorf("after Commit, out = %q", got)
	}
	if got := read("out.wgpart"); got != "unrelated" {
		t.Errorf("unrelated out.wgpart = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("left %d files behind, want out and out.wgpart only", len(entries))
	}
}
