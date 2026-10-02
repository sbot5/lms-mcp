package store

import (
	"database/sql"
	"errors"
)

// File is a downloaded material or attachment belonging to an item. SourceURL
// is stored as given; the caller strips any token or sesskey first.
type File struct {
	ID           string
	ItemID       string
	Name         string
	MIME         string
	Size         int64
	SourceURL    string
	LocalPath    string
	SHA256       string
	ETag         string
	LastModified string
	Status       string // "ok","video_link","too_large","failed","pending"
	Error        string
}

const fileCols = `id, item_id, name, mime, size, source_url, local_path, sha256, etag, last_modified, status, error`

// UpsertFile inserts or replaces a file by ID.
func (db *DB) UpsertFile(f File) error {
	return db.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO files (`+fileCols+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	item_id = excluded.item_id,
	name = excluded.name,
	mime = excluded.mime,
	size = excluded.size,
	source_url = excluded.source_url,
	local_path = excluded.local_path,
	sha256 = excluded.sha256,
	etag = excluded.etag,
	last_modified = excluded.last_modified,
	status = excluded.status,
	error = excluded.error`,
			f.ID, f.ItemID, f.Name, f.MIME, f.Size, f.SourceURL, f.LocalPath,
			f.SHA256, f.ETag, f.LastModified, f.Status, f.Error)
		return err
	})
}

func scanFile(s scanner) (File, error) {
	var f File
	err := s.Scan(&f.ID, &f.ItemID, &f.Name, &f.MIME, &f.Size, &f.SourceURL,
		&f.LocalPath, &f.SHA256, &f.ETag, &f.LastModified, &f.Status, &f.Error)
	return f, err
}

// GetFile returns the file with the given ID; ok is false when none exists.
func (db *DB) GetFile(id string) (File, bool, error) {
	f, err := scanFile(db.pool.QueryRow(`SELECT `+fileCols+` FROM files WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, err
	}
	return f, true, nil
}

// ListFiles returns all files for an item, ordered by name.
func (db *DB) ListFiles(itemID string) ([]File, error) {
	rows, err := db.pool.Query(`SELECT `+fileCols+` FROM files WHERE item_id = ? ORDER BY name ASC, id ASC`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
