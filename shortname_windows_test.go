//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// shortName returns the 8.3 short name of path's last element, skipping the
// test if the volume generates none.
func shortName(t *testing.T, path string) string {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 1024)
	n, err := syscall.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Skipf("GetShortPathName: %v", err)
	}
	short := filepath.Base(syscall.UTF16ToString(buf[:n]))
	if strings.EqualFold(short, filepath.Base(path)) {
		t.Skipf("no 8.3 short name for %s", path)
	}
	return short
}

// TestUlTmpDirNotReachableViaShortName: the temp dir's 8.3 name passes the
// reserved-name check, so only the identity check stands in the way.
func TestUlTmpDirNotReachableViaShortName(t *testing.T) {
	dir := t.TempDir()
	ts := newSimTestServer(t, Config{Root: dir, UlRoot: dir, NoAuth: true})
	if err := os.WriteFile(filepath.Join(dir, ulTmpDirName, "ul-x"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	short := shortName(t, filepath.Join(dir, ulTmpDirName))
	t.Logf("temp dir short name: %s", short)
	if isReservedName(short) {
		t.Fatalf("short name %q unexpectedly caught by the name check", short)
	}

	post := func(path string, body any) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(body)
		res, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	for _, prefix := range []string{"/api/files/", "/api/ul/"} {
		for _, p := range []string{short, short + "/ul-x"} {
			if res := post(prefix+"list", listRequest{Path: p}); res.StatusCode != http.StatusForbidden {
				t.Errorf("%slist %q: status %d, want 403", prefix, p, res.StatusCode)
			}
		}
		if res := post(prefix+"download", downloadRequest{Path: short + "/ul-x", Size: 7, Transform: "id"}); res.StatusCode != http.StatusForbidden {
			t.Errorf("%sdownload via short name: status %d, want 403", prefix, res.StatusCode)
		}
	}
	if res := post("/api/ul/mkdir", mkdirRequest{Path: short + "/x"}); res.StatusCode != http.StatusForbidden {
		t.Errorf("mkdir inside the temp dir via short name: status %d, want 403", res.StatusCode)
	}
	if resp, _ := postChunk(t, ts.URL, short+"/f.bin", []byte("x"), "", true); resp.StatusCode != http.StatusForbidden {
		t.Errorf("upload into the temp dir via short name: status %d, want 403", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, ulTmpDirName, "x")); !os.IsNotExist(err) {
		t.Errorf("directory created inside the temp dir: %v", err)
	}
}
