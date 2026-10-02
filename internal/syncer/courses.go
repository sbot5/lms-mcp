package syncer

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/store"
)

var sessionDigit = regexp.MustCompile(`\d+`)

// termLabel builds a compact term like "2026S2" from an Ed year and session.
// Unknown parts are omitted; an empty result falls back to the year alone.
func termLabel(year, session string) string {
	year = strings.TrimSpace(year)
	s := sessionDigit.FindString(session)
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

// courseKey is the stable store id for an Ed course: CODE-TERM when a code is
// known, else ed-<id>.
func courseKey(cfg courseCoder, c ed.Course) (code, term, id string) {
	term = termLabel(c.Year, c.Session)
	code = cfg.CourseCode(c.Code)
	if code == "" {
		code = cfg.CourseCode(c.Name)
	}
	if code == "" {
		return "", term, fmt.Sprintf("ed-%d", c.ID)
	}
	return code, term, code + "-" + term
}

type courseCoder interface {
	CourseCode(string) string
	IsExcluded(string) bool
}

// DiscoverMoodle lists the user's Moodle courses and upserts them, pairing with
// an existing Ed course of the same code when one is present (so a unit taught
// on both platforms is one row). Returns the active, non-excluded courses.
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
	// Index existing courses by code for pairing (newest term wins on ties).
	byCode := map[string]store.Course{}
	for _, c := range existing {
		if c.Code == "" {
			continue
		}
		if prev, ok := byCode[c.Code]; !ok || c.Term > prev.Term {
			byCode[c.Code] = c
		}
	}
	var active []store.Course
	for _, m := range mcourses {
		code := p.Cfg.CourseCode(m.ShortName)
		if code == "" {
			code = p.Cfg.CourseCode(m.FullName)
		}
		excluded := code != "" && p.Cfg.IsExcluded(code)
		var sc store.Course
		if paired, ok := byCode[code]; ok && code != "" {
			sc = paired
			sc.MoodleCourseID = m.ID
		} else {
			id := code
			if id == "" {
				id = fmt.Sprintf("moodle-%d", m.ID)
			}
			sc = store.Course{
				ID:             id,
				Code:           code,
				Title:          m.FullName,
				MoodleCourseID: m.ID,
				Active:         !excluded,
				Excluded:       excluded,
			}
		}
		if err := db.UpsertCourse(sc); err != nil {
			return nil, err
		}
		if sc.Active && !sc.Excluded {
			active = append(active, sc)
		}
	}
	return active, nil
}

// DiscoverEd lists the user's Ed courses and upserts them. It marks courses in
// the newest year active and flags excluded ones. Returns the active, non-
// excluded courses for downstream content sync.
func DiscoverEd(ctx context.Context, p *Providers, db *store.DB) ([]store.Course, error) {
	if p.Ed == nil {
		return nil, fmt.Errorf("Ed is not available: %s", p.EdReason)
	}
	who, err := p.Ed.Whoami(ctx)
	if err != nil {
		return nil, err
	}
	maxYear := ""
	for _, c := range who.Courses {
		if c.Year > maxYear {
			maxYear = c.Year
		}
	}
	var active []store.Course
	for _, c := range who.Courses {
		code, term, id := courseKey(p.Cfg, c)
		excluded := code != "" && p.Cfg.IsExcluded(code)
		sc := store.Course{
			ID:         id,
			Code:       code,
			Term:       term,
			Title:      c.Name,
			EdCourseID: c.ID,
			Active:     !excluded && c.Year == maxYear,
			Excluded:   excluded,
		}
		if err := db.UpsertCourse(sc); err != nil {
			return nil, err
		}
		if sc.Active {
			active = append(active, sc)
		}
	}
	return active, nil
}
