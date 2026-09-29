package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

type calendarEvent struct {
	UID               string             `json:"uid"`
	Summary           string             `json:"summary"`
	Start             string             `json:"start"`
	Timezone          string             `json:"timezone,omitempty"`
	End               string             `json:"end,omitempty"`
	AllDay            bool               `json:"all_day"`
	URL               string             `json:"url,omitempty"`
	Recurrence        string             `json:"recurrence,omitempty"`
	Status            string             `json:"status,omitempty"`
	RecurrenceDetails []calendarProperty `json:"recurrence_details,omitempty"`
}
type calendarProperty struct {
	Name       string              `json:"name"`
	Value      string              `json:"value"`
	Parameters map[string][]string `json:"parameters,omitempty"`
}

type moodleCalendarResult struct {
	Course      string          `json:"course"`
	LastSuccess time.Time       `json:"last_success"`
	Events      []calendarEvent `json:"events"`
}

func readMoodleCalendar(cfg syncConfig, code string) (moodleCalendarResult, error) {
	result := moodleCalendarResult{Course: code, Events: []calendarEvent{}}
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

func parseCalendar(raw []byte) ([]calendarEvent, error) {
	raw = bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))
	if !bytes.HasPrefix(raw, []byte("BEGIN:VCALENDAR")) || !bytes.HasSuffix(raw, []byte("END:VCALENDAR")) {
		return nil, fmt.Errorf("calendar response is not a complete iCalendar feed")
	}
	cal, err := ics.ParseCalendar(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("calendar parse failed (feed details redacted)")
	}
	events := []calendarEvent{}
	for _, e := range cal.Events() {
		get := func(key string) string {
			p := e.GetProperty(ics.ComponentProperty(key))
			if p == nil {
				return ""
			}
			return p.Value
		}
		start := e.GetProperty(ics.ComponentProperty("DTSTART"))
		if start == nil || get("UID") == "" {
			return nil, fmt.Errorf("calendar event missing UID or DTSTART")
		}
		if !validICalTime(start.Value) || (get("DTEND") != "" && !validICalTime(get("DTEND"))) {
			return nil, fmt.Errorf("calendar event has invalid date/time syntax")
		}
		tz := ""
		if params := start.ICalParameters["TZID"]; len(params) > 0 {
			tz = params[0]
		}
		recurrence := []calendarProperty{}
		for _, key := range []string{"RECURRENCE-ID", "RDATE", "EXDATE"} {
			for _, p := range e.GetProperties(ics.ComponentProperty(key)) {
				recurrence = append(recurrence, calendarProperty{Name: key, Value: p.Value, Parameters: p.ICalParameters})
			}
		}
		events = append(events, calendarEvent{UID: get("UID"), Summary: ics.FromText(get("SUMMARY")), Start: start.Value, Timezone: tz, End: get("DTEND"), AllDay: len(start.Value) == 8, URL: cleanMoodleURL(get("URL")), Recurrence: get("RRULE"), Status: get("STATUS"), RecurrenceDetails: recurrence})
	}
	slices.SortFunc(events, func(a, b calendarEvent) int {
		if a.Start != b.Start {
			return strings.Compare(a.Start, b.Start)
		}
		return strings.Compare(a.UID, b.UID)
	})
	return events, nil
}
func validICalTime(value string) bool {
	for _, layout := range []string{"20060102", "20060102T150405", "20060102T150405Z"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}
func (c *moodleClient) calendar(ctx context.Context, key string) ([]calendarEvent, error) {
	raw := c.env[key]
	if raw == "" {
		return nil, fmt.Errorf("calendar URL variable %s is missing", key)
	}
	u, err := url.Parse(raw)
	if err != nil || !c.allowed(u) {
		return nil, fmt.Errorf("calendar URL must use the configured Moodle site")
	}
	if !strings.HasSuffix(u.Path, "/calendar/export_execute.php") {
		return nil, fmt.Errorf("calendar URL must be a Moodle export_execute.php URL")
	}
	r, err := c.request(ctx, "GET", raw, "", "", "", false)
	if err != nil {
		return nil, err
	}
	return parseCalendar(r.Body)
}
func calendarMarkdown(events []calendarEvent) string {
	var b strings.Builder
	b.WriteString("# Moodle calendar\n\nEvents from the configured feed; not every event is an assignment deadline.\nTimes and TZID are preserved exactly; recurring rules are not expanded.\n\n")
	for _, e := range events {
		fmt.Fprintf(&b, "- %s %s — %s", e.Start, e.Timezone, mdEscape(e.Summary))
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
