package store

import (
	"database/sql"
	"fmt"
)

// MergeCourse moves every course-owned record from sourceID to destinationID
// and removes the duplicate source course in one transaction. Destination
// metadata wins; missing labels and provider IDs are filled from the source.
// Different nonzero provider IDs, different codes, or colliding grade/deadline
// keys abort the whole transaction so no history is silently overwritten.
func (db *DB) MergeCourse(sourceID, destinationID string) error {
	if sourceID == destinationID {
		return nil
	}
	return db.write(func(tx *sql.Tx) error {
		read := func(id string) (Course, error) {
			c := Course{ID: id}
			err := tx.QueryRow(`SELECT code, term, title, ed_course_id, moodle_course_id FROM courses WHERE id = ?`, id).
				Scan(&c.Code, &c.Term, &c.Title, &c.EdCourseID, &c.MoodleCourseID)
			return c, err
		}
		source, err := read(sourceID)
		if err != nil {
			return fmt.Errorf("store: merge source course: %w", err)
		}
		destination, err := read(destinationID)
		if err != nil {
			return fmt.Errorf("store: merge destination course: %w", err)
		}
		if (source.EdCourseID != 0 && destination.EdCourseID != 0 && source.EdCourseID != destination.EdCourseID) ||
			(source.MoodleCourseID != 0 && destination.MoodleCourseID != 0 && source.MoodleCourseID != destination.MoodleCourseID) ||
			(source.Code != "" && destination.Code != "" && source.Code != destination.Code) {
			return fmt.Errorf("store: cannot merge courses with conflicting provider identities or codes")
		}
		if _, err := tx.Exec(`UPDATE courses SET
	code = CASE WHEN code = '' THEN ? ELSE code END,
	term = CASE WHEN term = '' THEN ? ELSE term END,
	title = CASE WHEN title = '' THEN ? ELSE title END,
	ed_course_id = CASE WHEN ed_course_id = 0 THEN ? ELSE ed_course_id END,
	moodle_course_id = CASE WHEN moodle_course_id = 0 THEN ? ELSE moodle_course_id END
WHERE id = ?`, source.Code, source.Term, source.Title, source.EdCourseID, source.MoodleCourseID, destinationID); err != nil {
			return err
		}
		for _, stmt := range []string{
			`UPDATE items SET course_id = ? WHERE course_id = ?`,
			`UPDATE events SET course_id = ? WHERE course_id = ?`,
			`UPDATE grades SET course_id = ? WHERE course_id = ?`,
			`UPDATE deadlines SET course_id = ? WHERE course_id = ?`,
		} {
			if _, err := tx.Exec(stmt, destinationID, sourceID); err != nil {
				return fmt.Errorf("store: merge course records: %w", err)
			}
		}
		_, err = tx.Exec(`DELETE FROM courses WHERE id = ?`, sourceID)
		return err
	})
}
