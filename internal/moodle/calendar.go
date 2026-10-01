package moodle

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

type CalendarEvent struct {
	UID               string             `json:"uid"`
	Summary           string             `json:"summary"`
	Start             string             `json:"start"`
	Timezone          string             `json:"timezone,omitempty"`
	End               string             `json:"end,omitempty"`
	AllDay            bool               `json:"all_day"`
	URL               string             `json:"url,omitempty"`
	Recurrence        string             `json:"recurrence,omitempty"`
	Status            string             `json:"status,omitempty"`
	RecurrenceDetails []CalendarProperty `json:"recurrence_details,omitempty"`
}
type CalendarProperty struct {
	Name       string              `json:"name"`
	Value      string              `json:"value"`
	Parameters map[string][]string `json:"parameters,omitempty"`
}

func ParseCalendar(raw []byte) ([]CalendarEvent, error) {
	raw = bytes.TrimSpace(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))
	if !bytes.HasPrefix(raw, []byte("BEGIN:VCALENDAR")) || !bytes.HasSuffix(raw, []byte("END:VCALENDAR")) {
		return nil, fmt.Errorf("calendar response is not a complete iCalendar feed")
	}
	cal, err := ics.ParseCalendar(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("calendar parse failed (feed details redacted)")
	}
	events := []CalendarEvent{}
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
		recurrence := []CalendarProperty{}
		for _, key := range []string{"RECURRENCE-ID", "RDATE", "EXDATE"} {
			for _, p := range e.GetProperties(ics.ComponentProperty(key)) {
				recurrence = append(recurrence, CalendarProperty{Name: key, Value: p.Value, Parameters: p.ICalParameters})
			}
		}
		events = append(events, CalendarEvent{UID: get("UID"), Summary: ics.FromText(get("SUMMARY")), Start: start.Value, Timezone: tz, End: get("DTEND"), AllDay: len(start.Value) == 8, URL: CleanURL(get("URL")), Recurrence: get("RRULE"), Status: get("STATUS"), RecurrenceDetails: recurrence})
	}
	slices.SortFunc(events, func(a, b CalendarEvent) int {
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
func (c *Client) Calendar(ctx context.Context, key string) ([]CalendarEvent, error) {
	raw := c.env[key]
	if raw == "" {
		return nil, fmt.Errorf("calendar URL variable %s is missing", key)
	}
	u, err := url.Parse(raw)
	if err != nil || !c.Allowed(u) {
		return nil, fmt.Errorf("calendar URL must use the configured Moodle site")
	}
	if !strings.HasSuffix(u.Path, "/calendar/export_execute.php") {
		return nil, fmt.Errorf("calendar URL must be a Moodle export_execute.php URL")
	}
	r, err := c.Request(ctx, "GET", raw, "", "", "", false)
	if err != nil {
		return nil, err
	}
	return ParseCalendar(r.Body)
}
