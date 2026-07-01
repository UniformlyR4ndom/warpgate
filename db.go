package main

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
}

func OpenDB(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS clipboard (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp INTEGER NOT NULL,
		data      TEXT    NOT NULL
	)`); err != nil {
		conn.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}
	return &DB{sql: conn}, nil
}

func (db *DB) Close() error {
	return db.sql.Close()
}

type ClipItem struct {
	ID   int64  `json:"id"`
	TS   int64  `json:"ts"`
	Data string `json:"data"` // base64-encoded
}

func (db *DB) InsertClipboard(data []byte) (id int64, ts int64, err error) {
	ts = time.Now().Unix()
	res, err := db.sql.Exec(`INSERT INTO clipboard (timestamp, data) VALUES (?, ?)`, ts, data)
	if err != nil {
		return 0, 0, err
	}
	id, err = res.LastInsertId()
	return id, ts, err
}

func (db *DB) UpdateClipboard(id int64, data []byte) (bool, error) {
	ts := time.Now().Unix()
	res, err := db.sql.Exec(`UPDATE clipboard SET data = ?, timestamp = ? WHERE id = ?`, data, ts, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (db *DB) DeleteClipboard(id int64) (bool, error) {
	res, err := db.sql.Exec(`DELETE FROM clipboard WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (db *DB) ListClipboard() ([]ClipItem, error) {
	rows, err := db.sql.Query(`SELECT id, timestamp, data FROM clipboard ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ClipItem
	for rows.Next() {
		var id, ts int64
		var raw []byte
		if err := rows.Scan(&id, &ts, &raw); err != nil {
			return nil, err
		}
		items = append(items, ClipItem{
			ID:   id,
			TS:   ts,
			Data: base64.StdEncoding.EncodeToString(raw),
		})
	}
	return items, rows.Err()
}
