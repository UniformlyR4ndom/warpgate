package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenDBMigratesTitleColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE clipboard (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp INTEGER NOT NULL,
		data      TEXT    NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO clipboard (timestamp, data) VALUES (1, 'legacy')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	for range 2 { // second open must be a no-op
		db, err := OpenDB(path)
		if err != nil {
			t.Fatalf("OpenDB: %v", err)
		}
		items, err := db.ListClipboard()
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Title != "" {
			t.Fatalf("got %+v, want one untitled note", items)
		}
	}
}

func TestUpdateClipboardTitle(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	id, _, err := db.InsertClipboard("nmap scan", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	title := func() string {
		items, err := db.ListClipboard()
		if err != nil {
			t.Fatal(err)
		}
		return items[0].Title
	}

	if _, err := db.UpdateClipboard(id, nil, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "nmap scan" {
		t.Errorf("nil title: got %q, want title kept", got)
	}
	empty := ""
	if _, err := db.UpdateClipboard(id, &empty, []byte("c")); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "" {
		t.Errorf("empty title: got %q, want cleared", got)
	}
}

func TestValidClipTitle(t *testing.T) {
	cases := []struct {
		title string
		ok    bool
	}{
		{"", true},
		{"ACME – nmap internal 世界", true},
		{strings.Repeat("é", clipTitleMaxLen), true},
		{strings.Repeat("é", clipTitleMaxLen+1), false},
		{"line1\nline2", false},
		{"tab\there", false},
		{"bad\xffutf8", false},
	}
	for _, c := range cases {
		if got := validClipTitle(c.title); got != c.ok {
			t.Errorf("validClipTitle(%q) = %v, want %v", c.title, got, c.ok)
		}
	}
}
