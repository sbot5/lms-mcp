package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Item is a post, announcement, slide, page, module or forum post. ID is a
// provider-qualified key such as "ed:thread:123" or "moodle:cm:789".
type Item struct {
	ID            string
	CourseID      string
	Provider      string // "ed" | "moodle"
	Kind          string // "thread","reply","announcement","slide","material","page","forum_post",...
	ParentID      string // "" if top-level
	Title         string
	URL           string
	AuthorRole    string // "staff","ta","student",""
	AuthorDisplay string
	CreatedAt     int64 // unix seconds, 0 if unknown
	UpdatedAt     int64
	BodyMD        string
	ContentHash   string // caller-computed over normalized content
	MetaJSON      string // provider-specific extras
	RemovedAt     int64  // 0 unless detected gone remotely
}

// ItemFilter narrows ListItems and Search. Zero fields are ignored; Limit == 0
// means no limit.
type ItemFilter struct {
	CourseID string
	Provider string
	Kinds    []string
	Since    int64 // UpdatedAt >= Since
	Limit    int
	Offset   int
}

const itemCols = `id, course_id, provider, kind, parent_id, title, url, author_role, author_display, created_at, updated_at, body_md, content_hash, meta_json, removed_at`

// UpsertItem inserts or replaces an item and refreshes its full-text index.
// It computes nothing itself: the caller sets ContentHash. changed is true when
// the row is new or the stored ContentHash differs from it.ContentHash.
func (db *DB) UpsertItem(it Item) (changed bool, err error) {
	err = db.write(func(tx *sql.Tx) error {
		var prevHash string
		switch e := tx.QueryRow(`SELECT content_hash FROM items WHERE id = ?`, it.ID).Scan(&prevHash); {
		case e == nil:
			changed = prevHash != it.ContentHash
		case errors.Is(e, sql.ErrNoRows):
			changed = true
		default:
			return e
		}

		if _, e := tx.Exec(`INSERT INTO items (`+itemCols+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	course_id = excluded.course_id,
	provider = excluded.provider,
	kind = excluded.kind,
	parent_id = excluded.parent_id,
	title = excluded.title,
	url = excluded.url,
	author_role = excluded.author_role,
	author_display = excluded.author_display,
	created_at = excluded.created_at,
	updated_at = excluded.updated_at,
	body_md = excluded.body_md,
	content_hash = excluded.content_hash,
	meta_json = excluded.meta_json,
	removed_at = excluded.removed_at`,
			it.ID, it.CourseID, it.Provider, it.Kind, it.ParentID, it.Title, it.URL,
			it.AuthorRole, it.AuthorDisplay, it.CreatedAt, it.UpdatedAt, it.BodyMD,
			it.ContentHash, it.MetaJSON, it.RemovedAt); e != nil {
			return e
		}

		// Keep the FTS index in step with the stored title/body.
		return reindexItemTx(tx, it.ID, it.Title, it.BodyMD)
	})
	return changed, err
}

func scanItem(s scanner) (Item, error) {
	var it Item
	err := s.Scan(&it.ID, &it.CourseID, &it.Provider, &it.Kind, &it.ParentID,
		&it.Title, &it.URL, &it.AuthorRole, &it.AuthorDisplay, &it.CreatedAt,
		&it.UpdatedAt, &it.BodyMD, &it.ContentHash, &it.MetaJSON, &it.RemovedAt)
	return it, err
}

// GetItem returns the item with the given ID. Removed items are still returned
// (with RemovedAt > 0); ok is false only when no such row exists.
func (db *DB) GetItem(id string) (Item, bool, error) {
	it, err := scanItem(db.pool.QueryRow(`SELECT `+itemCols+` FROM items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	return it, true, nil
}

// ListItems returns items matching f, newest first. Removed items are excluded.
func (db *DB) ListItems(f ItemFilter) ([]Item, error) {
	var b strings.Builder
	b.WriteString(`SELECT ` + itemCols + ` FROM items WHERE removed_at = 0`)
	args := itemFilterClauses(&b, nil, f, "")
	b.WriteString(` ORDER BY updated_at DESC, id ASC`)
	args = appendLimitOffset(&b, args, f.Limit, f.Offset)

	rows, err := db.pool.Query(b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkItemsRemoved stamps removed_at on every not-yet-removed item of the given
// course/provider/kind whose ID is not in keepIDs, and returns how many rows it
// changed. An empty keepIDs removes all matching rows. It is idempotent: a
// second call changes nothing because already-removed rows are skipped.
func (db *DB) MarkItemsRemoved(courseID, provider, kind string, keepIDs []string) (removed int, err error) {
	err = db.write(func(tx *sql.Tx) error {
		var b strings.Builder
		b.WriteString(`UPDATE items SET removed_at = ? WHERE course_id = ? AND provider = ? AND kind = ? AND removed_at = 0`)
		args := []any{nowUnix(), courseID, provider, kind}
		if len(keepIDs) > 0 {
			b.WriteString(` AND id NOT IN (`)
			b.WriteString(placeholders(len(keepIDs)))
			b.WriteString(`)`)
			for _, id := range keepIDs {
				args = append(args, id)
			}
		}
		res, e := tx.Exec(b.String(), args...)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		removed = int(n)
		return nil
	})
	return removed, err
}

// itemFilterClauses appends WHERE fragments for f (not including removed_at,
// which callers add) and returns the extended args. alias qualifies the item
// columns, e.g. "i." when items is joined, or "" for a plain items query.
func itemFilterClauses(b *strings.Builder, args []any, f ItemFilter, alias string) []any {
	if f.CourseID != "" {
		b.WriteString(" AND " + alias + "course_id = ?")
		args = append(args, f.CourseID)
	}
	if f.Provider != "" {
		b.WriteString(" AND " + alias + "provider = ?")
		args = append(args, f.Provider)
	}
	if len(f.Kinds) > 0 {
		b.WriteString(" AND " + alias + "kind IN (" + placeholders(len(f.Kinds)) + ")")
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.Since > 0 {
		b.WriteString(" AND " + alias + "updated_at >= ?")
		args = append(args, f.Since)
	}
	return args
}

// placeholders returns "?, ?, ..." with n placeholders (n >= 1).
func placeholders(n int) string {
	if n <= 1 {
		return "?"
	}
	return strings.Repeat("?, ", n-1) + "?"
}
