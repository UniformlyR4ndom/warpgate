package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"warpgate/internal/authhash"
)

// errNoteNotFound is returned by attachment writes whose target note is gone.
var errNoteNotFound = errors.New("note not found")

type DB struct {
	sql *sql.DB
}

func OpenDB(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	// One connection serializes all access in Go: it keeps a ":memory:" test
	// DB single-shared (each modernc in-memory connection is otherwise its own
	// database) and removes SQLITE_BUSY under write contention. Clipboard load
	// is human-paced and blob reads release the connection before the network
	// write, so the lack of read parallelism costs nothing here.
	conn.SetMaxOpenConns(1)
	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return &DB{sql: conn}, nil
}

func migrate(conn *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS clipboard (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			data      TEXT    NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS meta_blob (
			key   TEXT PRIMARY KEY,
			value BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS clip_attachment (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			note_id INTEGER NOT NULL REFERENCES clipboard(id),
			mime    TEXT    NOT NULL,
			size    INTEGER NOT NULL,
			data    BLOB    NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := conn.Exec(q); err != nil {
			return fmt.Errorf("migrating schema: %w", err)
		}
	}
	if err := addColumnIfMissing(conn, "clipboard", "title", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrating schema: %w", err)
	}
	if err := addColumnIfMissing(conn, "clipboard", "rev", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("migrating schema: %w", err)
	}
	if err := addColumnIfMissing(conn, "clipboard", "source", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrating schema: %w", err)
	}
	if err := addColumnIfMissing(conn, "clip_attachment", "caption", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrating schema: %w", err)
	}
	// Indexes after the rev column exists.
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS idx_clipboard_rev ON clipboard(rev)`,
		`CREATE INDEX IF NOT EXISTS idx_attachment_note ON clip_attachment(note_id)`,
	} {
		if _, err := conn.Exec(q); err != nil {
			return fmt.Errorf("migrating schema: %w", err)
		}
	}
	// Backfill rev for pre-existing rows (id is monotonic, so this preserves
	// order with distinct positive revs). New rows always get rev > 0, so this
	// only ever touches legacy rows and is idempotent.
	if _, err := conn.Exec(`UPDATE clipboard SET rev = id WHERE rev = 0`); err != nil {
		return fmt.Errorf("migrating schema: %w", err)
	}
	if _, err := conn.Exec(
		`INSERT OR IGNORE INTO meta (key, value) VALUES ('clip_rev', (SELECT COALESCE(MAX(rev), 0) FROM clipboard))`,
	); err != nil {
		return fmt.Errorf("migrating schema: %w", err)
	}
	if _, err := conn.Exec(`INSERT OR IGNORE INTO meta (key, value) VALUES ('last_del_rev', 0)`); err != nil {
		return fmt.Errorf("migrating schema: %w", err)
	}
	salt := make([]byte, authhash.SaltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generating KDF salt: %w", err)
	}
	if _, err := conn.Exec(`INSERT OR IGNORE INTO meta_blob (key, value) VALUES ('kdf_salt', ?)`, salt); err != nil {
		return fmt.Errorf("migrating schema: %w", err)
	}
	return nil
}

// KDFSalt returns the salt for authhash.DeriveKey, fixed for the life of the
// database.
func (d *DB) KDFSalt() ([]byte, error) {
	var salt []byte
	if err := d.sql.QueryRow(`SELECT value FROM meta_blob WHERE key = 'kdf_salt'`).Scan(&salt); err != nil {
		return nil, fmt.Errorf("reading KDF salt: %w", err)
	}
	return salt, nil
}

func addColumnIfMissing(conn *sql.DB, table, column, decl string) error {
	rows, err := conn.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = conn.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, decl))
	return err
}

func (db *DB) Close() error {
	return db.sql.Close()
}

// nextRev bumps and returns the global clip_rev counter within tx.
func nextRev(tx *sql.Tx) (int64, error) {
	var cur int64
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'clip_rev'`).Scan(&cur); err != nil {
		return 0, err
	}
	cur++
	if _, err := tx.Exec(`UPDATE meta SET value = ? WHERE key = 'clip_rev'`, cur); err != nil {
		return 0, err
	}
	return cur, nil
}

type ClipItem struct {
	ID    int64  `json:"id"`
	TS    int64  `json:"ts"`
	Title string `json:"title"`
	Data  string `json:"data"` // base64-encoded
}

// AttachmentMeta describes an attachment without its bytes.
type AttachmentMeta struct {
	ID      int64  `json:"id"`
	Mime    string `json:"mime"`
	Size    int64  `json:"size"`
	Caption string `json:"caption,omitempty"`
}

// NoteMeta is a note as sent in the sync feed: text inline, images by
// reference only.
type NoteMeta struct {
	ID          int64            `json:"id"`
	TS          int64            `json:"ts"`
	Title       string           `json:"title"`
	Source      string           `json:"source,omitempty"`
	Data        string           `json:"data"` // base64-encoded text
	Attachments []AttachmentMeta `json:"attachments"`
}

func (db *DB) InsertClipboard(title, source string, data []byte) (id int64, ts int64, err error) {
	ts = time.Now().Unix()
	tx, err := db.sql.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	rev, err := nextRev(tx)
	if err != nil {
		return 0, 0, err
	}
	res, err := tx.Exec(`INSERT INTO clipboard (timestamp, title, source, data, rev) VALUES (?, ?, ?, ?, ?)`,
		ts, title, source, data, rev)
	if err != nil {
		return 0, 0, err
	}
	if id, err = res.LastInsertId(); err != nil {
		return 0, 0, err
	}
	return id, ts, tx.Commit()
}

// UpdateClipboard keeps the existing title when title is nil.
func (db *DB) UpdateClipboard(id int64, title *string, data []byte) (bool, error) {
	ts := time.Now().Unix()
	tx, err := db.sql.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	rev, err := nextRev(tx)
	if err != nil {
		return false, err
	}
	res, err := tx.Exec(
		`UPDATE clipboard SET data = ?, title = COALESCE(?, title), timestamp = ?, rev = ? WHERE id = ?`,
		data, title, ts, rev, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil // no row changed; leave the rev bump, it's harmless
	}
	return true, tx.Commit()
}

func (db *DB) DeleteClipboard(id int64) (bool, error) {
	tx, err := db.sql.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	rev, err := nextRev(tx)
	if err != nil {
		return false, err
	}
	res, err := tx.Exec(`DELETE FROM clipboard WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if _, err := tx.Exec(`DELETE FROM clip_attachment WHERE note_id = ?`, id); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE meta SET value = ? WHERE key = 'last_del_rev'`, rev); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// InsertAttachment stores an image for a note and bumps that note's rev (but
// not its display timestamp). Returns errNoteNotFound if the note is gone.
func (db *DB) InsertAttachment(noteID int64, mime, caption string, data []byte) (int64, error) {
	tx, err := db.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rev, err := nextRev(tx)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE clipboard SET rev = ? WHERE id = ?`, rev, noteID)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, err
	} else if n == 0 {
		return 0, errNoteNotFound
	}
	res, err = tx.Exec(`INSERT INTO clip_attachment (note_id, mime, caption, size, data) VALUES (?, ?, ?, ?, ?)`,
		noteID, mime, caption, int64(len(data)), data)
	if err != nil {
		return 0, err
	}
	aid, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return aid, tx.Commit()
}

// DeleteAttachment removes one image and bumps its owner note's rev. Returns
// false if no attachment with that id exists.
func (db *DB) DeleteAttachment(id int64) (bool, error) {
	tx, err := db.sql.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var noteID int64
	if err := tx.QueryRow(`SELECT note_id FROM clip_attachment WHERE id = ?`, id).Scan(&noteID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	rev, err := nextRev(tx)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE clipboard SET rev = ? WHERE id = ?`, rev, noteID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM clip_attachment WHERE id = ?`, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// UpdateAttachmentCaption sets one image's caption and bumps its owner note's
// rev. Returns false if no attachment with that id exists.
func (db *DB) UpdateAttachmentCaption(id int64, caption string) (bool, error) {
	tx, err := db.sql.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var noteID int64
	if err := tx.QueryRow(`SELECT note_id FROM clip_attachment WHERE id = ?`, id).Scan(&noteID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	rev, err := nextRev(tx)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE clipboard SET rev = ? WHERE id = ?`, rev, noteID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE clip_attachment SET caption = ? WHERE id = ?`, caption, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// GetAttachment returns one attachment's mime type and raw bytes.
func (db *DB) GetAttachment(id int64) (mime string, data []byte, err error) {
	err = db.sql.QueryRow(`SELECT mime, data FROM clip_attachment WHERE id = ?`, id).Scan(&mime, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	return mime, data, err
}

// ClipState returns the current global rev, the notes changed since `since`
// (new or edited), and — only when a deletion happened since `since` — the
// full set of live note ids for deletion reconciliation. reset is true when
// `since` is ahead of the server (a reset/swapped DB), telling the client to
// discard its state and take `changed` as the full snapshot.
func (db *DB) ClipState(since int64) (rev int64, changed []NoteMeta, ids []int64, reset bool, err error) {
	tx, err := db.sql.Begin()
	if err != nil {
		return 0, nil, nil, false, err
	}
	defer tx.Rollback()

	if err = tx.QueryRow(`SELECT value FROM meta WHERE key = 'clip_rev'`).Scan(&rev); err != nil {
		return 0, nil, nil, false, err
	}
	if since == rev {
		return rev, nil, nil, false, tx.Commit()
	}
	if since > rev {
		reset = true
		since = 0
	}

	changed, err = queryNotes(tx, since)
	if err != nil {
		return 0, nil, nil, false, err
	}

	var lastDel int64
	if err = tx.QueryRow(`SELECT value FROM meta WHERE key = 'last_del_rev'`).Scan(&lastDel); err != nil {
		return 0, nil, nil, false, err
	}
	if lastDel > since {
		ids, err = queryLiveIDs(tx)
		if err != nil {
			return 0, nil, nil, false, err
		}
	}
	return rev, changed, ids, reset, tx.Commit()
}

func queryNotes(tx *sql.Tx, since int64) ([]NoteMeta, error) {
	rows, err := tx.Query(`SELECT id, timestamp, title, source, data FROM clipboard WHERE rev > ? ORDER BY id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []NoteMeta
	index := map[int64]int{}
	for rows.Next() {
		var n NoteMeta
		var raw []byte
		if err := rows.Scan(&n.ID, &n.TS, &n.Title, &n.Source, &raw); err != nil {
			return nil, err
		}
		n.Data = base64.StdEncoding.EncodeToString(raw)
		n.Attachments = []AttachmentMeta{}
		index[n.ID] = len(notes)
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(notes) == 0 {
		return notes, nil
	}
	if err := attachMeta(tx, notes, index); err != nil {
		return nil, err
	}
	return notes, nil
}

func attachMeta(tx *sql.Tx, notes []NoteMeta, index map[int64]int) error {
	ids := make([]any, len(notes))
	for i, n := range notes {
		ids[i] = n.ID
	}
	q := `SELECT note_id, id, mime, size, caption FROM clip_attachment WHERE note_id IN (?` +
		strings.Repeat(`,?`, len(ids)-1) + `) ORDER BY id`
	rows, err := tx.Query(q, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var noteID int64
		var a AttachmentMeta
		if err := rows.Scan(&noteID, &a.ID, &a.Mime, &a.Size, &a.Caption); err != nil {
			return err
		}
		if i, ok := index[noteID]; ok {
			notes[i].Attachments = append(notes[i].Attachments, a)
		}
	}
	return rows.Err()
}

func queryLiveIDs(tx *sql.Tx) ([]int64, error) {
	rows, err := tx.Query(`SELECT id FROM clipboard ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (db *DB) ListClipboard() ([]ClipItem, error) {
	rows, err := db.sql.Query(`SELECT id, timestamp, title, data FROM clipboard ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ClipItem
	for rows.Next() {
		var id, ts int64
		var title string
		var raw []byte
		if err := rows.Scan(&id, &ts, &title, &raw); err != nil {
			return nil, err
		}
		items = append(items, ClipItem{
			ID:    id,
			TS:    ts,
			Title: title,
			Data:  base64.StdEncoding.EncodeToString(raw),
		})
	}
	return items, rows.Err()
}
