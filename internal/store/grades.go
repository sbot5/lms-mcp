package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Grade is one gradebook entry, keyed by (CourseID, ItemKey). The schema exists
// in v1; rows are populated in a later milestone.
type Grade struct {
	CourseID, ItemKey, Name string
	Grade, GradeMax         string
	Percentage              string
	FeedbackMD              string
	GradedAt                int64
	Hash                    string
}

const gradeCols = `course_id, item_key, name, grade, grade_max, percentage, feedback_md, graded_at, hash`

// UpsertGrade inserts or replaces a grade. changed is true when the row is new
// or the stored Hash differs.
func (db *DB) UpsertGrade(g Grade) (changed bool, err error) {
	err = db.write(func(tx *sql.Tx) error {
		var prevHash string
		switch e := tx.QueryRow(`SELECT hash FROM grades WHERE course_id = ? AND item_key = ?`, g.CourseID, g.ItemKey).Scan(&prevHash); {
		case e == nil:
			changed = prevHash != g.Hash
		case errors.Is(e, sql.ErrNoRows):
			changed = true
		default:
			return e
		}

		_, e := tx.Exec(`INSERT INTO grades (`+gradeCols+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(course_id, item_key) DO UPDATE SET
	name = excluded.name,
	grade = excluded.grade,
	grade_max = excluded.grade_max,
	percentage = excluded.percentage,
	feedback_md = excluded.feedback_md,
	graded_at = excluded.graded_at,
	hash = excluded.hash`,
			g.CourseID, g.ItemKey, g.Name, g.Grade, g.GradeMax, g.Percentage,
			g.FeedbackMD, g.GradedAt, g.Hash)
		return e
	})
	return changed, err
}

// ListGrades returns all grades for a course ordered by item key.
func (db *DB) ListGrades(courseID string) ([]Grade, error) {
	rows, err := db.pool.Query(`SELECT `+gradeCols+` FROM grades WHERE course_id = ? ORDER BY item_key ASC`, courseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Grade
	for rows.Next() {
		var g Grade
		if err := rows.Scan(&g.CourseID, &g.ItemKey, &g.Name, &g.Grade, &g.GradeMax,
			&g.Percentage, &g.FeedbackMD, &g.GradedAt, &g.Hash); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Deadline is an assignment/quiz window, keyed by (CourseID, ItemID, Kind). The
// schema exists in v1; rows are populated in a later milestone.
type Deadline struct {
	CourseID, ItemID, Kind   string
	OpensAt, DueAt, CutoffAt int64
	SubmissionStatus         string
	Completed                bool
}

const deadlineCols = `course_id, item_id, kind, opens_at, due_at, cutoff_at, submission_status, completed`

// UpsertDeadline inserts or replaces a deadline.
func (db *DB) UpsertDeadline(d Deadline) error {
	return db.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO deadlines (`+deadlineCols+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(course_id, item_id, kind) DO UPDATE SET
	opens_at = excluded.opens_at,
	due_at = excluded.due_at,
	cutoff_at = excluded.cutoff_at,
	submission_status = excluded.submission_status,
	completed = excluded.completed`,
			d.CourseID, d.ItemID, d.Kind, d.OpensAt, d.DueAt, d.CutoffAt,
			d.SubmissionStatus, boolToInt(d.Completed))
		return err
	})
}

// ListDeadlines returns deadlines whose DueAt is within [from, to], ordered by
// DueAt. A to <= 0 means "no upper bound".
func (db *DB) ListDeadlines(from, to int64) ([]Deadline, error) {
	var b strings.Builder
	b.WriteString(`SELECT ` + deadlineCols + ` FROM deadlines WHERE due_at >= ?`)
	args := []any{from}
	if to > 0 {
		b.WriteString(` AND due_at <= ?`)
		args = append(args, to)
	}
	b.WriteString(` ORDER BY due_at ASC, item_id ASC`)

	rows, err := db.pool.Query(b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Deadline
	for rows.Next() {
		var (
			d         Deadline
			completed int64
		)
		if err := rows.Scan(&d.CourseID, &d.ItemID, &d.Kind, &d.OpensAt, &d.DueAt,
			&d.CutoffAt, &d.SubmissionStatus, &completed); err != nil {
			return nil, err
		}
		d.Completed = completed != 0
		out = append(out, d)
	}
	return out, rows.Err()
}
