package store

import "database/sql"

// Course is a single enrolled course, keyed by a stable ID such as
// "ABC1234-2026S2".
type Course struct {
	ID             string
	Code           string
	Term           string
	Title          string
	EdCourseID     int // 0 if none
	MoodleCourseID int // 0 if none
	Active         bool
	Excluded       bool
}

const courseCols = `id, code, term, title, ed_course_id, moodle_course_id, active, is_excluded`

// UpsertCourse inserts or replaces a course by ID.
func (db *DB) UpsertCourse(c Course) error {
	return db.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO courses (`+courseCols+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	code = excluded.code,
	term = excluded.term,
	title = excluded.title,
	ed_course_id = excluded.ed_course_id,
	moodle_course_id = excluded.moodle_course_id,
	active = excluded.active,
	is_excluded = excluded.is_excluded`,
			c.ID, c.Code, c.Term, c.Title, c.EdCourseID, c.MoodleCourseID,
			boolToInt(c.Active), boolToInt(c.Excluded))
		return err
	})
}

// ListCourses returns all courses ordered by ID.
func (db *DB) ListCourses() ([]Course, error) {
	rows, err := db.pool.Query(`SELECT ` + courseCols + ` FROM courses ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Course
	for rows.Next() {
		var (
			c                Course
			active, excluded int64
		)
		if err := rows.Scan(&c.ID, &c.Code, &c.Term, &c.Title, &c.EdCourseID,
			&c.MoodleCourseID, &active, &excluded); err != nil {
			return nil, err
		}
		c.Active = active != 0
		c.Excluded = excluded != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetCourseExcluded flips the excluded flag on a course.
func (db *DB) SetCourseExcluded(id string, excluded bool) error {
	return db.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE courses SET is_excluded = ? WHERE id = ?`, boolToInt(excluded), id)
		return err
	})
}
