package syncer

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sbot5/lms-mcp/internal/store"
)

func seedContentDiscussion(f *materialFixture) {
	f.routes["/api/courses/"+strconv.Itoa(f.course.EdCourseID)+"/threads"] = map[string]any{"threads": []any{}}
}

func TestEdCourseWithdrawnGradeAndRemovedParentRetainArchive(t *testing.T) {
	f := newMaterialFixture(t)
	seedContentDiscussion(f)
	db := openStore(t)
	lesson := f.routes["/api/lessons/101"].(map[string]any)["lesson"].(map[string]any)
	lesson["slides"].([]any)[0].(map[string]any)["due_at"] = "2030-01-08T00:00:00Z"
	if _, err := syncEdCourse(context.Background(), f.p, db, f.course, false, ""); err != nil {
		t.Fatal(err)
	}
	f.routes["/api/lesson_marks/901"].(map[string]any)["lesson_mark"].(map[string]any)["is_released"] = false
	if _, err := syncEdCourse(context.Background(), f.p, db, f.course, false, ""); err != nil {
		t.Fatal(err)
	}
	item, _, _ := db.GetItem("ed:lesson:101")
	if !strings.Contains(item.MetaJSON, `"marks_availability":"withheld"`) {
		t.Fatal("withdrawal not recorded", item.MetaJSON)
	}
	rows, _ := db.ListGrades(f.course.ID)
	if len(rows) != 2 {
		t.Fatal("withdrawal deleted grade cache")
	}
	f.routes["/api/courses/10/lessons"] = map[string]any{"lessons": []any{map[string]any{"id": 102, "title": "Scheduled synthetic lesson", "state": "scheduled", "openable": false}}}
	if _, err := syncEdCourse(context.Background(), f.p, db, f.course, false, ""); err != nil {
		t.Fatal(err)
	}
	slides, _ := db.ListItems(store.ItemFilter{CourseID: f.course.ID, Provider: "ed", Kinds: []string{"slide"}})
	if len(slides) != 0 {
		t.Fatal("removed lesson left active child slides", len(slides))
	}
	deadlines, _ := db.ListDeadlines(0, 0)
	if len(deadlines) != 0 {
		t.Fatal("removed descendants retained current deadlines", deadlines)
	}
	old, ok, _ := db.GetItem("ed:slide:201")
	if !ok || old.RemovedAt == 0 || old.BodyMD == "" {
		t.Fatal("removed slide archive lost")
	}
}

func TestEdCourseTransactionIdempotentAndFileFailure(t *testing.T) {
	f := newMaterialFixture(t)
	seedContentDiscussion(f)
	db := openStore(t)
	first, err := syncEdCourse(context.Background(), f.p, db, f.course, false, "")
	if err != nil || first.Items == 0 {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := syncEdCourse(context.Background(), f.p, db, f.course, false, "")
	if err != nil || second.Changes != 0 {
		t.Fatalf("repeat %+v %v", second, err)
	}
	checkpoint, _, _ := db.GetMeta("ed:last_success:" + f.course.ID)
	files, _ := db.ListFiles("ed:resource:401")
	var before []byte
	if len(files) > 0 {
		before, _ = os.ReadFile(files[0].LocalPath)
	}
	f.mu.Lock()
	f.fileBytes = "replacement file"
	f.statuses["/api/resources/401/download"] = http.StatusForbidden
	f.mu.Unlock()
	if _, err := syncEdCourse(context.Background(), f.p, db, f.course, true, ""); err == nil {
		t.Fatal("failed visible file accepted")
	}
	after, _, _ := db.GetMeta("ed:last_success:" + f.course.ID)
	if after != checkpoint {
		t.Fatal("file failure advanced checkpoint")
	}
	if len(files) > 0 {
		body, _ := os.ReadFile(files[0].LocalPath)
		if string(body) != string(before) {
			t.Fatal("file failure replaced prior mirror")
		}
	}
}

func TestEdCourseLostLeaseRestoresPublishedFiles(t *testing.T) {
	f := newMaterialFixture(t)
	seedContentDiscussion(f)
	db := openStore(t)
	if _, err := syncEdCourse(context.Background(), f.p, db, f.course, false, ""); err != nil {
		t.Fatal(err)
	}
	checkpoint, _, _ := db.GetMeta("ed:last_success:" + f.course.ID)
	items, _ := db.ListItems(store.ItemFilter{CourseID: f.course.ID, Provider: "ed"})
	before := map[string][]byte{}
	for _, item := range items {
		rows, _ := db.ListFiles(item.ID)
		for _, file := range rows {
			if file.LocalPath != "" {
				body, _ := os.ReadFile(file.LocalPath)
				before[file.LocalPath] = body
			}
		}
	}
	f.mu.Lock()
	f.fileBytes = "new bytes rejected by lease"
	f.mu.Unlock()
	if _, err := syncEdCourse(context.Background(), f.p, db, f.course, true, "unowned"); err == nil {
		t.Fatal("unowned lease committed")
	}
	if got, _, _ := db.GetMeta("ed:last_success:" + f.course.ID); got != checkpoint {
		t.Fatal("lost lease advanced checkpoint")
	}
	for path, body := range before {
		got, err := os.ReadFile(filepath.Clean(path))
		if err != nil || string(got) != string(body) {
			t.Fatalf("rollback did not restore %s: %v", filepath.Base(path), err)
		}
	}
}
