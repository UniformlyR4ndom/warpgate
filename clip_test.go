package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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

	id, _, err := db.InsertClipboard("nmap scan", "", []byte("a"))
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

func mustInsert(t *testing.T, db *DB, title, text string) int64 {
	t.Helper()
	id, _, err := db.InsertClipboard(title, "", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClipStateChangedAndRev(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Empty: rev 0, nothing changed.
	rev, changed, ids, reset, err := db.ClipState(0)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 0 || len(changed) != 0 || ids != nil || reset {
		t.Fatalf("empty: rev=%d changed=%d ids=%v reset=%v", rev, len(changed), ids, reset)
	}

	id1 := mustInsert(t, db, "a", "one")
	id2 := mustInsert(t, db, "", "two")

	rev, changed, _, _, err = db.ClipState(0)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 2 || len(changed) != 2 {
		t.Fatalf("after 2 inserts: rev=%d changed=%d", rev, len(changed))
	}

	// Since current rev: nothing.
	_, changed, _, _, err = db.ClipState(rev)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("since==rev should be empty, got %d", len(changed))
	}

	// Edit id1 -> only id1 comes back as changed.
	if _, err := db.UpdateClipboard(id1, nil, []byte("one-v2")); err != nil {
		t.Fatal(err)
	}
	newRev, changed, ids, _, err := db.ClipState(rev)
	if err != nil {
		t.Fatal(err)
	}
	if newRev != 3 || len(changed) != 1 || changed[0].ID != id1 {
		t.Fatalf("edit: rev=%d changed=%+v", newRev, changed)
	}
	if ids != nil {
		t.Fatalf("no delete happened, ids should be nil, got %v", ids)
	}
	_ = id2
}

func TestClipStateDeletion(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	id1 := mustInsert(t, db, "a", "one")
	id2 := mustInsert(t, db, "b", "two")
	rev, _, _, _, _ := db.ClipState(0) // client caught up to here

	if ok, err := db.DeleteClipboard(id1); err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}

	newRev, changed, ids, _, err := db.ClipState(rev)
	if err != nil {
		t.Fatal(err)
	}
	if newRev != rev+1 {
		t.Fatalf("rev should advance on delete: %d -> %d", rev, newRev)
	}
	if len(changed) != 0 {
		t.Fatalf("delete adds nothing to changed, got %d", len(changed))
	}
	if len(ids) != 1 || ids[0] != id2 {
		t.Fatalf("ids should be live set [%d], got %v", id2, ids)
	}
}

func TestHandleClipStateDeleteLastNote(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true})
	id := mustInsert(t, s.db, "a", "one")
	rev, _, _, _, _ := s.db.ClipState(0)
	if ok, err := s.db.DeleteClipboard(id); err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}

	rec := httptest.NewRecorder()
	s.handleClipState(rec, httptest.NewRequest(http.MethodGet, "/api/clip/state?since="+strconv.FormatInt(rev, 10), nil))
	if !strings.Contains(rec.Body.String(), `"ids":[]`) {
		t.Fatalf("empty live set must be sent as ids:[], got %s", rec.Body.String())
	}
}

func TestHandleClipNewSource(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true})
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleClipNew(rec, httptest.NewRequest(http.MethodPost, "/api/clip/new", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"data":"YQ==","source":"outbox"}`); rec.Code != http.StatusOK {
		t.Fatalf("outbox source: status %d", rec.Code)
	}
	if rec := post(`{"data":"Yg=="}`); rec.Code != http.StatusOK {
		t.Fatalf("no source: status %d", rec.Code)
	}
	if rec := post(`{"data":"ZA==","source":"cli"}`); rec.Code != http.StatusOK {
		t.Fatalf("cli source: status %d", rec.Code)
	}
	if rec := post(`{"data":"Yw==","source":"other"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid source: status %d, want 400", rec.Code)
	}

	_, notes, _, _, err := s.db.ClipState(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 3 || notes[0].Source != clipSourceOutbox || notes[1].Source != "" || notes[2].Source != clipSourceCLI {
		t.Fatalf("got %+v, want sources [outbox, \"\", cli]", notes)
	}
	if _, err := s.db.UpdateClipboard(notes[0].ID, nil, []byte("edited")); err != nil {
		t.Fatal(err)
	}
	_, notes, _, _, _ = s.db.ClipState(0)
	if notes[0].Source != clipSourceOutbox {
		t.Fatalf("edit must keep source, got %q", notes[0].Source)
	}

	rec := httptest.NewRecorder()
	s.handleClipState(rec, httptest.NewRequest(http.MethodGet, "/api/clip/state?since=0", nil))
	if got := strings.Count(rec.Body.String(), `"source":`); got != 2 {
		t.Fatalf("source should only be sent for the outbox and cli notes, got %d in %s", got, rec.Body.String())
	}
}

func TestClipStateResetWhenSinceAhead(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustInsert(t, db, "a", "one")

	rev, changed, _, reset, err := db.ClipState(999)
	if err != nil {
		t.Fatal(err)
	}
	if !reset || rev != 1 || len(changed) != 1 {
		t.Fatalf("since ahead: reset=%v rev=%d changed=%d", reset, rev, len(changed))
	}
}

func TestAttachmentRoundTripAndRevBump(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	noteID := mustInsert(t, db, "shot", "")
	revBefore, _, _, _, _ := db.ClipState(0)

	img := []byte("\x89PNG\r\n\x1a\n fake png bytes")
	aid, err := db.InsertAttachment(noteID, "image/png", "first shot", img)
	if err != nil {
		t.Fatal(err)
	}

	// Attaching bumps the note's rev, so it reappears in changed.
	_, changed, _, _, err := db.ClipState(revBefore)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || len(changed[0].Attachments) != 1 || changed[0].Attachments[0].ID != aid {
		t.Fatalf("attach: changed=%+v", changed)
	}
	if a := changed[0].Attachments[0]; a.Size != int64(len(img)) || a.Caption != "first shot" {
		t.Fatalf("attachment meta = %+v", a)
	}

	// Changing the caption bumps the note's rev as well.
	revAttached, _, _, _, _ := db.ClipState(0)
	if ok, err := db.UpdateAttachmentCaption(aid, "renamed"); err != nil || !ok {
		t.Fatalf("caption update: ok=%v err=%v", ok, err)
	}
	_, changed, _, _, _ = db.ClipState(revAttached)
	if len(changed) != 1 || changed[0].Attachments[0].Caption != "renamed" {
		t.Fatalf("caption update: changed=%+v", changed)
	}
	if ok, err := db.UpdateAttachmentCaption(aid+100, "x"); err != nil || ok {
		t.Fatalf("caption update of missing id: ok=%v err=%v", ok, err)
	}

	mime, data, err := db.GetAttachment(aid)
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" || string(data) != string(img) {
		t.Fatalf("round trip mismatch: mime=%q data=%q", mime, data)
	}

	// Deleting the note removes its attachment.
	if _, err := db.DeleteClipboard(noteID); err != nil {
		t.Fatal(err)
	}
	if _, data, _ := db.GetAttachment(aid); data != nil {
		t.Fatalf("attachment should be gone after note delete")
	}
}

func TestInsertAttachmentMissingNote(t *testing.T) {
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.InsertAttachment(123, "image/png", "", []byte("x")); err != errNoteNotFound {
		t.Fatalf("want errNoteNotFound, got %v", err)
	}
}

func TestMigrateSeedsRevAboveLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE clipboard (id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp INTEGER NOT NULL, data TEXT NOT NULL)`,
		`INSERT INTO clipboard (timestamp, data) VALUES (1,'a'),(2,'b'),(3,'c')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Legacy rows backfilled to rev=id, so the global rev is 3; the whole set
	// comes back from since=0, and a fresh insert advances to rev 4.
	rev, changed, _, _, err := db.ClipState(0)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 3 || len(changed) != 3 {
		t.Fatalf("after migrate: rev=%d changed=%d", rev, len(changed))
	}
	id, _, err := db.InsertClipboard("", "", []byte("d"))
	if err != nil {
		t.Fatal(err)
	}
	rev2, changed2, _, _, _ := db.ClipState(3)
	if rev2 != 4 || len(changed2) != 1 || changed2[0].ID != id {
		t.Fatalf("new insert: rev=%d changed=%+v", rev2, changed2)
	}
}

func TestOpenDBMigratesCaptionColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE clipboard (id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE clip_attachment (id INTEGER PRIMARY KEY AUTOINCREMENT, note_id INTEGER NOT NULL,
			mime TEXT NOT NULL, size INTEGER NOT NULL, data BLOB NOT NULL)`,
		`INSERT INTO clipboard (timestamp, data) VALUES (1, 'legacy')`,
		`INSERT INTO clip_attachment (note_id, mime, size, data) VALUES (1, 'image/png', 1, x'00')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	_, notes, _, _, err := db.ClipState(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || len(notes[0].Attachments) != 1 || notes[0].Attachments[0].Caption != "" {
		t.Fatalf("got %+v, want one note with one uncaptioned image", notes)
	}
}

func TestHandleClipAttachmentCaption(t *testing.T) {
	s := newParamDecodeTestServer(t, Config{NoAuth: true})
	noteID := mustInsert(t, s.db, "n", "")

	upload := func(caption string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		spec, _ := json.Marshal(map[string]any{"transform": "id", "note_id": noteID, "mime": "image/png", "caption": caption})
		for _, part := range []struct{ field, name, data string }{
			{"transform", "transform.json", string(spec)},
			{"file", "image", "png"},
		} {
			pw, err := mw.CreateFormFile(part.field, part.name)
			if err == nil {
				_, err = pw.Write([]byte(part.data))
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/api/clip/attachment", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		s.handleClipAttachmentNew(rec, r)
		return rec
	}
	rec := upload("screen 1")
	var meta AttachmentMeta
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &meta) != nil || meta.Caption != "screen 1" {
		t.Fatalf("upload with caption: %d %s", rec.Code, rec.Body.String())
	}
	if rec := upload("two\nlines"); rec.Code != http.StatusBadRequest {
		t.Fatalf("multi-line caption: status %d, want 400", rec.Code)
	}

	edit := func(body string) int {
		rec := httptest.NewRecorder()
		s.handleClipAttachmentEdit(rec, httptest.NewRequest(http.MethodPost, "/api/clip/attachment/edit", strings.NewReader(body)))
		return rec.Code
	}
	if code := edit(fmt.Sprintf(`{"id":%d,"caption":"screen A"}`, meta.ID)); code != http.StatusNoContent {
		t.Fatalf("edit: status %d", code)
	}
	if code := edit(fmt.Sprintf(`{"id":%d,"caption":"%s"}`, meta.ID, strings.Repeat("x", clipTitleMaxLen+1))); code != http.StatusBadRequest {
		t.Fatalf("too long caption: status %d, want 400", code)
	}
	if code := edit(`{"id":99999,"caption":"x"}`); code != http.StatusNotFound {
		t.Fatalf("missing id: status %d, want 404", code)
	}
	_, notes, _, _, _ := s.db.ClipState(0)
	if got := notes[0].Attachments[0].Caption; got != "screen A" {
		t.Fatalf("stored caption = %q", got)
	}
}
