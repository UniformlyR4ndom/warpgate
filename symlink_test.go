package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
)

// symlinkFixture builds root/ and uploads/ next to an outside/ directory:
//
//	root/escape       -> ../outside
//	root/inside       -> a.txt
//	root/insidedir    -> sub
//	root/broken       -> missing
//	uploads/victim.txt -> ../outside/secret.txt
func symlinkFixture(t *testing.T) (root, ulRoot, secret string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	ulRoot = filepath.Join(base, "uploads")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, ulRoot, outside, filepath.Join(root, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret = filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		filepath.Join(root, "escape"):       "../outside",
		filepath.Join(root, "inside"):       "a.txt",
		filepath.Join(root, "insidedir"):    "sub",
		filepath.Join(root, "broken"):       "missing",
		filepath.Join(ulRoot, "victim.txt"): "../outside/secret.txt",
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	return root, ulRoot, secret
}

func TestListDirSymlinks(t *testing.T) {
	root, _, _ := symlinkFixture(t)
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	entries, err := listDir(r, ".")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]listEntry{}
	for _, e := range entries {
		got[e.Name] = e
	}
	if e := got["escape"]; e.Warning != "symlink points outside the root" {
		t.Errorf("escape: %+v", e)
	}
	if e := got["broken"]; e.Warning != "broken symlink" {
		t.Errorf("broken: %+v", e)
	}
	if e := got["inside"]; e.Warning != "" || e.Type != "f" || e.Size == nil || *e.Size != 6 {
		t.Errorf("inside: %+v", e)
	}
	if e := got["insidedir"]; e.Warning != "" || e.Type != "d" {
		t.Errorf("insidedir: %+v", e)
	}
}

func TestSymlinkEscapeBlocked(t *testing.T) {
	root, ulRoot, secret := symlinkFixture(t)
	ts := newSimTestServer(t, Config{Root: root, UlRoot: ulRoot, NoAuth: true})

	post := func(path string, body any) int {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if code := post("/api/files/download", downloadRequest{Path: "escape/secret.txt", Size: 100, Transform: "id"}); code != http.StatusForbidden {
		t.Errorf("download through escaping link: status %d, want 403", code)
	}
	if code := post("/api/files/download", downloadRequest{Path: "inside", Size: 100, Transform: "id"}); code != http.StatusOK {
		t.Errorf("download through inside link: status %d, want 200", code)
	}
	if code := post("/api/files/list", listRequest{Path: "escape"}); code != http.StatusForbidden {
		t.Errorf("list through escaping link: status %d, want 403", code)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/api/files/ws/download"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(wsDownloadRequest{Path: "escape/secret.txt", Transform: "id"}); err != nil {
		t.Fatal(err)
	}
	var ack wsAck
	if err := conn.ReadJSON(&ack); err != nil || ack.OK || ack.Error != errSymlinkEscape {
		t.Errorf("WS download through escaping link: ack %+v, err %v", ack, err)
	}

	if resp, _ := postChunk(t, ts.URL, "victim.txt", []byte("OVERWRITTEN"), "", true); resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload over symlink: status %d", resp.StatusCode)
	}
	if got, _ := os.ReadFile(secret); string(got) != "OUTSIDE" {
		t.Errorf("link target outside the upload root was modified: %q", got)
	}
	if fi, err := os.Lstat(filepath.Join(ulRoot, "victim.txt")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("victim.txt should now be a regular file: %v, %v", fi, err)
	}
}
