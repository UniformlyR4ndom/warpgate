package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMkdir(t *testing.T) {
	_, ulRoot, secret := symlinkFixture(t)
	if err := os.Symlink("../outside", filepath.Join(ulRoot, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ulRoot, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})

	mkdir := func(path string) int {
		t.Helper()
		res, err := http.Post(ts.URL+"/api/ul/mkdir", "application/json", strings.NewReader(`{"path":"`+path+`"}`))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	cases := []struct {
		path string
		want int
	}{
		{"a", http.StatusCreated},
		{"a/b", http.StatusCreated},
		{"a/b/c", http.StatusCreated},
		{"a/b/c/d", http.StatusCreated},
		{"a/b/c/d/e", http.StatusBadRequest}, // depth limit
		{"a", http.StatusConflict},
		{"file", http.StatusConflict},
		{"missing/x", http.StatusNotFound},
		{"file/x", http.StatusNotFound},
		{"", http.StatusBadRequest},
		{"/", http.StatusBadRequest},
		{"../escape", http.StatusBadRequest},
		{"a/../../escape", http.StatusBadRequest},
		{ulTmpDirName, http.StatusBadRequest},
		{"a/.Warpgate-Tmp", http.StatusBadRequest},
		{ulTmpDirName + "/x", http.StatusBadRequest},
		{"trailing.", http.StatusBadRequest},
		{"trailing ", http.StatusBadRequest},
		{`back\\slash`, http.StatusBadRequest},
		{`ctl\u0001`, http.StatusBadRequest},
		{strings.Repeat("n", 256), http.StatusBadRequest},
		{"out/x", http.StatusForbidden}, // symlink leaving -ul-root
	}
	for _, c := range cases {
		if got := mkdir(c.path); got != c.want {
			t.Errorf("mkdir %q: status %d, want %d", c.path, got, c.want)
		}
	}
	if fi, err := os.Stat(filepath.Join(ulRoot, "a", "b", "c", "d")); err != nil || !fi.IsDir() {
		t.Fatalf("a/b/c/d not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(secret), "x")); !os.IsNotExist(err) {
		t.Fatalf("directory created outside -ul-root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(ulRoot), "escape")); !os.IsNotExist(err) {
		t.Fatalf("directory created outside -ul-root: %v", err)
	}
}

// TestMkdirDeepExisting: the depth limit applies to creation only.
func TestMkdirDeepExisting(t *testing.T) {
	ulRoot := t.TempDir()
	deep := filepath.Join(ulRoot, "1", "2", "3", "4", "5", "6")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, NoAuth: true})
	if resp, got := postChunk(t, ts.URL, "1/2/3/4/5/6/f.bin", []byte("x"), "", true); resp.StatusCode != http.StatusCreated || got.Name != "1/2/3/4/5/6/f.bin" {
		t.Fatalf("upload into deep existing dir: status %d, name %q", resp.StatusCode, got.Name)
	}
}

func TestUlFlat(t *testing.T) {
	ulRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(ulRoot, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := newSimTestServer(t, Config{Root: t.TempDir(), UlRoot: ulRoot, UlFlat: true, NoAuth: true})

	for _, p := range []string{"/api/ul/list", "/api/ul/download", "/api/ul/mkdir"} {
		res, err := http.Post(ts.URL+p, "application/json", strings.NewReader(`{"path":"sub"}`))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s with -ul-flat: status %d, want 404", p, res.StatusCode)
		}
	}
	if resp, _ := postChunk(t, ts.URL, "sub/f.bin", []byte("x"), "", true); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("upload with dir under -ul-flat: status %d, want 400", resp.StatusCode)
	}
	if resp, got := postChunk(t, ts.URL, "f.bin", []byte("x"), "", true); resp.StatusCode != http.StatusCreated || got.Name != "f.bin" {
		t.Fatalf("flat upload: status %d, name %q", resp.StatusCode, got.Name)
	}
}
