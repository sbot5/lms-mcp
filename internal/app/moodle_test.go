package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sbot5/lms-mcp/internal/moodle"
)

const sampleICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:deadline-1\r\nSUMMARY:Assignment\\, due\r\nDTSTART;TZID=Australia/Sydney:20261004T235500\r\nDTEND;TZID=Australia/Sydney:20261005T000000\r\nURL:https://example.test/mod/assign/view.php?id=1&authtoken=hidden\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func moodleFixture(t *testing.T, base, auth string) (moodleConfig, moodleCourse, *moodle.Client) {
	t.Helper()
	envPath := filepath.Join(t.TempDir(), ".env")
	text := "MOODLE_TOKEN=fixture-token\nMOODLE_COOKIE=MoodleSession=fixture-session\nMOODLE_CALENDAR=" + base + "/calendar/export_execute.php?authtoken=fixture-calendar\n"
	if err := os.WriteFile(envPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	co := moodleCourse{ID: 7, Code: "TEST", Directory: t.TempDir()}
	cfg := moodleConfig{BaseURL: base, Auth: auth, EnvFile: envPath, Courses: []moodleCourse{co}}
	c, err := newMoodleClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, co, c
}
func stateBytes(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".lms-sync", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMoodleTokenIncrementalAndFailedDownloadPreservesState(t *testing.T) {
	version := int64(1)
	body := "first-file"
	denied := false
	downloads := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webservice/rest/server.php":
			if r.Method != "POST" || r.URL.Query().Get("wstoken") != "" {
				t.Error("token not in POST body")
			}
			r.ParseForm()
			if r.Form.Get("wstoken") != "fixture-token" || r.Form.Get("wsfunction") != "core_course_get_contents" {
				t.Error("incorrect API call")
			}
			json.NewEncoder(w).Encode([]any{map[string]any{"name": "Week 1", "modules": []any{map[string]any{"id": 10, "contents": []any{map[string]any{"type": "file", "filename": "slides.pdf", "filepath": "/", "fileurl": server.URL + "/webservice/pluginfile.php/1/mod_resource/content/1/slides.pdf?token=server-secret", "timemodified": version, "filesize": len(body)}}}}}})
		case "/webservice/pluginfile.php/1/mod_resource/content/1/slides.pdf":
			downloads++
			if r.URL.Query().Get("token") != "fixture-token" {
				t.Error("wrong file token")
			}
			if denied {
				w.WriteHeader(403)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg, co, c := moodleFixture(t, server.URL, "token")
	first, err := syncMoodleCourse(context.Background(), c, cfg, co, false)
	if err != nil || first.Materials != 1 || first.Changes != 1 || first.Downloads != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := syncMoodleCourse(context.Background(), c, cfg, co, false)
	if err != nil || second.Changes != 0 || second.Downloads != 0 || downloads != 1 {
		t.Fatalf("second: %+v %v", second, err)
	}
	version++
	body = "updated-file"
	third, err := syncMoodleCourse(context.Background(), c, cfg, co, false)
	if err != nil || third.Changes != 1 {
		t.Fatalf("update: %+v %v", third, err)
	}
	before := stateBytes(t, co.Directory)
	version++
	denied = true
	if _, err = syncMoodleCourse(context.Background(), c, cfg, co, false); err == nil {
		t.Fatal("failed download accepted")
	}
	if !bytes.Equal(before, stateBytes(t, co.Directory)) {
		t.Fatal("state advanced on failure")
	}
	err = filepath.WalkDir(co.Directory, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			b, _ := os.ReadFile(path)
			for _, secret := range []string{"fixture-token", "server-secret"} {
				if bytes.Contains(b, []byte(secret)) {
					t.Errorf("credential written to %s", e.Name())
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMoodleCookieRedirectAndConditionalDownload(t *testing.T) {
	version := "1"
	bodyCount := 0
	expired := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "MoodleSession=fixture-session" {
			t.Error("missing session")
		}
		if expired {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<body id="page-login-index"><input name="password"></body>`)
			return
		}
		switch r.URL.Path {
		case "/course/view.php":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<body id="page-course-view-topics"><a href="/mod/resource/view.php?id=9">Slides</a><a href="/login/logout.php?sesskey=x">Logout</a><a href="https://other.invalid/pluginfile.php/1/mod_resource/content/1/evil.pdf">Other host</a></body>`)
		case "/mod/resource/view.php":
			http.Redirect(w, r, "/pluginfile.php/1/mod_resource/content/1/slides.pdf", 302)
		case "/pluginfile.php/1/mod_resource/content/1/slides.pdf":
			if r.Header.Get("If-None-Match") == version {
				w.WriteHeader(304)
				return
			}
			bodyCount++
			w.Header().Set("ETag", version)
			w.Header().Set("Content-Type", "application/pdf")
			fmt.Fprint(w, "PDF "+version)
		default:
			t.Errorf("unsafe/unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg, co, c := moodleFixture(t, server.URL, "cookie")
	a, err := syncMoodleCourse(context.Background(), c, cfg, co, false)
	if err != nil || a.Downloads != 1 || bodyCount != 1 {
		t.Fatalf("initial: %+v %v", a, err)
	}
	b, err := syncMoodleCourse(context.Background(), c, cfg, co, false)
	if err != nil || b.Changes != 0 || b.Downloads != 0 || bodyCount != 1 {
		t.Fatalf("conditional cache: %+v %v count=%d", b, err, bodyCount)
	}
	version = "2"
	b, err = syncMoodleCourse(context.Background(), c, cfg, co, false)
	if err != nil || b.Changes != 1 {
		t.Fatalf("same URL update: %+v %v", b, err)
	}
	before := stateBytes(t, co.Directory)
	expired = true
	_, err = syncMoodleCourse(context.Background(), c, cfg, co, false)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("missing session-expired error: %v", err)
	}
	if !bytes.Equal(before, stateBytes(t, co.Directory)) {
		t.Fatal("login replaced checkpoint")
	}
}

func TestMoodleCrossOriginAndCredentialRedaction(t *testing.T) {
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls++
		t.Error("cross-site request reached server")
	}))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/?token=fixture-token", 302)
	}))
	defer server.Close()
	_, _, c := moodleFixture(t, server.URL, "cookie")
	_, err := c.Request(context.Background(), "GET", server.URL+"/course/view.php?id=7", "", "", "", true)
	if err == nil || strings.Contains(err.Error(), "fixture-token") || foreignCalls != 0 {
		t.Fatalf("unsafe redirect: %v, calls %d", err, foreignCalls)
	}
	if _, err = c.Request(context.Background(), "GET", foreign.URL, "", "", "", true); err == nil {
		t.Fatal("foreign direct request accepted")
	}
	u, _ := url.Parse(server.URL + "/moodle/../outside")
	if c.Allowed(u) {
		t.Fatal("path traversal accepted")
	}
}

func TestMoodleCalendarTimezoneAndNoCredentialExports(t *testing.T) {
	events, err := moodle.ParseCalendar([]byte(sampleICS))
	if err != nil || len(events) != 1 {
		t.Fatalf("parse: %v %v", events, err)
	}
	if events[0].Timezone != "Australia/Sydney" || events[0].Start != "20261004T235500" || strings.Contains(events[0].URL, "authtoken") {
		t.Fatalf("calendar semantics: %+v", events[0])
	}
	if events[0].Summary != "Assignment, due" {
		t.Fatalf("escaped summary not decoded: %q", events[0].Summary)
	}
	allDay := strings.ReplaceAll(sampleICS, "DTSTART;TZID=Australia/Sydney:20261004T235500", "DTSTART;VALUE=DATE:20261004")
	events, err = moodle.ParseCalendar([]byte(allDay))
	if err != nil || !events[0].AllDay {
		t.Fatalf("all day lost: %v", err)
	}
	if _, err = moodle.ParseCalendar([]byte("<html>login</html>")); err == nil {
		t.Fatal("HTML accepted as iCalendar")
	}
	if _, err = moodle.ParseCalendar([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n")); err == nil {
		t.Fatal("truncated calendar accepted")
	}
}

func TestMoodleCalendarFailureDoesNotCommitMaterials(t *testing.T) {
	bad := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/course/view.php":
			fmt.Fprint(w, `<body id="page-course-view-topics"></body>`)
		case "/calendar/export_execute.php":
			if r.Header.Get("Cookie") != "" {
				t.Error("cookie sent with calendar URL")
			}
			if bad {
				fmt.Fprint(w, "<html>expired</html>")
			} else {
				fmt.Fprint(w, sampleICS)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg, co, c := moodleFixture(t, server.URL, "cookie")
	co.CalendarEnv = "MOODLE_CALENDAR"
	if _, err := syncMoodleCourse(context.Background(), c, cfg, co, false); err != nil {
		t.Fatal(err)
	}
	fullCfg := syncConfig{Moodle: &cfg}
	fullCfg.Moodle.Courses = []moodleCourse{co}
	cached, err := readMoodleCalendar(fullCfg, co.Code)
	if err != nil || len(cached.Events) != 1 {
		t.Fatalf("cached calendar: %+v %v", cached, err)
	}
	before := stateBytes(t, co.Directory)
	bad = true
	if _, err := syncMoodleCourse(context.Background(), c, cfg, co, false); err == nil {
		t.Fatal("bad calendar accepted")
	}
	if !bytes.Equal(before, stateBytes(t, co.Directory)) {
		t.Fatal("calendar failure advanced state")
	}
}

func TestMoodleRecoveryJournalAndServiceError(t *testing.T) {
	errorBody := false
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "rest/server.php") {
			json.NewEncoder(w).Encode([]any{map[string]any{"modules": []any{map[string]any{"id": 10, "contents": []any{map[string]any{"type": "file", "filename": "a.txt", "filepath": "/", "fileurl": server.URL + "/webservice/pluginfile.php/a.txt", "timemodified": 1}}}}}})
			return
		}
		if errorBody {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"exception":"moodle_exception","errorcode":"invalidtoken","error":"fixture-token"}`)
		} else {
			fmt.Fprint(w, "remote-v3")
		}
	}))
	defer server.Close()
	cfg, co, c := moodleFixture(t, server.URL, "token")
	storage := co.storage(cfg)
	s, _ := readState(storage)
	key := "module-10:/a.txt"
	rel := filepath.Join("files", digest([]byte(key))[:16]+"-a.txt")
	s.MoodleFiles = map[string]moodleFileState{key: {Path: rel, Hash: digest([]byte("v1"))}}
	if err := writeOutputs(storage, &s, map[string][]byte{rel: []byte("v1")}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(co.Directory, rel), []byte("v2"), 0600)
	j, _ := json.Marshal(map[string][]string{rel: {digest([]byte("v2"))}})
	os.WriteFile(filepath.Join(co.Directory, ".lms-sync", "pending.json"), j, 0600)
	if _, err := syncMoodleCourse(context.Background(), c, cfg, co, true); err != nil {
		t.Fatalf("interrupted write recovery: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(co.Directory, rel))
	if string(b) != "remote-v3" {
		t.Fatal("recovery missed current body")
	}
	before := stateBytes(t, co.Directory)
	errorBody = true
	_, err := syncMoodleCourse(context.Background(), c, cfg, co, true)
	if err == nil || strings.Contains(err.Error(), "fixture-token") {
		t.Fatalf("service error accepted/leaked: %v", err)
	}
	if !bytes.Equal(before, stateBytes(t, co.Directory)) {
		t.Fatal("checkpoint overwritten with service error")
	}
}

func TestMoodleOnlyConfigOfflineCLIAndSharedSync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<body id="page-course-view-topics"></body>`)
	}))
	defer server.Close()
	mc, co, _ := moodleFixture(t, server.URL, "cookie")
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := syncConfig{Moodle: &mc}
	if err := saveNewConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range [][]string{{"config", "validate"}, {"status"}, {"whats-new"}, {"sync"}} {
		var out bytes.Buffer
		args := append(cmd, "-config", path)
		if err := Run(context.Background(), args, strings.NewReader(""), &out, io.Discard); err != nil {
			t.Fatalf("%v: %v", cmd, err)
		}
	}
	normalized, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	news, err := whatsNew(normalized, newsInput{Course: "moodle:" + co.Code})
	if err != nil || len(news.LastSuccess) != 1 {
		t.Fatalf("Moodle freshness missing: %v %v", news, err)
	}
	// Failure in an Ed account must not suppress a healthy Moodle course.
	normalized.Courses = []courseConfig{testCourse(t)}
	r, err := syncAll(context.Background(), nil, normalized, false)
	if err == nil || len(r.Courses) != 2 || r.Courses[1].Error != "" {
		t.Fatalf("provider isolation: %+v %v", r, err)
	}
}

func TestMoodleConfigRejectsCredentialsAndOverlappingFolders(t *testing.T) {
	for _, base := range []string{"http://remote.example", "https://user:pass@example.test", "https://example.test?token=secret"} {
		cfg := syncConfig{Moodle: &moodleConfig{BaseURL: base, Auth: "cookie", EnvFile: ".env", Courses: []moodleCourse{{ID: 1, Code: "T", Directory: "out"}}}}
		if _, err := normalizeConfig(cfg, filepath.Join(t.TempDir(), "config.json")); err == nil {
			t.Fatalf("unsafe base accepted %s", base)
		}
	}
	cfg := syncConfig{Courses: []courseConfig{{ID: 1, Code: "E", Directory: "out"}}, Moodle: &moodleConfig{BaseURL: "https://example.test", Auth: "cookie", EnvFile: ".env", Courses: []moodleCourse{{ID: 1, Code: "M", Directory: "out/moodle"}}}}
	if _, err := normalizeConfig(cfg, filepath.Join(t.TempDir(), "config.json")); err == nil {
		t.Fatal("overlapping provider directories accepted")
	}
}
