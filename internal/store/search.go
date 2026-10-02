package store

import (
	"database/sql"
	"strings"
)

// SearchHit is one full-text match: the item id, its title, a highlighted
// snippet and the bm25 score (more negative is a better match).
type SearchHit struct {
	ItemID  string
	Title   string
	Snippet string
	Score   float64
}

// reindexItemTx replaces the FTS row for id with the given title/body. Shared
// by UpsertItem (inside its transaction) and the exported ReindexItem.
func reindexItemTx(tx *sql.Tx, id, title, body string) error {
	if _, err := tx.Exec(`DELETE FROM item_fts WHERE item_id = ?`, id); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO item_fts (item_id, title, body) VALUES (?, ?, ?)`, id, title, body)
	return err
}

// ReindexItem refreshes the full-text entry for an item. UpsertItem calls the
// same logic; it is exported for standalone rebuilds.
func (db *DB) ReindexItem(id, title, body string) error {
	return db.write(func(tx *sql.Tx) error {
		return reindexItemTx(tx, id, title, body)
	})
}

// Search runs a full-text query over item titles and bodies, applying the same
// ItemFilter as ListItems. Hits are ranked by bm25 (titles weighted above
// bodies) and carry a highlighted snippet. Removed items never match. An empty
// query returns no hits.
func (db *DB) Search(query string, f ItemFilter) ([]SearchHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	var b strings.Builder
	// snippet(..., -1, ...) lets FTS5 pick the matching column, so a title-only
	// or body-only match still yields a non-empty, highlighted snippet. bm25
	// weights: item_id 0 (unindexed), title 10, body 1.
	b.WriteString(`SELECT f.item_id, i.title,
	snippet(item_fts, -1, '[', ']', '…', 12) AS snip,
	bm25(item_fts, 0.0, 10.0, 1.0) AS score
FROM item_fts f
JOIN items i ON i.id = f.item_id
WHERE item_fts MATCH ? AND i.removed_at = 0`)
	args := []any{query}
	args = itemFilterClauses(&b, args, f, "i.")
	b.WriteString(` ORDER BY score ASC, f.item_id ASC`)
	args = appendLimitOffset(&b, args, f.Limit, f.Offset)

	rows, err := db.pool.Query(b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.ItemID, &h.Title, &h.Snippet, &h.Score); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}
