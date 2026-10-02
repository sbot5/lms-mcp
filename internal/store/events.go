package store

import (
	"database/sql"
	"strings"
)

// Event is a detected change feeding whats_new and notifications, deduplicated
// on ID (e.g. "ed:thread:123:staff_post").
type Event struct {
	ID         string
	CourseID   string
	ItemID     string
	Kind       string // staff_post, announcement, reply_to_me, new_material, material_changed, new_grade, feedback, deadline_added, deadline_changed
	DetectedAt int64
	Title      string
	Summary    string
	Baseline   bool  // true for first-import; excluded from "recent changes"
	NotifiedAt int64 // 0 until a notification fired
}

// EventFilter narrows ListEvents. Baseline nil means either; a non-nil pointer
// restricts to that value (so &false excludes baseline events).
type EventFilter struct {
	Since      int64
	CourseID   string
	Kinds      []string
	Baseline   *bool
	Unnotified bool
	Limit      int
	Offset     int
}

const eventCols = `id, course_id, item_id, kind, detected_at, title, summary, baseline, notified_at`

// InsertEvent records an event. It is idempotent on ID: a repeat insert of the
// same ID is ignored and the first-seen row is kept.
func (db *DB) InsertEvent(e Event) error {
	return db.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO events (`+eventCols+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.CourseID, e.ItemID, e.Kind, e.DetectedAt, e.Title, e.Summary,
			boolToInt(e.Baseline), e.NotifiedAt)
		return err
	})
}

// ListEvents returns events matching f, newest first.
func (db *DB) ListEvents(f EventFilter) ([]Event, error) {
	var b strings.Builder
	b.WriteString(`SELECT ` + eventCols + ` FROM events WHERE 1 = 1`)
	var args []any
	if f.Since > 0 {
		b.WriteString(` AND detected_at >= ?`)
		args = append(args, f.Since)
	}
	if f.CourseID != "" {
		b.WriteString(` AND course_id = ?`)
		args = append(args, f.CourseID)
	}
	if len(f.Kinds) > 0 {
		b.WriteString(` AND kind IN (` + placeholders(len(f.Kinds)) + `)`)
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.Baseline != nil {
		b.WriteString(` AND baseline = ?`)
		args = append(args, boolToInt(*f.Baseline))
	}
	if f.Unnotified {
		b.WriteString(` AND notified_at = 0`)
	}
	b.WriteString(` ORDER BY detected_at DESC, id ASC`)
	args = appendLimitOffset(&b, args, f.Limit, f.Offset)

	rows, err := db.pool.Query(b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var (
			e        Event
			baseline int64
		)
		if err := rows.Scan(&e.ID, &e.CourseID, &e.ItemID, &e.Kind, &e.DetectedAt,
			&e.Title, &e.Summary, &baseline, &e.NotifiedAt); err != nil {
			return nil, err
		}
		e.Baseline = baseline != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkNotified stamps notified_at = at on the given event IDs.
func (db *DB) MarkNotified(ids []string, at int64) error {
	if len(ids) == 0 {
		return nil
	}
	return db.write(func(tx *sql.Tx) error {
		args := make([]any, 0, len(ids)+1)
		args = append(args, at)
		for _, id := range ids {
			args = append(args, id)
		}
		_, err := tx.Exec(`UPDATE events SET notified_at = ? WHERE id IN (`+placeholders(len(ids))+`)`, args...)
		return err
	})
}
