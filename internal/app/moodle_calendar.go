package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sbot5/lms-mcp/internal/moodle"
	"github.com/sbot5/lms-mcp/internal/render"
)

type moodleCalendarResult struct {
	Course      string                 `json:"course"`
	LastSuccess time.Time              `json:"last_success"`
	Events      []moodle.CalendarEvent `json:"events"`
}

func readMoodleCalendar(cfg syncConfig, code string) (moodleCalendarResult, error) {
	result := moodleCalendarResult{Course: code, Events: []moodle.CalendarEvent{}}
	if cfg.Moodle == nil {
		return result, fmt.Errorf("Moodle is not configured")
	}
	for _, co := range cfg.Moodle.Courses {
		if co.Code != strings.TrimPrefix(code, "moodle:") {
			continue
		}
		if co.CalendarEnv == "" {
			return result, fmt.Errorf("no calendar feed configured for this course")
		}
		s, err := readState(co.storage(*cfg.Moodle))
		if err != nil {
			return result, err
		}
		if s.LastSuccess.IsZero() || s.CalendarHash == "" {
			return result, fmt.Errorf("calendar has not been synchronized")
		}
		p, err := safeTarget(co.Directory, "_calendar.json")
		if err != nil {
			return result, err
		}
		b, err := os.ReadFile(filepath.Clean(p))
		if err != nil {
			return result, err
		}
		if digest(b) != s.CalendarHash {
			return result, fmt.Errorf("calendar differs from checkpoint; finish or retry synchronization")
		}
		if err = json.Unmarshal(b, &result.Events); err != nil {
			return result, fmt.Errorf("cached calendar is invalid")
		}
		result.LastSuccess = s.LastSuccess
		return result, nil
	}
	return result, fmt.Errorf("unknown Moodle course code")
}

func calendarMarkdown(events []moodle.CalendarEvent) string {
	var b strings.Builder
	b.WriteString("# Moodle calendar\n\nEvents from the configured feed; not every event is an assignment deadline.\nTimes and TZID are preserved exactly; recurring rules are not expanded.\n\n")
	for _, e := range events {
		fmt.Fprintf(&b, "- %s %s — %s", e.Start, e.Timezone, render.Escape(e.Summary))
		if e.AllDay {
			b.WriteString(" (all day)")
		}
		if e.Recurrence != "" {
			fmt.Fprintf(&b, "; recurrence: %s", e.Recurrence)
		}
		if e.Status != "" {
			fmt.Fprintf(&b, "; %s", e.Status)
		}
		b.WriteString("\n")
	}
	return b.String()
}
