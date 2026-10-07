package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sbot5/lms-mcp/internal/config"
	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/httpx"
	"github.com/sbot5/lms-mcp/internal/store"
)

type materialFixture struct {
	mu                         sync.Mutex
	routes                     map[string]any
	statuses                   map[string]int
	requests                   map[string]int
	conditional, unconditional int
	fileBytes                  string
	fileQueries                []string
	srv                        *httptest.Server
	p                          *Providers
	course                     store.Course
}

func TestOwnQuizSelectionSurvivesProjection(t *testing.T) {
	f := newMaterialFixture(t)
	f.routes["/api/lessons/slides/203/questions/responses"] = map[string]any{"responses": []any{map[string]any{"id": 601, "user_id": 1, "question_id": 501, "data": map[string]any{"selection": 1}}}}
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	quiz := materialByID(t, snapshot, "ed:slide:203")
	if !strings.Contains(quiz.MetaJSON, `"selection":1`) || !strings.Contains(quiz.BodyMD, `"selection": 1`) {
		t.Fatal("own quiz selection omitted", quiz.MetaJSON, quiz.BodyMD)
	}
}

func TestMarkAvailabilityFollowsLatestSelectionInEitherOrder(t *testing.T) {
	for _, published := range []bool{true, false} {
		for _, reverse := range []bool{true, false} {
			t.Run(fmt.Sprintf("published=%v/reverse=%v", published, reverse), func(t *testing.T) {
				f := newMaterialFixture(t)
				rows := []any{map[string]any{"id": 702, "user_id": 1, "lesson_mark_id": 903, "is_released": published}, map[string]any{"id": 701, "user_id": 1, "lesson_mark_id": 902, "is_released": !published}}
				if reverse {
					rows[0], rows[1] = rows[1], rows[0]
				}
				f.routes["/api/users/1/challenges/301/submissions"] = map[string]any{"submissions": rows}
				f.routes["/api/lesson_marks/903"] = map[string]any{"lesson_mark": map[string]any{"id": 903, "user_id": 1, "score": 8, "is_released": published, "updated_at": "2030-01-03T00:00:00Z"}}
				f.routes["/api/lesson_marks/902"] = map[string]any{"lesson_mark": map[string]any{"id": 902, "user_id": 1, "score": 2, "is_released": !published, "updated_at": "2030-01-02T00:00:00Z"}}
				snap, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
				if err != nil {
					t.Fatal(err)
				}
				defer batch.Rollback()
				item := materialByID(t, snap, "ed:slide:204")
				want := "withheld"
				if published {
					want = "available"
				}
				if !strings.Contains(item.MetaJSON, `"marks_availability":"`+want+`"`) {
					t.Fatal("status does not follow selected mark", item.MetaJSON)
				}
				found := false
				for _, g := range snap.Grades {
					if g.ItemKey == item.ID {
						found = true
						if !published || g.Grade != "8" {
							t.Fatal("incorrect current score", g)
						}
					}
				}
				if found != published {
					t.Fatal("current grade availability mismatch")
				}
			})
		}
	}
}

func newMaterialFixture(t *testing.T) *materialFixture {
	t.Helper()
	f := &materialFixture{routes: map[string]any{}, statuses: map[string]int{}, requests: map[string]int{}, fileBytes: "%PDF synthetic"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests[r.URL.Path]++
		if r.Method != http.MethodGet || r.URL.Query().Has("view") {
			t.Error("side-effecting request")
		}
		if status := f.statuses[r.URL.Path]; status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{}`)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/files/") || strings.HasSuffix(r.URL.Path, "/download") {
			f.fileQueries = append(f.fileQueries, r.URL.RawQuery)
			if r.Header.Get("If-None-Match") == `"synthetic-tag"` && r.Header.Get("If-Modified-Since") == "Tue, 01 Jan 2030 00:00:00 GMT" {
				f.conditional++
				w.WriteHeader(http.StatusNotModified)
				return
			}
			f.unconditional++
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("ETag", `"synthetic-tag"`)
			w.Header().Set("Last-Modified", "Tue, 01 Jan 2030 00:00:00 GMT")
			fmt.Fprint(w, f.fileBytes)
			return
		}
		if value, ok := f.routes[r.URL.Path]; ok {
			json.NewEncoder(w).Encode(value)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(f.srv.Close)
	base, _ := url.Parse(f.srv.URL)
	doer, err := httpx.New(httpx.Options{Guard: httpx.EdGuard{APIHost: base.Hostname()}, UserAgent: "lms-mcp/test", RequestsPerS: 10000})
	if err != nil {
		t.Fatal(err)
	}
	f.p = &Providers{Cfg: &config.Config{DataDir: t.TempDir()}, Ed: ed.NewClient(doer, f.srv.URL+"/api", "synthetic-credential")}
	f.course = store.Course{ID: "ABC1234-2030S1", Code: "ABC1234", Term: "2030S1", EdCourseID: 10}
	asset := f.srv.URL + "/api/files/slides.pdf?token=must-not-persist&ticket=must-not-persist&part=1"
	assetXML := strings.ReplaceAll(asset, "&", "&amp;")
	f.routes["/api/user"] = map[string]any{"user": map[string]any{"id": 1}, "courses": []any{}}
	f.routes["/api/courses/10/lessons"] = map[string]any{"lessons": []any{map[string]any{"id": 101, "module_id": 11, "title": "Synthetic lesson", "state": "active", "openable": true, "status": "unattempted", "available_at": "2030-01-01T10:00:00+10:00", "due_at": "2030-01-08T10:00:00+10:00", "locked_at": nil, "solutions_at": "2000-01-01T00:00:00Z"}}, "modules": []any{map[string]any{"id": 11, "name": "Synthetic module"}}}
	f.routes["/api/lessons/101"] = map[string]any{"lesson": map[string]any{"id": 101, "title": "Synthetic lesson", "slides": []any{
		map[string]any{"id": 201, "title": "Reading", "type": "document", "content": `<paragraph>Synthetic reading <file url="` + assetXML + `" filename="slides.pdf"/></paragraph>`},
		map[string]any{"id": 202, "title": "Slides", "filename": "slides.pdf", "type": "pdf", "file_url": asset},
		map[string]any{"id": 203, "title": "Quiz", "type": "quiz"},
		map[string]any{"id": 204, "title": "Code", "type": "code", "challenge_id": 301},
		map[string]any{"id": 205, "title": "Video", "type": "video", "video_url": f.srv.URL + "/never.mp4?token=must-not-persist"},
	}}}
	f.routes["/api/lessons/slides/203/questions"] = map[string]any{"questions": []any{map[string]any{"id": 501, "is_released": true, "data": map[string]any{"type": "multiple-choice", "content": "<paragraph>Synthetic question?</paragraph>", "answers": []any{"first", "second"}, "solution": 1, "explanation": "Synthetic explanation", "ticket": "must-not-persist"}}}}
	f.routes["/api/lessons/slides/203/questions/responses"] = map[string]any{"responses": []any{map[string]any{"id": 601, "user_id": 1, "question_id": 501, "data": map[string]any{"answer": 1, "jwt": "must-not-persist"}}}}
	f.routes["/api/challenges/301"] = map[string]any{"challenge": map[string]any{"id": 301, "content": "<paragraph>Synthetic coding task.</paragraph>", "ticket": "must-not-persist"}}
	f.routes["/api/users/1/challenges/301/submissions"] = map[string]any{"submissions": []any{map[string]any{"id": 701, "user_id": 1, "status": "completed", "code": "print('synthetic')", "lesson_mark_id": 901, "is_released": true}}}
	f.routes["/api/lessons/101/attempts/1"] = map[string]any{"attempt": map[string]any{"id": 801, "user_id": 1, "lesson_mark_id": 901, "status": "completed", "is_released": true}}
	f.routes["/api/lesson_marks/901"] = map[string]any{"lesson_mark": map[string]any{"id": 901, "user_id": 1, "auto_mark": 3, "rubric_mark": 2, "max_points": 10, "comment": "Synthetic feedback", "is_released": true, "jwt": "must-not-persist"}}
	f.routes["/api/courses/10/resources"] = map[string]any{"resources": []any{
		map[string]any{"id": 401, "name": "Synthetic handout", "extension": "pdf", "category": "Handouts"},
		map[string]any{"id": 402, "name": "Recording", "link": f.srv.URL + "/never.mp4?ticket=must-not-persist", "extension": "mp4"},
	}}
	return f
}

func materialByID(t *testing.T, snapshot store.ContentSnapshot, id string) store.Item {
	t.Helper()
	for _, item := range snapshot.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("missing item %s", id)
	return store.Item{}
}

func TestEdMaterialsStagedCompleteAndRepeatable(t *testing.T) {
	f := newMaterialFixture(t)
	first, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	if len(first.Items) != 8 || len(first.Grades) != 2 || len(first.Deadlines) != 1 || !slices.Contains(first.Kinds, "lesson") || !slices.Contains(first.Kinds, "slide") || !slices.Contains(first.Kinds, "resource") {
		t.Fatalf("snapshot=%+v", first)
	}
	itemIDs := map[string]bool{}
	for _, item := range first.Items {
		itemIDs[item.ID] = true
		if item.CourseID != f.course.ID || item.Provider != "ed" || item.ContentHash == "" {
			t.Fatal(item)
		}
	}
	for _, grade := range first.Grades {
		if !itemIDs[grade.ItemKey] || grade.Grade != "5" || grade.GradeMax != "10" {
			t.Fatal(grade)
		}
	}
	deadline := first.Deadlines[0]
	wantDue, _ := time.Parse(time.RFC3339, "2030-01-08T00:00:00Z")
	if deadline.ItemID != "ed:lesson:101" || deadline.DueAt != wantDue.Unix() || deadline.SubmissionStatus != "completed" || !deadline.Completed {
		t.Fatal(deadline)
	}
	for _, file := range first.Files {
		if file.Status == "ok" {
			if file.SHA256 == "" || file.Size == 0 {
				t.Fatal(file)
			}
			if _, err := os.Stat(file.LocalPath); !os.IsNotExist(err) {
				t.Fatalf("published while collecting: %s", file.LocalPath)
			}
		}
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "must-not-persist") || strings.Contains(string(encoded), "synthetic-credential") {
		t.Fatal("credential fields escaped")
	}
	quiz := materialByID(t, first, "ed:slide:203")
	if !strings.Contains(quiz.BodyMD, "Synthetic question?") || !strings.Contains(quiz.MetaJSON, "released_answers") || !strings.Contains(quiz.MetaJSON, "responses") {
		t.Fatal(quiz)
	}
	code := materialByID(t, first, "ed:slide:204")
	if code.ParentID != "ed:lesson:101" || !strings.Contains(code.BodyMD, "Synthetic coding task") || !strings.Contains(code.MetaJSON, "submissions") {
		t.Fatal(code)
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := batch.Finish(); err != nil {
		t.Fatal(err)
	}
	for _, file := range first.Files {
		if file.Status == "ok" {
			data, err := os.ReadFile(file.LocalPath)
			if err != nil || int64(len(data)) != file.Size {
				t.Fatalf("published file: %v", err)
			}
		}
	}
	second, next, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Rollback()
	for _, item := range second.Items {
		previous := materialByID(t, first, item.ID)
		if item.ContentHash != previous.ContentHash {
			t.Fatalf("unstable hash: %s", item.ID)
		}
	}
	if f.conditional != 2 || f.unconditional != 2 || f.requests["/never.mp4"] != 0 {
		t.Fatalf("conditional=%d unconditional=%d video=%d", f.conditional, f.unconditional, f.requests["/never.mp4"])
	}
	if err := next.Commit(); err != nil {
		t.Fatal(err)
	}
	next.Finish()
	_, full, err := collectEdMaterials(context.Background(), f.p, f.course, true)
	if err != nil {
		t.Fatal(err)
	}
	full.Rollback()
	if f.unconditional != 4 {
		t.Fatalf("full failed to redownload: %d", f.unconditional)
	}
}

func TestEdMaterialsUnavailableInventoriesAndScheduledLesson(t *testing.T) {
	f := newMaterialFixture(t)
	f.statuses["/api/courses/10/resources"] = 403
	f.routes["/api/courses/10/lessons"] = map[string]any{"lessons": []any{map[string]any{"id": 101, "title": "Scheduled lesson", "state": "scheduled", "openable": false, "status": "unattempted", "available_at": nil, "locked_at": nil, "due_at": "2030-01-08T00:00:00Z"}}}
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	if len(snapshot.Items) != 1 || !slices.Contains(snapshot.Kinds, "lesson") || slices.Contains(snapshot.Kinds, "slide") || slices.Contains(snapshot.Kinds, "resource") || len(snapshot.Warnings) != 1 {
		t.Fatal(snapshot)
	}
	if !strings.Contains(snapshot.Items[0].MetaJSON, `"content_unavailable":true`) || f.requests["/api/lessons/101"] != 0 || len(snapshot.Files) != 0 {
		t.Fatal("opened scheduled content or discarded availability")
	}
	f.statuses["/api/courses/10/lessons"] = 404
	snapshot, other, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback()
	if len(snapshot.Kinds) != 0 || len(snapshot.Warnings) != 2 {
		t.Fatal(snapshot)
	}
}

func TestEdMaterialsVisibleFailuresPreserveOldFiles(t *testing.T) {
	for _, endpoint := range []string{"/api/lessons/101", "/api/files/slides.pdf", "/api/resources/401/download"} {
		t.Run(endpoint, func(t *testing.T) {
			f := newMaterialFixture(t)
			original, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := batch.Commit(); err != nil {
				t.Fatal(err)
			}
			batch.Finish()
			before := map[string]string{}
			for _, file := range original.Files {
				if file.Status == "ok" {
					data, _ := os.ReadFile(file.LocalPath)
					before[file.LocalPath] = string(data)
				}
			}
			f.statuses[endpoint] = 403
			f.fileBytes = "%PDF changed"
			_, failed, err := collectEdMaterials(context.Background(), f.p, f.course, true)
			if err == nil || failed != nil {
				t.Fatalf("visible failure did not abort: %v", err)
			}
			for path, text := range before {
				data, _ := os.ReadFile(path)
				if string(data) != text {
					t.Fatal("failed collection replaced previous bytes")
				}
			}
			stages, _ := filepath.Glob(f.p.Cfg.DataPath("ABC1234-2030S1-ed-10", "ed", ".stage-*"))
			if len(stages) != 0 {
				t.Fatal("failed staging leaked", stages)
			}
		})
	}
}

func TestEdMaterialsUnknownAndUnreleasedGrades(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mark  any
		want  int
		score string
	}{
		{"unknown", map[string]any{"id": 901, "user_id": 1, "is_released": true}, 0, ""},
		{"hidden", map[string]any{"id": 901, "user_id": 1, "score": 9, "is_released": false}, 0, ""},
		{"zero", map[string]any{"id": 901, "user_id": 1, "score": 0, "is_released": true}, 2, "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaterialFixture(t)
			f.routes["/api/lesson_marks/901"] = map[string]any{"lesson_mark": tc.mark}
			snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
			if err != nil {
				t.Fatal(err)
			}
			defer batch.Rollback()
			if len(snapshot.Grades) != tc.want {
				t.Fatal(snapshot.Grades)
			}
			for _, grade := range snapshot.Grades {
				if grade.Grade != tc.score {
					t.Fatal(grade)
				}
			}
		})
	}
}

func TestEdMaterialsMissingQuizInventoryPreservesSlides(t *testing.T) {
	f := newMaterialFixture(t)
	f.statuses["/api/lessons/slides/203/questions"] = 404
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	if slices.Contains(snapshot.Kinds, "slide") || len(snapshot.Warnings) != 1 {
		t.Fatal(snapshot)
	}
	lesson := materialByID(t, snapshot, "ed:lesson:101")
	quiz := materialByID(t, snapshot, "ed:slide:203")
	if !strings.Contains(lesson.MetaJSON, `"content_unavailable":true`) || !strings.Contains(quiz.MetaJSON, `"content_unavailable":true`) {
		t.Fatal("unavailable quiz would replace old content")
	}
	for _, file := range snapshot.Files {
		if file.ID == "ed:lesson:101:markdown" {
			t.Fatal("incomplete lesson exported")
		}
	}
}

func TestEdMaterialsOwnUserAndLocalEdits(t *testing.T) {
	for _, endpoint := range []string{"/api/users/1/challenges/301/submissions", "/api/lessons/101/attempts/1", "/api/lesson_marks/901", "/api/lessons/slides/203/questions/responses"} {
		t.Run(endpoint, func(t *testing.T) {
			f := newMaterialFixture(t)
			switch endpoint {
			case "/api/users/1/challenges/301/submissions":
				f.routes[endpoint] = map[string]any{"submissions": []any{map[string]any{"id": 701, "user_id": 2}}}
			case "/api/lessons/101/attempts/1":
				f.routes[endpoint] = map[string]any{"attempt": map[string]any{"id": 801, "user_id": 2}}
			case "/api/lesson_marks/901":
				f.routes[endpoint] = map[string]any{"lesson_mark": map[string]any{"id": 901, "user_id": 2, "score": 5, "is_released": true}}
			default:
				f.routes[endpoint] = map[string]any{"responses": []any{map[string]any{"id": 601, "user_id": 2}}}
			}
			_, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
			if err == nil || batch != nil {
				t.Fatal("accepted another user's data")
			}
		})
	}
	f := newMaterialFixture(t)
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	batch.Finish()
	for _, file := range snapshot.Files {
		if file.Status == "ok" && file.Name == "lesson.md" {
			if err := os.WriteFile(file.LocalPath, []byte("local edits"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, other, err := collectEdMaterials(context.Background(), f.p, f.course, true)
	if err == nil || other != nil {
		t.Fatal("overwrote locally edited lesson")
	}
}

func TestEdMaterialReleasedAnswersAndURLScrubbing(t *testing.T) {
	question := ed.Document{"id": 1, "data": map[string]any{"answers": []any{map[string]any{"text": "choice", "correct": true}}, "solution": 1, "explanation": "unreleased solution", "jwt": "must-not-persist"}}
	encoded, _ := json.Marshal(edMaterialQuestion(question, false))
	if strings.Contains(string(encoded), "solution") || strings.Contains(string(encoded), "correct") || strings.Contains(string(encoded), "must-not-persist") {
		t.Fatal(string(encoded))
	}
	encoded, _ = json.Marshal(edMaterialQuestion(question, true))
	if !strings.Contains(string(encoded), `"solution":1`) {
		t.Fatal(string(encoded))
	}
	clean := edMaterialCleanURL("https://user:pass@example.invalid/file.pdf?token=must-not-persist&ticket=must-not-persist&jwt=must-not-persist&part=1#must-not-persist")
	if clean != "https://example.invalid/file.pdf?part=1" {
		t.Fatal(clean)
	}
	if edMaterialTime("invalid") != 0 || edMaterialID(1.5) != 0 || edMaterialNumber(nil) != "" {
		t.Fatal("accepted malformed metadata")
	}
}

func TestEdMaterialsDetailAvailabilityTransition(t *testing.T) {
	f := newMaterialFixture(t)
	f.routes["/api/lessons/101"] = map[string]any{"lesson": map[string]any{"id": 101, "title": "Scheduled lesson", "state": "scheduled", "openable": false}}
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	if slices.Contains(snapshot.Kinds, "slide") || !strings.Contains(materialByID(t, snapshot, "ed:lesson:101").MetaJSON, `"content_unavailable":true`) {
		t.Fatal(snapshot)
	}
}

func TestEdMaterialsLatestGradeIgnoresSubmissionOrder(t *testing.T) {
	f := newMaterialFixture(t)
	f.routes["/api/users/1/challenges/301/submissions"] = map[string]any{"submissions": []any{
		map[string]any{"id": 702, "user_id": 1, "lesson_mark_id": 903, "is_released": true},
		map[string]any{"id": 701, "user_id": 1, "lesson_mark_id": 902, "is_released": true},
	}}
	f.routes["/api/lesson_marks/903"] = map[string]any{"lesson_mark": map[string]any{"id": 903, "user_id": 1, "score": 8, "is_released": true, "updated_at": "2030-01-03T00:00:00Z"}}
	f.routes["/api/lesson_marks/902"] = map[string]any{"lesson_mark": map[string]any{"id": 902, "user_id": 1, "score": 2, "is_released": true, "updated_at": "2030-01-02T00:00:00Z"}}
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	for _, grade := range snapshot.Grades {
		if grade.ItemKey == "ed:slide:204" && grade.Grade != "8" {
			t.Fatal(grade)
		}
	}
}

func TestEdMaterialsAssessmentProjectionProtectsEveryExport(t *testing.T) {
	f := newMaterialFixture(t)
	privateFields := func() map[string]any {
		return map[string]any{
			"api_key": "PRIVATE_SENTINEL", "private_key": "PRIVATE_SENTINEL", "session_cookie": "PRIVATE_SENTINEL",
			"credential":        map[string]any{"value": "PRIVATE_SENTINEL"},
			"user":              map[string]any{"name": "privateStudent", "email": "privateStudent@example.invalid"},
			"future_permission": map[string]any{"data": map[string]any{"password": "PRIVATE_SENTINEL"}},
		}
	}
	question := privateFields()
	question["id"] = 501
	question["is_released"] = true
	data := privateFields()
	data["type"] = "multiple-choice"
	data["content"] = "<paragraph>Visible question.</paragraph>"
	choice := privateFields()
	choice["index"] = 0
	choice["text"] = "Visible choice"
	choice["value"] = privateFields()
	choice["value"].(map[string]any)["choice_id"] = 1
	data["answers"] = []any{choice}
	data["solution"] = privateFields()
	data["solution"].(map[string]any)["value"] = 1
	data["explanation"] = "Visible published explanation"
	question["data"] = data
	response := privateFields()
	response["id"] = 601
	response["user_id"] = 1
	response["question_id"] = 501
	responseData := privateFields()
	answer := privateFields()
	answer["value"] = 1
	responseData["answer"] = answer
	responseData["code"] = "print('visible own answer')"
	responseData["choices"] = []any{choice}
	responseData["text"] = map[string]any{"value": "PRIVATE_SENTINEL"}
	response["data"] = responseData
	f.routes["/api/lessons/slides/203/questions"] = map[string]any{"questions": []any{question}}
	f.routes["/api/lessons/slides/203/questions/responses"] = map[string]any{"responses": []any{response}}
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	encoded, _ := json.Marshal(snapshot)
	for _, secret := range []string{"PRIVATE_SENTINEL", "privateStudent"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("assessment secret reached a stored/exported item: %s", secret)
		}
	}
	quiz := materialByID(t, snapshot, "ed:slide:203")
	lesson := materialByID(t, snapshot, "ed:lesson:101")
	for _, required := range []string{"Visible question", "Visible choice", "Visible published explanation", "visible own answer"} {
		if !strings.Contains(quiz.BodyMD, required) || !strings.Contains(lesson.BodyMD, required) {
			t.Fatalf("necessary assessment field removed: %s", required)
		}
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := batch.Finish(); err != nil {
		t.Fatal(err)
	}
	for _, file := range snapshot.Files {
		if file.Name == "lesson.md" {
			contents, err := os.ReadFile(file.LocalPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(contents), "PRIVATE_SENTINEL") || strings.Contains(string(contents), "privateStudent") {
				t.Fatal("generated lesson exported raw private data")
			}
		}
	}
}

func TestEdMaterialsOwnResponseDoesNotReleaseSolutions(t *testing.T) {
	f := newMaterialFixture(t)
	f.routes["/api/lessons/slides/203/questions/responses"] = map[string]any{"responses": []any{map[string]any{
		"id": 601, "question_id": 501, "user_id": 1, "is_released": false,
		"data": map[string]any{"answer": 1, "code": "print('own code')", "solution": "UNRELEASED_SENTINEL", "explanation": "UNRELEASED_SENTINEL", "correct_answer": "UNRELEASED_SENTINEL"},
	}}}
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	all, _ := json.Marshal(snapshot)
	if strings.Contains(string(all), "UNRELEASED_SENTINEL") {
		t.Fatal("reading an own response released hidden solutions")
	}
	quiz := materialByID(t, snapshot, "ed:slide:203")
	if !strings.Contains(quiz.BodyMD, "own code") {
		t.Fatal("own code was dropped with unpublished solutions")
	}
}

func TestEdMaterialsExportURLsUseSafeQueryWhitelist(t *testing.T) {
	f := newMaterialFixture(t)
	raw := f.srv.URL + "/api/files/signed.pdf?SessKey=PRIVATE_SENTINEL&API_KEY=PRIVATE_SENTINEL&Password=PRIVATE_SENTINEL&SESSION_COOKIE=PRIVATE_SENTINEL&credential=PRIVATE_SENTINEL&privateStudent=PRIVATE_SENTINEL&Part=2&id=10&dl=1"
	encodedURL := strings.ReplaceAll(raw, "&", "&amp;")
	lesson := f.routes["/api/lessons/101"].(map[string]any)["lesson"].(map[string]any)
	lesson["content"] = `<paragraph><link href="` + encodedURL + `">Signed link</link></paragraph>`
	slides := lesson["slides"].([]any)
	slides[0].(map[string]any)["content"] = `<paragraph><file url="` + encodedURL + `" filename="signed.pdf"/></paragraph>`
	slides[1].(map[string]any)["file_url"] = raw
	slides[1].(map[string]any)["filename"] = "signed.pdf"
	slides[4].(map[string]any)["video_url"] = raw
	questions := f.routes["/api/lessons/slides/203/questions"].(map[string]any)["questions"].([]any)
	questions[0].(map[string]any)["data"].(map[string]any)["explanation"] = "Published link: " + encodedURL
	responses := f.routes["/api/lessons/slides/203/questions/responses"].(map[string]any)["responses"].([]any)
	responses[0].(map[string]any)["data"].(map[string]any)["text"] = encodedURL
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	all, _ := json.Marshal(snapshot)
	if strings.Contains(string(all), "PRIVATE_SENTINEL") || strings.Contains(string(all), "privateStudent") {
		t.Fatal("URL credentials reached BodyMD, metadata, or SourceURL")
	}
	want := f.srv.URL + "/api/files/signed.pdf?dl=1&id=10&part=2"
	matched := false
	for _, file := range snapshot.Files {
		if file.SourceURL == want {
			matched = true
		}
	}
	if !matched {
		t.Fatal("safe display parameters were lost")
	}
	f.mu.Lock()
	originalSent := false
	for _, query := range f.fileQueries {
		if strings.Contains(query, "SessKey=PRIVATE_SENTINEL") && strings.Contains(query, "API_KEY=PRIVATE_SENTINEL") {
			originalSent = true
		}
	}
	f.mu.Unlock()
	if !originalSent {
		t.Fatal("download did not retain its original signed request URL")
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := batch.Finish(); err != nil {
		t.Fatal(err)
	}
	for _, file := range snapshot.Files {
		if file.Name == "lesson.md" {
			contents, _ := os.ReadFile(file.LocalPath)
			if strings.Contains(string(contents), "PRIVATE_SENTINEL") {
				t.Fatal("lesson.md leaked URL credentials")
			}
			manifest, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file.LocalPath)))), ".mirror.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(manifest), "PRIVATE_SENTINEL") {
				t.Fatal("manifest leaked URL credentials")
			}
		}
	}
}

func TestEdMaterialURLParameterAliasesAndMalformedValues(t *testing.T) {
	for _, raw := range []string{
		"https://example.invalid/file?ID=1&Part=2&DL=TRUE&SeSsKeY=PRIVATE_SENTINEL&Api_Key=PRIVATE_SENTINEL&Password=PRIVATE_SENTINEL&COOKIE=PRIVATE_SENTINEL",
		"HTTPS://example.invalid/file?ID=1&amp;Part=2&amp;DL=TRUE&amp;session_cookie=PRIVATE_SENTINEL&amp;credential=PRIVATE_SENTINEL",
	} {
		if clean := edMaterialCleanURL(raw); clean != "https://example.invalid/file?dl=true&id=1&part=2" {
			t.Fatal(clean)
		}
	}
	if clean := edMaterialCleanURL("https://example.invalid/file?id=PRIVATE_SENTINEL&part=PRIVATE_SENTINEL&dl=PRIVATE_SENTINEL&unknown=PRIVATE_SENTINEL"); clean != "https://example.invalid/file" {
		t.Fatal(clean)
	}
	for range 20 {
		if clean := edMaterialCleanURL("https://example.invalid/file?ID=2&id=1"); clean != "https://example.invalid/file?id=1&id=2" {
			t.Fatal("unstable normalized parameters", clean)
		}
	}
}

func TestEdMaterialsExplicitFeatureAvailability(t *testing.T) {
	for _, tc := range []struct {
		name               string
		features           any
		lessons, resources bool
	}{
		{"unknown", nil, true, true},
		{"null flags", map[string]any{"lessons": nil, "resources": nil}, true, true},
		{"all disabled", map[string]any{"lessons": false, "resources": false}, false, false},
		{"resources disabled", map[string]any{"resources": false}, true, false},
		{"lessons disabled", map[string]any{"lessons": false, "resources": true}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaterialFixture(t)
			f.routes["/api/user"] = map[string]any{"user": map[string]any{"id": 1}, "courses": []any{map[string]any{"course": map[string]any{"id": 10, "features": tc.features}, "role": map[string]any{"role": "student"}}}}
			snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
			if err != nil {
				t.Fatal(err)
			}
			defer batch.Rollback()
			if slices.Contains(snapshot.Kinds, "lesson") != tc.lessons || slices.Contains(snapshot.Kinds, "resource") != tc.resources {
				t.Fatal(snapshot.Kinds)
			}
			if (f.requests["/api/courses/10/lessons"] > 0) != tc.lessons || (f.requests["/api/courses/10/resources"] > 0) != tc.resources {
				t.Fatal("explicitly disabled feature was probed")
			}
		})
	}
}

func TestEdMaterialsDeadlineClearsAndUnknownFields(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		regular, effective               any
		setEffective, missing, wantKnown bool
	}{
		{"null", nil, nil, false, false, true},
		{"empty", "", nil, false, false, true},
		{"zero", 0, nil, false, false, true},
		{"string zero", "0", nil, false, false, true},
		{"effective null", "2030-01-08T00:00:00Z", nil, true, false, true},
		{"missing", nil, nil, false, true, false},
		{"invalid", "not a timestamp", nil, false, false, false},
		{"invalid override", "2030-01-08T00:00:00Z", "invalid", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaterialFixture(t)
			listed := f.routes["/api/courses/10/lessons"].(map[string]any)["lessons"].([]any)[0].(map[string]any)
			if tc.missing {
				delete(listed, "due_at")
			} else {
				listed["due_at"] = tc.regular
			}
			if tc.setEffective {
				listed["effective_due_at"] = tc.effective
			}
			snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
			if err != nil {
				t.Fatal(err)
			}
			defer batch.Rollback()
			if len(snapshot.Deadlines) != 1 || snapshot.Deadlines[0].DueAt != 0 {
				t.Fatal(snapshot.Deadlines)
			}
			known := snapshot.DeadlineFields["ed:lesson:101"]
			if known.Due != tc.wantKnown || !known.Opens || !known.Cutoff || !known.Status {
				t.Fatal(known)
			}
			if (len(snapshot.Warnings) > 0) == tc.wantKnown {
				t.Fatal("unknown timestamp did not preserve old data with a warning", snapshot.Warnings)
			}
		})
	}
	f := newMaterialFixture(t)
	attempt := f.routes["/api/lessons/101/attempts/1"].(map[string]any)["attempt"].(map[string]any)
	attempt["status"] = "unrecognized"
	listed := f.routes["/api/courses/10/lessons"].(map[string]any)["lessons"].([]any)[0].(map[string]any)
	listed["available_at"] = map[string]any{"unexpected": true}
	delete(listed, "locked_at")
	snapshot, batch, err := collectEdMaterials(context.Background(), f.p, f.course, false)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Rollback()
	known := snapshot.DeadlineFields["ed:lesson:101"]
	if known.Opens || !known.Due || known.Cutoff || known.Status || snapshot.Deadlines[0].SubmissionStatus != "" || snapshot.Deadlines[0].Completed {
		t.Fatal("unavailable fields were invented", known, snapshot.Deadlines)
	}
}
