package syncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sbot5/lms-mcp/internal/config"
	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/httpx"
	"github.com/sbot5/lms-mcp/internal/moodle"
	"github.com/sbot5/lms-mcp/internal/store"
)

const whoamiJSON = `{"user":{"id":1,"name":"Me"},"courses":[
 {"course":{"id":10,"code":"ABC1234","name":"Algorithms","year":"2026","session":"Semester 2"},"role":{"role":"student"}},
 {"course":{"id":11,"code":"XYZ9999","name":"Old Unit","year":"2025","session":"Semester 1"},"role":{"role":"student"}},
 {"course":{"id":12,"code":"SKIP1000","name":"Dropped","year":"2026","session":"Semester 2"},"role":{"role":"student"}}
]}`

func testProviders(t *testing.T, edURL string) *Providers {
	t.Helper()
	u, _ := url.Parse(edURL)
	doer, err := httpx.New(httpx.Options{Guard: httpx.EdGuard{APIHost: u.Hostname()}, RequestsPerS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Exclude: []string{"SKIP1000"}, CourseCodePattern: config.CoursePattern}
	// exercise the config helpers used by discovery
	return &Providers{
		Cfg: cfg,
		Ed:  ed.NewClient(doer, edURL+"/api", "tok"),
	}
}

func openStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "lms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDiscoverEd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(whoamiJSON))
	}))
	defer srv.Close()

	db := openStore(t)
	p := testProviders(t, srv.URL)
	active, err := DiscoverEd(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != "ABC1234-2026S2" {
		t.Fatalf("active courses = %+v", active)
	}
	all, err := db.ListCourses()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("persisted %d courses, want 3", len(all))
	}
	byID := map[string]store.Course{}
	for _, c := range all {
		byID[c.ID] = c
	}
	if c := byID["ABC1234-2026S2"]; !c.Active || c.Excluded || c.EdCourseID != 10 {
		t.Fatalf("ABC1234 wrong: %+v", c)
	}
	if c := byID["XYZ9999-2025S1"]; c.Active {
		t.Fatal("older-year course should be inactive")
	}
	if c := byID["SKIP1000-2026S2"]; !c.Excluded || c.Active {
		t.Fatalf("excluded course wrong: %+v", c)
	}
}

func TestServiceRunAndLease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(whoamiJSON))
	}))
	defer srv.Close()

	db := openStore(t)
	svc := NewService(db, func() (*Providers, error) { return testProviders(t, srv.URL), nil })

	st := svc.Start("all", false)
	if st.Phase != "running" {
		t.Fatalf("initial phase = %q", st.Phase)
	}
	// A second Start while running must not launch another job.
	st2 := svc.Start("all", false)
	if st2.ID != st.ID {
		t.Fatalf("second Start returned a different job: %q vs %q", st2.ID, st.ID)
	}

	deadline := time.Now().Add(5 * time.Second)
	var final JobStatus
	for time.Now().Before(deadline) {
		s, _ := svc.Status(st.ID)
		if s.Phase == "done" || s.Phase == "error" {
			final = s
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final.Phase != "done" {
		t.Fatalf("job did not finish cleanly: %+v", final)
	}
	if final.Courses != 1 {
		t.Fatalf("expected 1 active course, got %d", final.Courses)
	}
	run, ok, err := db.LastSyncRun("all")
	if err != nil || !ok || run.Status != "ok" {
		t.Fatalf("sync_run not recorded ok: ok=%v run=%+v err=%v", ok, run, err)
	}
	// Lease must be released after the run.
	if ok, _ := db.AcquireLease(leaseName, "someone-else", 60); !ok {
		t.Fatal("lease was not released after the job finished")
	}
}

func TestDiscoverMoodlePairs(t *testing.T) {
	edSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(whoamiJSON))
	}))
	defer edSrv.Close()
	moodleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/user/preferences.php":
			w.Write([]byte(`<script>M.cfg = {"sesskey":"sk1","contextid":1,"userId":2};</script>`))
		case r.Method == "POST" && r.URL.Path == "/lib/ajax/service.php":
			w.Write([]byte(`[{"error":false,"data":{"courses":[
				{"id":500,"shortname":"ABC1234","fullname":"Algorithms","visible":1},
				{"id":501,"shortname":"DEF5678 Databases","fullname":"Databases","visible":1}
			]}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer moodleSrv.Close()

	db := openStore(t)
	p := testProviders(t, edSrv.URL)
	if _, err := DiscoverEd(context.Background(), p, db); err != nil {
		t.Fatal(err)
	}

	base, _ := url.Parse(moodleSrv.URL)
	doer, err := httpx.New(httpx.Options{Guard: httpx.MoodleGuard{Base: base}, RequestsPerS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	p.Moodle = moodle.NewSession(doer, base, "MoodleSession=x", false)

	active, err := DiscoverMoodle(context.Background(), p, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("expected 2 active Moodle courses, got %d", len(active))
	}
	all, _ := db.ListCourses()
	byID := map[string]store.Course{}
	for _, c := range all {
		byID[c.ID] = c
	}
	// ABC1234 was an Ed course; Moodle discovery must pair into the same row.
	paired := byID["ABC1234-2026S2"]
	if paired.EdCourseID != 10 || paired.MoodleCourseID != 500 {
		t.Fatalf("pairing failed: %+v", paired)
	}
	// DEF5678 exists only on Moodle.
	if c := byID["DEF5678"]; c.MoodleCourseID != 501 || c.EdCourseID != 0 {
		t.Fatalf("moodle-only course wrong: %+v", c)
	}
}

// ensure config implements the coder interface used by discovery
var _ courseCoder = (*config.Config)(nil)

func TestMain(m *testing.M) { os.Exit(m.Run()) }
