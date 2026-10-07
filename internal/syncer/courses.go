package syncer

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/store"
)

var (
	edYearPattern     = regexp.MustCompile(`^[0-9]{4}$`)
	edSemesterPattern = regexp.MustCompile(`(?i)^(?:semester|sem|s)\s*([12])$`)
)

// edTermOrder compares only the year and semester formats Ed is known to send.
// A zero component is unknown, rather than an inferred calendar date.
func edTermOrder(year, session string) (int, int) {
	var y, semester int
	if year = strings.TrimSpace(year); edYearPattern.MatchString(year) {
		y, _ = strconv.Atoi(year)
	}
	if m := edSemesterPattern.FindStringSubmatch(strings.TrimSpace(session)); m != nil {
		semester, _ = strconv.Atoi(m[1])
	}
	return y, semester
}

// termLabel builds a compact term like "2026S2" from an Ed year and session.
// Unknown parts are omitted; an empty result falls back to the year alone.
func termLabel(year, session string) string {
	year = strings.TrimSpace(year)
	_, semester := edTermOrder(year, session)
	s := ""
	if semester != 0 {
		s = strconv.Itoa(semester)
	}
	switch {
	case year != "" && s != "":
		return year + "S" + s
	case year != "":
		return year
	case s != "":
		return "S" + s
	default:
		return "unknown"
	}
}

// courseKey uses CODE-TERM only when the complete year/semester is known.
// Unknown terms retain Ed's stable ID instead of conflating enrolments that
// share a code and a partial term label.
func courseKey(cfg courseCoder, c ed.Course) (code, term, id string) {
	term = termLabel(c.Year, c.Session)
	code = cfg.CourseCode(c.Code)
	if code == "" {
		code = cfg.CourseCode(c.Name)
	}
	year, semester := edTermOrder(c.Year, c.Session)
	if code == "" || year == 0 || semester == 0 {
		return code, term, fmt.Sprintf("ed-%d", c.ID)
	}
	return code, term, code + "-" + term
}

// currentEdCourse separates term ordering from the course's stable identity.
func currentEdCourse(c ed.Course, maxYear, maxSemester int) bool {
	year, semester := edTermOrder(c.Year, c.Session)
	return year == 0 || (year == maxYear && (semester == 0 || semester == maxSemester))
}

// uniqueMoodleOnlyCourse avoids guessing when multiple current Moodle courses
// have the same code. A known Moodle ID can identify an existing duplicate.
func uniqueMoodleOnlyCourse(courses map[string]store.Course, code string, moodleID int) (store.Course, bool) {
	var match store.Course
	for _, c := range courses {
		if c.EdCourseID != 0 || c.MoodleCourseID == 0 || !c.Active || c.Code != code || (moodleID != 0 && c.MoodleCourseID != moodleID) {
			continue
		}
		if match.ID != "" {
			return store.Course{}, false
		}
		match = c
	}
	return match, match.ID != ""
}

type courseCoder interface {
	CourseCode(string) string
	IsExcluded(string) bool
}

// DiscoverMoodle lists the user's Moodle courses and upserts them, pairing with
// an active Ed course of the same code when one is present (so a unit taught
// on both platforms is one row). Stable Moodle IDs retain their existing pair;
// historical rows are never reused for a new Moodle course. Returns the active,
// non-excluded courses.
func DiscoverMoodle(ctx context.Context, p *Providers, db *store.DB) ([]store.Course, error) {
	if p.Moodle == nil {
		return nil, fmt.Errorf("Moodle is not available: %s", p.MoodleReason)
	}
	sk, err := p.Moodle.Sesskey(ctx)
	if err != nil {
		return nil, err
	}
	mcourses, err := p.Moodle.Courses(ctx, sk.Value)
	if err != nil {
		return nil, err
	}
	existing, err := db.ListCourses()
	if err != nil {
		return nil, err
	}
	// Only current Ed courses can form a new pair. Moodle's names do not provide
	// a reliable term, so retain any pair already identified by its Moodle ID.
	seenMoodle := map[int]bool{}
	for _, m := range mcourses {
		seenMoodle[m.ID] = true
	}
	byCode := map[string]store.Course{}
	codeCount := map[string]int{}
	byMoodleID := map[int]store.Course{}
	byID := map[string]store.Course{}
	for _, c := range existing {
		// Keep absent courses for history, without refreshing their Moodle source.
		// A current Ed source stays active and can pair with a new Moodle ID.
		if c.MoodleCourseID != 0 && !seenMoodle[c.MoodleCourseID] {
			if c.EdCourseID == 0 {
				c.Active = false
			} else if c.Active {
				c.MoodleCourseID = 0
			}
			c.Excluded = c.Code != "" && p.Cfg.IsExcluded(c.Code)
			c.Active = c.Active && !c.Excluded
			if err := db.UpsertCourse(c); err != nil {
				return nil, err
			}
		}
		byID[c.ID] = c
		if c.MoodleCourseID != 0 {
			byMoodleID[c.MoodleCourseID] = c
		}
		if c.Code == "" || c.EdCourseID == 0 || !c.Active {
			continue
		}
		codeCount[c.Code]++
		if codeCount[c.Code] == 1 {
			byCode[c.Code] = c
		} else {
			delete(byCode, c.Code)
		}
	}
	var active []store.Course
	for _, m := range mcourses {
		code := p.Cfg.CourseCode(m.ShortName)
		if code == "" {
			code = p.Cfg.CourseCode(m.FullName)
		}
		var sc store.Course
		if previous, ok := byMoodleID[m.ID]; ok {
			sc = previous
			// The in-progress Moodle source is current even if Ed no longer
			// returns its enrolment. Keep that established pair on the same row.
			sc.Active = true
			if sc.EdCourseID == 0 {
				if code == "" {
					code = sc.Code
				}
				if code != "" {
					sc.Code = code
				}
				sc.Title, sc.Active = m.FullName, true
				if paired, ok := byCode[code]; ok && (paired.MoodleCourseID == 0 || paired.MoodleCourseID == m.ID) {
					sc = paired
					sc.MoodleCourseID, sc.Active = m.ID, true
				}
			}
		} else if paired, ok := byCode[code]; ok && code != "" && paired.MoodleCourseID == 0 {
			sc = paired
			sc.MoodleCourseID = m.ID
		} else {
			id := code
			if previous, exists := byID[id]; id == "" || (exists && previous.MoodleCourseID != m.ID) {
				id = fmt.Sprintf("moodle-%d", m.ID)
			}
			sc = store.Course{
				ID:             id,
				Code:           code,
				Title:          m.FullName,
				MoodleCourseID: m.ID,
				Active:         true,
			}
		}
		sc.Excluded = sc.Code != "" && p.Cfg.IsExcluded(sc.Code)
		sc.Active = sc.Active && !sc.Excluded
		if err := db.UpsertCourse(sc); err != nil {
			return nil, err
		}
		if sc.EdCourseID != 0 {
			// Repair Moodle-first and already-duplicated discoveries by stable
			// Moodle identity, preserving every source row's local history.
			for _, source := range byID {
				if source.ID == sc.ID || source.EdCourseID != 0 || source.MoodleCourseID != m.ID {
					continue
				}
				if err := db.MergeCourse(source.ID, sc.ID); err != nil {
					return nil, err
				}
				delete(byID, source.ID)
			}
		}
		byID[sc.ID] = sc
		byMoodleID[m.ID] = sc
		if candidate, ok := byCode[sc.Code]; ok && candidate.ID == sc.ID {
			byCode[sc.Code] = sc
		}
		if sc.Active && !sc.Excluded {
			active = append(active, sc)
		}
	}
	return active, nil
}

// DiscoverEd lists the user's Ed courses and upserts them. It marks courses in
// the newest known semester active and flags excluded ones. Unknown terms stay
// discoverable because their age cannot be determined. Returns the active, non-
// excluded courses for downstream content sync.
func DiscoverEd(ctx context.Context, p *Providers, db *store.DB) ([]store.Course, error) {
	if p.Ed == nil {
		return nil, fmt.Errorf("Ed is not available: %s", p.EdReason)
	}
	who, err := p.Ed.Whoami(ctx)
	if err != nil {
		return nil, err
	}
	maxYear, maxSemester := 0, 0
	for _, c := range who.Courses {
		year, semester := edTermOrder(c.Year, c.Session)
		if year > maxYear {
			maxYear, maxSemester = year, semester
		} else if year == maxYear && semester > maxSemester {
			maxSemester = semester
		}
	}
	existing, err := db.ListCourses()
	if err != nil {
		return nil, err
	}
	byID := map[string]store.Course{}
	byEdID := map[int]store.Course{}
	for _, c := range existing {
		byID[c.ID] = c
		if c.EdCourseID != 0 {
			if previous, ok := byEdID[c.EdCourseID]; !ok || (previous.MoodleCourseID == 0 && c.MoodleCourseID != 0) {
				byEdID[c.EdCourseID] = c
			}
		}
	}
	currentCodeCount := map[string]int{}
	for _, c := range who.Courses {
		code, _, _ := courseKey(p.Cfg, c)
		if code != "" && !p.Cfg.IsExcluded(code) && currentEdCourse(c, maxYear, maxSemester) {
			currentCodeCount[code]++
		}
	}
	var active []store.Course
	seenEd := map[string]bool{}
	for _, c := range who.Courses {
		code, term, id := courseKey(p.Cfg, c)
		if previous, ok := byEdID[c.ID]; ok {
			id = previous.ID
		} else if previous, ok := byID[id]; ok && previous.EdCourseID != 0 && previous.EdCourseID != c.ID {
			id = fmt.Sprintf("ed-%d", c.ID)
		}
		seenEd[id] = true
		excluded := code != "" && p.Cfg.IsExcluded(code)
		current := currentEdCourse(c, maxYear, maxSemester)
		sc := store.Course{
			ID:             id,
			Code:           code,
			Term:           term,
			Title:          c.Name,
			EdCourseID:     c.ID,
			MoodleCourseID: byID[id].MoodleCourseID,
			Active:         !excluded && current,
			Excluded:       excluded,
		}
		if err := db.UpsertCourse(sc); err != nil {
			return nil, err
		}
		if sc.Active && currentCodeCount[code] == 1 {
			if source, ok := uniqueMoodleOnlyCourse(byID, code, sc.MoodleCourseID); ok && source.ID != sc.ID {
				if err := db.MergeCourse(source.ID, sc.ID); err != nil {
					return nil, err
				}
				sc.MoodleCourseID = source.MoodleCourseID
				delete(byID, source.ID)
			}
		}
		byID[sc.ID] = sc
		byEdID[c.ID] = sc
		if sc.Active {
			active = append(active, sc)
		}
	}
	for _, c := range existing {
		if c.EdCourseID == 0 || seenEd[c.ID] {
			continue
		}
		// Retain unseen enrolments as history. A later Moodle discovery may
		// still find a current source for an established pair.
		c.Active = false
		c.Excluded = c.Code != "" && p.Cfg.IsExcluded(c.Code)
		if err := db.UpsertCourse(c); err != nil {
			return nil, err
		}
	}
	return active, nil
}
