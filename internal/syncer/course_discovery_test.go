package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/httpx"
	"github.com/sbot5/lms-mcp/internal/moodle"
	"github.com/sbot5/lms-mcp/internal/store"
)

func discoveryEdProviders(t *testing.T, courses []ed.Course) *Providers {
	t.Helper()
	type enrollment struct {
		Course ed.Course `json:"course"`
	}
	enrollments := make([]enrollment, len(courses))
	for i, c := range courses {
		enrollments[i].Course = c
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"courses": enrollments}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return testProviders(t, srv.URL)
}

func discoveryMoodleProvider(t *testing.T, p *Providers, courses []moodle.SessionCourse) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/preferences.php":
			_, _ = w.Write([]byte(`<script>M.cfg = {"sesskey":"sk1"};</script>`))
		case r.Method == http.MethodPost && r.URL.Path == "/lib/ajax/service.php":
			if err := json.NewEncoder(w).Encode([]map[string]any{{"error": false, "data": map[string]any{"courses": courses}}}); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	doer, err := httpx.New(httpx.Options{Guard: httpx.MoodleGuard{Base: base}, RequestsPerS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	p.Moodle = moodle.NewSession(doer, base, "MoodleSession=synthetic", false)
}

func discoveryCoursesByID(t *testing.T, db *store.DB) map[string]store.Course {
	t.Helper()
	courses, err := db.ListCourses()
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]store.Course, len(courses))
	for _, c := range courses {
		byID[c.ID] = c
	}
	return byID
}

func TestDiscoverEdNewestSemester(t *testing.T) {
	p := discoveryEdProviders(t, []ed.Course{
		{ID: 1, Code: "ABC1234", Name: "Synthetic first semester", Year: "2026", Session: "Semester 1"},
		{ID: 2, Code: "DEF5678", Name: "Synthetic second semester", Year: "2026", Session: "Sem 2"},
		{ID: 3, Code: "XYZ9999", Name: "Synthetic prior year", Year: "2025", Session: "S2"},
	})
	db := openStore(t)
	active, err := DiscoverEd(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != "DEF5678-2026S2" {
		t.Fatalf("active courses = %+v, want only latest semester", active)
	}
	all := discoveryCoursesByID(t, db)
	if len(all) != 3 || all["ABC1234-2026S1"].Active || all["XYZ9999-2025S2"].Active {
		t.Fatalf("historical courses must remain stored and inactive: %+v", all)
	}
}

func TestDiscoverEdUnknownSemester(t *testing.T) {
	for _, tc := range []struct {
		name, year, session, term string
	}{
		{"no term", "", "", "unknown"},
		{"unknown session", "2026", "Special session", "2026"},
		{"unrelated digits", "2026", "Special cohort 99", "2026"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := discoveryEdProviders(t, []ed.Course{{ID: 1, Code: "ABC1234", Name: "Synthetic unit", Year: tc.year, Session: tc.session}})
			db := openStore(t)
			active, err := DiscoverEd(context.Background(), p, db)
			if err != nil {
				t.Fatal(err)
			}
			if len(active) != 1 || active[0].Term != tc.term {
				t.Fatalf("unknown term must remain discoverable without invented semester: %+v", active)
			}
		})
	}
}

func TestDiscoverEdUnknownTermsKeepDistinctIdentities(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			p := discoveryEdProviders(t, []ed.Course{
				{ID: 1, Code: "ABC1234", Name: "Synthetic first trimester", Year: "2026", Session: "Trimester 1"},
				{ID: 2, Code: "ABC1234", Name: "Synthetic second trimester", Year: "2026", Session: "Trimester 2"},
			})
			db := openStore(t)
			secondID := "ed-2"
			if legacy {
				secondID = "ABC1234-2026"
				if err := db.UpsertCourse(store.Course{ID: secondID, Code: "ABC1234", Term: "2026", EdCourseID: 2, MoodleCourseID: 500, Active: true}); err != nil {
					t.Fatal(err)
				}
			}
			for round := 0; round < 2; round++ {
				active, err := DiscoverEd(context.Background(), p, db)
				if err != nil {
					t.Fatal(err)
				}
				all := discoveryCoursesByID(t, db)
				if len(active) != 2 || len(all) != 2 || all["ed-1"].EdCourseID != 1 || all[secondID].EdCourseID != 2 {
					t.Fatalf("unknown sessions must keep distinct stable identities: active=%+v stored=%+v", active, all)
				}
				if legacy && all[secondID].MoodleCourseID != 500 {
					t.Fatalf("existing Ed identity lost Moodle pairing: %+v", all)
				}
			}
		})
	}
}

func TestDiscoverCoursesMoodleBeforeEdMergesData(t *testing.T) {
	for _, existingDuplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_duplicate=%v", existingDuplicate), func(t *testing.T) {
			p := discoveryEdProviders(t, []ed.Course{{ID: 1, Code: "ABC1234", Name: "Synthetic unit", Year: "2026", Session: "Semester 2"}})
			db := openStore(t)
			discoveryMoodleProvider(t, p, []moodle.SessionCourse{{ID: 500, ShortName: "ABC1234", FullName: "Synthetic unit"}})
			if _, err := DiscoverMoodle(context.Background(), p, db); err != nil {
				t.Fatal(err)
			}
			item := store.Item{ID: "moodle:cm:123", CourseID: "ABC1234", Provider: "moodle", Title: "Synthetic resource", BodyMD: "Synthetic searchable content", ContentHash: "content", MetaJSON: `{"synthetic":true}`}
			if _, err := db.UpsertItem(item); err != nil {
				t.Fatal(err)
			}
			if err := db.InsertEvent(store.Event{ID: "synthetic-event", CourseID: item.CourseID, ItemID: item.ID, Title: "Synthetic change", Summary: "Preserved history", Baseline: true}); err != nil {
				t.Fatal(err)
			}
			const canonicalID = "ABC1234-2026S2"
			if existingDuplicate {
				if err := db.UpsertCourse(store.Course{ID: canonicalID, Code: "ABC1234", Term: "2026S2", EdCourseID: 1, MoodleCourseID: 500, Active: true}); err != nil {
					t.Fatal(err)
				}
				if _, err := DiscoverMoodle(context.Background(), p, db); err != nil {
					t.Fatal(err)
				}
			}
			for round := 0; round < 3; round++ {
				if _, err := DiscoverEd(context.Background(), p, db); err != nil {
					t.Fatal(err)
				}
				active, err := DiscoverMoodle(context.Background(), p, db)
				if err != nil {
					t.Fatal(err)
				}
				all := discoveryCoursesByID(t, db)
				paired := all[canonicalID]
				if len(all) != 1 || len(active) != 1 || !paired.Active || paired.EdCourseID != 1 || paired.MoodleCourseID != 500 {
					t.Fatalf("Moodle-first discovery must converge on one paired row: active=%+v stored=%+v", active, all)
				}
			}
			item.CourseID = canonicalID
			if got, ok, err := db.GetItem(item.ID); err != nil || !ok || got != item {
				t.Fatalf("course merge lost item metadata: got=%+v ok=%v err=%v", got, ok, err)
			}
			if events, err := db.ListEvents(store.EventFilter{CourseID: canonicalID}); err != nil || len(events) != 1 || events[0].Summary != "Preserved history" || !events[0].Baseline {
				t.Fatalf("course merge lost event history: events=%+v err=%v", events, err)
			}
		})
	}
}

func TestDiscoverEdPreservesMoodlePair(t *testing.T) {
	p := discoveryEdProviders(t, []ed.Course{{ID: 1, Code: "ABC1234", Name: "Synthetic unit", Year: "2026", Session: "Semester 2"}})
	db := openStore(t)
	discoveryMoodleProvider(t, p, []moodle.SessionCourse{{ID: 500, ShortName: "ABC1234", FullName: "Synthetic unit"}})
	if _, err := DiscoverEd(context.Background(), p, db); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverMoodle(context.Background(), p, db); err != nil {
		t.Fatal(err)
	}
	active, err := DiscoverEd(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].MoodleCourseID != 500 {
		t.Fatalf("repeated Ed discovery lost Moodle pairing: %+v", active)
	}
}

func TestDiscoverMoodleUpdatesPairedExclusion(t *testing.T) {
	for _, tc := range []struct {
		name, shortName string
		moodleID        int
	}{
		{"new pair", "ABC1234", 0},
		{"existing pair without code in Moodle name", "Synthetic unit", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := discoveryEdProviders(t, nil)
			db := openStore(t)
			if err := db.UpsertCourse(store.Course{ID: "ABC1234-2026S2", Code: "ABC1234", Term: "2026S2", EdCourseID: 1, MoodleCourseID: tc.moodleID, Active: true}); err != nil {
				t.Fatal(err)
			}
			p.Cfg.Exclude = []string{"ABC1234"}
			discoveryMoodleProvider(t, p, []moodle.SessionCourse{{ID: 500, ShortName: tc.shortName, FullName: "Synthetic unit"}})
			active, err := DiscoverMoodle(context.Background(), p, db)
			if err != nil {
				t.Fatal(err)
			}
			paired := discoveryCoursesByID(t, db)["ABC1234-2026S2"]
			if len(active) != 0 || !paired.Excluded || paired.Active || paired.MoodleCourseID != 500 || paired.EdCourseID != 1 {
				t.Fatalf("paired exclusion was not applied: active=%+v paired=%+v", active, paired)
			}
		})
	}
}

func TestDiscoverMoodleDoesNotPairHistoricalEdCourse(t *testing.T) {
	p := discoveryEdProviders(t, nil)
	db := openStore(t)
	old := store.Course{ID: "ABC1234-2025S2", Code: "ABC1234", Term: "2025S2", EdCourseID: 1}
	if err := db.UpsertCourse(old); err != nil {
		t.Fatal(err)
	}
	discoveryMoodleProvider(t, p, []moodle.SessionCourse{{ID: 500, ShortName: "ABC1234", FullName: "Synthetic current unit"}})
	active, err := DiscoverMoodle(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	all := discoveryCoursesByID(t, db)
	if all[old.ID] != old || len(active) != 1 || active[0].EdCourseID != 0 || active[0].MoodleCourseID != 500 {
		t.Fatalf("current Moodle course must not attach to historical Ed row: active=%+v stored=%+v", active, all)
	}
}

func TestDiscoverMoodlePreservesHistoricalPair(t *testing.T) {
	p := discoveryEdProviders(t, nil)
	db := openStore(t)
	old := store.Course{ID: "ABC1234-2025S2", Code: "ABC1234", Term: "2025S2", EdCourseID: 1, MoodleCourseID: 500}
	current := store.Course{ID: "ABC1234-2026S2", Code: "ABC1234", Term: "2026S2", EdCourseID: 2, Active: true}
	for _, c := range []store.Course{old, current} {
		if err := db.UpsertCourse(c); err != nil {
			t.Fatal(err)
		}
	}
	discoveryMoodleProvider(t, p, []moodle.SessionCourse{{ID: 500, ShortName: "ABC1234", FullName: "Synthetic old unit"}})
	active, err := DiscoverMoodle(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	all := discoveryCoursesByID(t, db)
	// Moodle's in-progress classification is authoritative for its own source;
	// its existing pair remains intact rather than moving to the newer Ed row.
	old.Active = true
	if len(active) != 1 || active[0].ID != old.ID || all[old.ID] != old || all[current.ID] != current {
		t.Fatalf("existing Moodle ID must not be reassigned to newer Ed row: active=%+v stored=%+v", active, all)
	}
}

func TestDiscoverEdRetiresMissingCoursesWithoutLosingMoodlePair(t *testing.T) {
	p := discoveryEdProviders(t, []ed.Course{{ID: 2, Code: "DEF5678", Name: "Synthetic current unit", Year: "2026", Session: "Semester 2"}})
	db := openStore(t)
	paired := store.Course{ID: "ABC1234-2026S2", Code: "ABC1234", Term: "2026S2", EdCourseID: 1, MoodleCourseID: 500, Active: true}
	old := store.Course{ID: "XYZ9999-2025S2", Code: "XYZ9999", Term: "2025S2", EdCourseID: 3, Active: true}
	for _, c := range []store.Course{paired, old} {
		if err := db.UpsertCourse(c); err != nil {
			t.Fatal(err)
		}
	}
	active, err := DiscoverEd(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	paired.Active, old.Active = false, false
	all := discoveryCoursesByID(t, db)
	if len(active) != 1 || all[paired.ID] != paired || all[old.ID] != old {
		t.Fatalf("missing Ed courses must remain inactive with source IDs preserved: active=%+v stored=%+v", active, all)
	}
	discoveryMoodleProvider(t, p, []moodle.SessionCourse{{ID: 500, ShortName: "ABC1234", FullName: "Synthetic current unit"}})
	active, err = DiscoverMoodle(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	paired.Active = true
	all = discoveryCoursesByID(t, db)
	if len(active) != 1 || active[0].ID != paired.ID || all[paired.ID] != paired || all[old.ID] != old {
		t.Fatalf("in-progress Moodle source must reactivate its established pair: active=%+v stored=%+v", active, all)
	}
}

func TestDiscoverMoodleKeepsSameCodeSourcesDistinct(t *testing.T) {
	p := discoveryEdProviders(t, nil)
	db := openStore(t)
	old := store.Course{ID: "ABC1234-2025S2", Code: "ABC1234", Term: "2025S2", EdCourseID: 1, MoodleCourseID: 500}
	current := store.Course{ID: "ABC1234-2026S2", Code: "ABC1234", Term: "2026S2", EdCourseID: 2, Active: true}
	for _, c := range []store.Course{old, current} {
		if err := db.UpsertCourse(c); err != nil {
			t.Fatal(err)
		}
	}
	discoveryMoodleProvider(t, p, []moodle.SessionCourse{
		{ID: 500, ShortName: "ABC1234", FullName: "Synthetic established unit"},
		{ID: 501, ShortName: "ABC1234", FullName: "Synthetic new unit"},
	})
	active, err := DiscoverMoodle(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	old.Active, current.MoodleCourseID = true, 501
	all := discoveryCoursesByID(t, db)
	if len(active) != 2 || all[old.ID] != old || all[current.ID] != current {
		t.Fatalf("same-code Moodle sources must retain separate course identities: active=%+v stored=%+v", active, all)
	}
}

func TestDiscoverMoodleRetiresMissingCourses(t *testing.T) {
	p := discoveryEdProviders(t, nil)
	db := openStore(t)
	for _, c := range []store.Course{
		{ID: "ABC1234", Code: "ABC1234", MoodleCourseID: 500, Active: true},
		{ID: "DEF5678-2026S2", Code: "DEF5678", Term: "2026S2", EdCourseID: 1, MoodleCourseID: 501, Active: true},
	} {
		if err := db.UpsertCourse(c); err != nil {
			t.Fatal(err)
		}
	}
	discoveryMoodleProvider(t, p, nil)
	if _, err := DiscoverMoodle(context.Background(), p, db); err != nil {
		t.Fatal(err)
	}
	all := discoveryCoursesByID(t, db)
	if len(all) != 2 || all["ABC1234"].Active || all["ABC1234"].MoodleCourseID != 500 {
		t.Fatalf("missing Moodle-only course should remain stored and inactive: %+v", all)
	}
	paired := all["DEF5678-2026S2"]
	if !paired.Active || paired.EdCourseID != 1 || paired.MoodleCourseID != 0 {
		t.Fatalf("missing Moodle source must not deactivate current Ed course: %+v", paired)
	}
}
