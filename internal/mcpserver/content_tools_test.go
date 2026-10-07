package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sbot5/lms-mcp/internal/store"
)

const contentCourse = "ABC1234-2026S2"

func contentCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || r.IsError {
		t.Fatalf("%s failed: result=%+v err=%v", name, r, err)
	}
	b, err := json.Marshal(r.StructuredContent)
	if err != nil || len(b) > BudgetChars {
		t.Fatalf("%s exceeded JSON budget: %d bytes, err=%v", name, len(b), err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil || out == nil {
		t.Fatalf("%s must return an object: %s (%v)", name, b, err)
	}
	return out
}

func contentSeed(t *testing.T, d Deps, items ...store.Item) {
	t.Helper()
	for _, it := range items {
		it.CourseID, it.Provider = contentCourse, "ed"
		if it.ContentHash == "" {
			it.ContentHash = it.ID
		}
		if _, err := d.DB.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
	}
}

func contentRows(t *testing.T, out map[string]any) []any {
	t.Helper()
	rows, ok := out["items"].([]any)
	if !ok {
		t.Fatalf("items must be an array, got %+v", out)
	}
	return rows
}

func TestContentToolsRegisteredReadOnly(t *testing.T) {
	cs := connect(t, testDeps(t))
	r, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"list_posts": false, "get_post": false, "list_materials": false, "read_material": false, "whats_new": false, "upcoming_deadlines": false, "get_assessment": false}
	for _, tool := range r.Tools {
		if _, ok := want[tool.Name]; !ok {
			continue
		}
		want[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not read-only", tool.Name)
		}
		if len(tool.Description) >= 1000 {
			t.Errorf("%s description too long", tool.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("%s missing", name)
		}
	}
}

func TestContentListsEmptyAndInvalidCursor(t *testing.T) {
	cs := connect(t, testDeps(t))
	for _, name := range []string{"list_posts", "list_materials", "whats_new", "upcoming_deadlines"} {
		t.Run(name, func(t *testing.T) {
			out := contentCall(t, cs, name, map[string]any{"course": contentCourse})
			if len(contentRows(t, out)) != 0 || out["has_more"] != false {
				t.Fatalf("bad empty list: %+v", out)
			}
			for _, cursor := range []string{"bad cursor!", "e30", "eyJvIjotMX0"} {
				r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"cursor": cursor}})
				if err == nil && !r.IsError {
					t.Fatalf("invalid cursor accepted: %s", cursor)
				}
			}
		})
	}
	for _, name := range []string{"get_post", "read_material", "get_assessment"} {
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"item_id": "ed:missing"}})
		if err == nil && !r.IsError {
			t.Fatalf("%s accepted absent item", name)
		}
	}
}

func TestPostsPaginationFiltersFreshnessAndReplyTree(t *testing.T) {
	d := testDeps(t)
	if err := d.DB.SetMeta("ed:last_success:"+contentCourse, "1800000000"); err != nil {
		t.Fatal(err)
	}
	contentSeed(t, d,
		store.Item{ID: "ed:thread:1", Kind: "thread", Title: "Synthetic staff post", AuthorRole: "staff", AuthorDisplay: "Synthetic Tutor", UpdatedAt: 10, BodyMD: "Root", MetaJSON: `{"category":"Questions","is_answered":false,"is_own":true,"jwt":"private","number":1}`},
		store.Item{ID: "ed:thread:2", Kind: "announcement", Title: "Synthetic announcement", UpdatedAt: 9},
		store.Item{ID: "ed:reply:1", Kind: "reply", ParentID: "ed:thread:1", BodyMD: "First reply", AuthorRole: "student", AuthorDisplay: "Private student name", UpdatedAt: 8},
		store.Item{ID: "ed:reply:2", Kind: "reply", ParentID: "ed:reply:1", BodyMD: "Nested reply", UpdatedAt: 7},
	)
	cs := connect(t, d)
	first := contentCall(t, cs, "list_posts", map[string]any{"course": contentCourse, "limit": 1})
	if len(contentRows(t, first)) != 1 || first["has_more"] != true {
		t.Fatalf("first page: %+v", first)
	}
	second := contentCall(t, cs, "list_posts", map[string]any{"course": contentCourse, "limit": 1, "cursor": first["next_cursor"]})
	if len(contentRows(t, second)) != 1 || second["has_more"] != false {
		t.Fatalf("second page: %+v", second)
	}
	filtered := contentCall(t, cs, "list_posts", map[string]any{"course": contentCourse, "category": "Questions", "staff": true, "mine": true, "unanswered": true})
	if len(contentRows(t, filtered)) != 1 {
		t.Fatalf("post filters: %+v", filtered)
	}
	if !strings.Contains(fmt.Sprint(filtered["freshness"]), "1.8e+09") {
		t.Fatalf("freshness missing: %+v", filtered)
	}
	post := contentCall(t, cs, "get_post", map[string]any{"item_id": "ed:thread:1"})
	encoded, _ := json.Marshal(post)
	if !strings.Contains(string(encoded), "Nested reply") || strings.Contains(string(encoded), "Private student name") || strings.Contains(string(encoded), "private") {
		t.Fatalf("unsafe or incomplete tree: %s", encoded)
	}
}

func TestMaterialsAssessmentEventsAndDeadlines(t *testing.T) {
	d := testDeps(t)
	contentSeed(t, d, store.Item{ID: "ed:lesson:1", Kind: "lesson", Title: "Synthetic lesson", BodyMD: "Lesson text", MetaJSON: `{"responses":[{"answer":"Synthetic answer","jwt":"private"}],"released_answers":["Synthetic released answer"],"unknown_secret":"private"}`}, store.Item{ID: "ed:slide:2", Kind: "slide", ParentID: "ed:lesson:1", BodyMD: "Slide text"}, store.Item{ID: "ed:resource:3", Kind: "resource", Title: "Synthetic resource"})
	if _, err := d.DB.UpsertGrade(store.Grade{CourseID: contentCourse, ItemKey: "ed:slide:2", Name: "Synthetic grade", Grade: "8", GradeMax: "10", FeedbackMD: "Synthetic feedback"}); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.UpsertDeadline(store.Deadline{CourseID: contentCourse, ItemID: "ed:lesson:1", Kind: "lesson", DueAt: time.Now().Add(24 * time.Hour).Unix(), SubmissionStatus: "unknown"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []store.Event{{ID: "baseline", CourseID: contentCourse, Baseline: true, Kind: "new_material"}, {ID: "change", CourseID: contentCourse, ItemID: "ed:lesson:1", Kind: "material_changed", Title: "Synthetic change"}} {
		if err := d.DB.InsertEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	cs := connect(t, d)
	materials := contentCall(t, cs, "list_materials", map[string]any{"course": contentCourse, "limit": 1})
	if len(contentRows(t, materials)) != 1 || materials["has_more"] != true {
		t.Fatalf("materials pagination: %+v", materials)
	}
	read := contentCall(t, cs, "read_material", map[string]any{"item_id": "ed:lesson:1", "offset": 7, "max_chars": 4})
	if read["text"] != "text" {
		t.Fatalf("read text: %+v", read)
	}
	assessment := contentCall(t, cs, "get_assessment", map[string]any{"item_id": "ed:lesson:1"})
	b, _ := json.Marshal(assessment)
	if !strings.Contains(string(b), "Synthetic answer") || !strings.Contains(string(b), "Synthetic feedback") || strings.Contains(string(b), "private") {
		t.Fatalf("assessment content: %s", b)
	}
	events := contentCall(t, cs, "whats_new", map[string]any{"course": contentCourse})
	if len(contentRows(t, events)) != 1 {
		t.Fatalf("baseline was returned: %+v", events)
	}
	deadlines := contentCall(t, cs, "upcoming_deadlines", map[string]any{"course": contentCourse, "days": 2})
	if len(contentRows(t, deadlines)) != 1 {
		t.Fatalf("future deadline missing: %+v", deadlines)
	}
}

func TestContentWholeJSONBudget(t *testing.T) {
	d := testDeps(t)
	huge := strings.Repeat("<课程>&\"", 12000)
	for i := 0; i < 30; i++ {
		contentSeed(t, d, store.Item{ID: fmt.Sprintf("ed:thread:%d", i), Kind: "thread", Title: huge, BodyMD: huge, MetaJSON: fmt.Sprintf(`{"category":%q,"responses":[{"answer":%q}]}`, huge, huge)})
	}
	contentSeed(t, d, store.Item{ID: "ed:lesson:1", Kind: "lesson", Title: huge, BodyMD: huge, MetaJSON: fmt.Sprintf(`{"responses":[{"answer":%q}]}`, huge)})
	cs := connect(t, d)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{{"list_posts", map[string]any{"limit": 100}}, {"get_post", map[string]any{"item_id": "ed:thread:1"}}, {"list_materials", nil}, {"read_material", map[string]any{"item_id": "ed:lesson:1", "max_chars": 30000}}, {"get_assessment", map[string]any{"item_id": "ed:lesson:1"}}} {
		out := contentCall(t, cs, tc.name, tc.args)
		if out["truncated"] != true {
			t.Fatalf("%s did not declare truncation", tc.name)
		}
	}
}

func contentError(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error()
	}
	if !r.IsError {
		t.Fatalf("%s should reject %+v: %+v", name, args, r)
	}
	raw, _ := json.Marshal(r.Content)
	return string(raw)
}

func TestEveryContentListHasStableBudgetedContinuation(t *testing.T) {
	d := testDeps(t)
	huge := strings.Repeat("<课程>&\"", 16000)
	due := time.Now().Add(24 * time.Hour).Unix()
	for i := 0; i < 5; i++ {
		postID, materialID := fmt.Sprintf("ed:thread:%d", i), fmt.Sprintf("ed:lesson:%d", i)
		contentSeed(t, d, store.Item{ID: postID, Kind: "thread", Title: huge, UpdatedAt: 100}, store.Item{ID: materialID, Kind: "lesson", Title: huge, UpdatedAt: 100})
		if err := d.DB.InsertEvent(store.Event{ID: fmt.Sprintf("event:%d", i), CourseID: contentCourse, ItemID: materialID, Kind: "new_material", DetectedAt: 100, Title: huge}); err != nil {
			t.Fatal(err)
		}
		if err := d.DB.UpsertDeadline(store.Deadline{CourseID: contentCourse, ItemID: materialID, Kind: "lesson", DueAt: due}); err != nil {
			t.Fatal(err)
		}
	}
	cs := connect(t, d)
	for _, name := range []string{"list_posts", "list_materials", "whats_new", "upcoming_deadlines"} {
		t.Run(name, func(t *testing.T) {
			seen := map[string]bool{}
			cursor := ""
			for page := 0; page < 10; page++ {
				out := contentCall(t, cs, name, map[string]any{"course": contentCourse, "limit": 100, "cursor": cursor})
				if out["truncated"] != true {
					t.Fatalf("budget truncation missing: %+v", out)
				}
				rows := contentRows(t, out)
				if len(rows) == 0 {
					t.Fatal("budget pagination made no progress")
				}
				for _, row := range rows {
					entry := row.(map[string]any)
					id, _ := entry["id"].(string)
					if id == "" {
						id = entry["item_id"].(string)
					}
					if seen[id] {
						t.Fatalf("duplicate entry %s", id)
					}
					seen[id] = true
				}
				if out["has_more"] == false {
					break
				}
				next, _ := out["next_cursor"].(string)
				if next == "" || next == cursor {
					t.Fatalf("cursor failed to advance: %+v", out)
				}
				cursor = next
			}
			if len(seen) != 5 {
				t.Fatalf("lost entries: %v", seen)
			}
			first := contentCall(t, cs, name, map[string]any{"course": contentCourse, "limit": 1})
			again := contentCall(t, cs, name, map[string]any{"course": contentCourse, "limit": 1})
			if fmt.Sprint(first) != fmt.Sprint(again) {
				t.Fatalf("unstable equal-timestamp order")
			}
		})
	}
}

func TestContentRejectsBadScopesKindsWindowsAndNoncanonicalCursors(t *testing.T) {
	cs := connect(t, testDeps(t))
	for _, name := range []string{"list_posts", "list_materials", "whats_new", "upcoming_deadlines", "list_courses"} {
		for _, raw := range []string{`{"o":1,"o":2}`, `{"o":1,"x":2}`, `{"o":0}`, `{"o":1.5}`, `null`, `{"o": 1}`} {
			contentError(t, cs, name, map[string]any{"cursor": base64.RawURLEncoding.EncodeToString([]byte(raw))})
		}
		if name != "list_courses" {
			contentError(t, cs, name, map[string]any{"course": "10"})
		}
	}
	contentError(t, cs, "list_materials", map[string]any{"kinds": []string{"thread"}})
	for _, days := range []int{-1, 366} {
		contentError(t, cs, "upcoming_deadlines", map[string]any{"days": days})
	}
}

func TestAssessmentVisibilityRelatedRowsAndSecretWhitelist(t *testing.T) {
	d := testDeps(t)
	contentSeed(t, d,
		store.Item{ID: "ed:lesson:1", Kind: "lesson", BodyMD: "Synthetic requirements", MetaJSON: `{"availability":{"state":"scheduled","openable":false,"opens_at":1800000000,"ticket":"SECRET"},"content_unavailable":true,"responses":null}`},
		store.Item{ID: "ed:slide:2", Kind: "slide", ParentID: "ed:lesson:1", BodyMD: "Synthetic question", MetaJSON: `{"responses":[{"answer":{"text":"Synthetic nested answer","token":"SECRET"},"jwt":"SECRET"}],"submissions":[{"code":"Synthetic code","ticket":"SECRET"}],"released_answers":[],"availability":{"state":"open","session_key":"SECRET"},"JWT":"SECRET"}`},
		store.Item{ID: "ed:slide:3", Kind: "slide", ParentID: "ed:lesson:other", BodyMD: "Unrelated synthetic answer"},
		store.Item{ID: "ed:lesson:empty", Kind: "lesson"},
	)
	for _, id := range []string{"ed:slide:2", "ed:slide:3"} {
		if _, err := d.DB.UpsertGrade(store.Grade{CourseID: contentCourse, ItemKey: id, Grade: "8", GradeMax: "10", FeedbackMD: "Synthetic feedback " + id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.DB.UpsertFile(store.File{ID: "ed:file:child", ItemID: "ed:slide:2", Name: "Synthetic child.txt", MIME: "text/plain", SourceURL: "https://example.test/file?token=SECRET#SECRET", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.UpsertDeadline(store.Deadline{CourseID: contentCourse, ItemID: "ed:lesson:1", Kind: "lesson", DueAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, d)
	out := contentCall(t, cs, "get_assessment", map[string]any{"item_id": "ed:lesson:1"})
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "Unrelated") || strings.Contains(string(raw), "feedback ed:slide:3") {
		t.Fatalf("unsafe/unrelated content: %s", raw)
	}
	if !strings.Contains(string(raw), "Synthetic nested answer") || !strings.Contains(string(raw), "Synthetic child.txt") {
		t.Fatalf("missing related assessment rows: %s", raw)
	}
	item := out["item"].(map[string]any)
	for _, field := range []string{"responses_available", "submissions_available", "released_answers_available", "grades_available", "deadlines_available"} {
		if item[field] != true {
			t.Fatalf("%s missing: %+v", field, item)
		}
	}
	if out["availability"] != "content_unavailable" {
		t.Fatalf("permission state lost: %+v", out)
	}
	if out["freshness"].([]any)[0].(map[string]any)["synced"] != false {
		t.Fatal("fabricated sync freshness")
	}
	slide := contentCall(t, cs, "get_assessment", map[string]any{"item_id": "ed:slide:2"})["item"].(map[string]any)
	if len(slide["deadlines"].([]any)) != 1 {
		t.Fatal("slide did not inherit lesson deadline")
	}
	empty := contentCall(t, cs, "get_assessment", map[string]any{"item_id": "ed:lesson:empty"})
	item = empty["item"].(map[string]any)
	for _, field := range []string{"responses_available", "submissions_available", "released_answers_available", "grades_available", "deadlines_available"} {
		if item[field] != false {
			t.Fatalf("invented %s: %+v", field, item)
		}
	}
	if len(item["grades"].([]any)) != 0 || len(item["deadlines"].([]any)) != 0 || empty["availability"] != "not_synced" {
		t.Fatalf("invented unavailable data: %+v", empty)
	}
}

func TestReadMaterialUnicodePagesAndNoText(t *testing.T) {
	d := testDeps(t)
	body := strings.Repeat("课程<>&\"", 7000)
	contentSeed(t, d, store.Item{ID: "ed:lesson:1", Kind: "lesson", BodyMD: body}, store.Item{ID: "ed:resource:2", Kind: "resource"})
	cs := connect(t, d)
	offset := 0
	var combined strings.Builder
	for page := 0; page < 100; page++ {
		out := contentCall(t, cs, "read_material", map[string]any{"item_id": "ed:lesson:1", "offset": offset, "max_chars": 30000})
		text := out["text"].(string)
		combined.WriteString(text)
		next := int(out["next_offset"].(float64))
		if next <= offset {
			t.Fatal("text pagination made no progress")
		}
		offset = next
		if out["has_more"] == false {
			break
		}
		if out["truncated"] != true {
			t.Fatal("missing text truncation flag")
		}
	}
	if combined.String() != body {
		t.Fatal("Unicode text pages lost or duplicated characters")
	}
	for _, args := range []map[string]any{{"offset": -1}, {"max_chars": -1}} {
		args["item_id"] = "ed:lesson:1"
		contentError(t, cs, "read_material", args)
	}
	out := contentCall(t, cs, "read_material", map[string]any{"item_id": "ed:resource:2"})
	if out["text"] != "" || out["has_more"] != false || out["availability"] != "not_synced" {
		t.Fatalf("empty resource: %+v", out)
	}
}

func TestReadMaterialVerifiedTextAndLocalMutation(t *testing.T) {
	d := testDeps(t)
	d.DataRoot = t.TempDir()
	contentSeed(t, d, store.Item{ID: "ed:resource:1", Kind: "resource"}, store.Item{ID: "ed:resource:2", Kind: "resource"})
	text := []byte("Synthetic 课程 attachment")
	path := filepath.Join(d.DataRoot, "synthetic.txt")
	if err := os.WriteFile(path, text, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(text)
	f := store.File{ID: "ed:file:1", ItemID: "ed:resource:1", Name: "synthetic.txt", MIME: "text/plain", LocalPath: path, Size: int64(len(text)), SHA256: hex.EncodeToString(hash[:]), Status: "ok"}
	if err := d.DB.UpsertFile(f); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, d)
	for _, args := range []map[string]any{{"item_id": f.ItemID}, {"item_id": f.ItemID, "file_id": f.ID}} {
		out := contentCall(t, cs, "read_material", args)
		if out["text"] != string(text) || out["item"].(map[string]any)["local_path"] != path {
			t.Fatalf("verified text: %+v", out)
		}
	}
	contentError(t, cs, "read_material", map[string]any{"item_id": "ed:resource:2", "file_id": f.ID})
	if err := os.WriteFile(path, []byte(strings.Repeat("X", len(text))), 0600); err != nil {
		t.Fatal(err)
	}
	errText := contentError(t, cs, "read_material", map[string]any{"item_id": f.ItemID, "file_id": f.ID})
	if !strings.Contains(errText, "checksum mismatch") {
		t.Fatalf("local mutation not detected: %s", errText)
	}
}

func TestReadMaterialRejectsUntrustedAttachments(t *testing.T) {
	for _, tc := range []string{"outside", "traversal", "ads", "hash", "size", "status", "oversize", "binary", "invalid_utf8", "nul", "symlink"} {
		t.Run(tc, func(t *testing.T) {
			d := testDeps(t)
			d.DataRoot = t.TempDir()
			contentSeed(t, d, store.Item{ID: "ed:resource:1", Kind: "resource"})
			text := []byte("Synthetic text")
			path := filepath.Join(d.DataRoot, "text.txt")
			if tc == "invalid_utf8" {
				text = []byte{0xff}
			} else if tc == "nul" {
				text = []byte{'a', 0, 'b'}
			}
			if err := os.WriteFile(path, text, 0600); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(text)
			f := store.File{ID: "ed:file:1", ItemID: "ed:resource:1", Name: "text.txt", MIME: "text/plain", LocalPath: path, Size: int64(len(text)), SHA256: hex.EncodeToString(hash[:]), Status: "ok"}
			switch tc {
			case "outside":
				f.LocalPath = filepath.Join(t.TempDir(), "text.txt")
				if err := os.WriteFile(f.LocalPath, text, 0600); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				f.LocalPath = filepath.Join("..", "text.txt")
			case "ads":
				f.LocalPath = "text.txt:stream"
			case "hash":
				f.SHA256 = strings.Repeat("0", 64)
			case "size":
				f.Size++
			case "status":
				f.Status = "failed"
			case "oversize":
				f.Size = maxLocalTextBytes + 1
			case "binary":
				f.Name = "text.pdf"
				f.MIME = "application/pdf"
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.txt")
				if err := os.WriteFile(outside, text, 0600); err != nil {
					t.Fatal(err)
				}
				f.LocalPath = filepath.Join(d.DataRoot, "linked.txt")
				if err := os.Symlink(outside, f.LocalPath); err != nil {
					t.Skipf("host cannot create symlink: %v", err)
				}
			}
			if err := d.DB.UpsertFile(f); err != nil {
				t.Fatal(err)
			}
			errText := contentError(t, connect(t, d), "read_material", map[string]any{"item_id": f.ItemID, "file_id": f.ID})
			if strings.Contains(errText, path) || strings.Contains(errText, "Synthetic text") {
				t.Fatalf("attachment error leaked local content/path: %s", errText)
			}
			if tc == "binary" && !strings.Contains(errText, "M4") {
				t.Fatalf("binary support limitation missing: %s", errText)
			}
		})
	}
}

func TestListCoursesEmptyBudgetAndContinuation(t *testing.T) {
	d := testDeps(t)
	courses, err := d.DB.ListCourses()
	if err != nil {
		t.Fatal(err)
	}
	for _, course := range courses {
		course.Active = false
		if err := d.DB.UpsertCourse(course); err != nil {
			t.Fatal(err)
		}
	}
	cs := connect(t, d)
	out := contentCall(t, cs, "list_courses", nil)
	if rows, ok := out["courses"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("empty courses must be []: %+v", out)
	}
	huge := strings.Repeat("<课程>&\"", 12000)
	for i := 0; i < 4; i++ {
		if err := d.DB.UpsertCourse(store.Course{ID: fmt.Sprintf("SYN%d-2026S2", i), Title: huge, Active: true, EdCourseID: 100 + i}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 8; page++ {
		out = contentCall(t, cs, "list_courses", map[string]any{"limit": 100, "cursor": cursor})
		if out["truncated"] != true {
			t.Fatal("course budget truncation missing")
		}
		for _, row := range out["courses"].([]any) {
			id := row.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("duplicate course %s", id)
			}
			seen[id] = true
		}
		if out["has_more"] == false {
			break
		}
		cursor = out["next_cursor"].(string)
	}
	if len(seen) != 4 {
		t.Fatalf("courses lost to budget: %v", seen)
	}
}

func TestRecentFiltersDeadlinesWindowsAndUnknownCompletion(t *testing.T) {
	d := testDeps(t)
	contentSeed(t, d, store.Item{ID: "ed:lesson:soon", Kind: "lesson"}, store.Item{ID: "ed:lesson:done", Kind: "lesson"}, store.Item{ID: "ed:lesson:far", Kind: "lesson"}, store.Item{ID: "ed:thread:staff", Kind: "thread", AuthorRole: "ta"}, store.Item{ID: "ed:thread:student", Kind: "thread", AuthorRole: "student"})
	for _, deadline := range []store.Deadline{{CourseID: contentCourse, ItemID: "ed:lesson:soon", Kind: "lesson", DueAt: time.Now().Add(24 * time.Hour).Unix()}, {CourseID: contentCourse, ItemID: "ed:lesson:done", Kind: "lesson", DueAt: time.Now().Add(24 * time.Hour).Unix(), Completed: true, SubmissionStatus: "submitted"}, {CourseID: contentCourse, ItemID: "ed:lesson:far", Kind: "lesson", DueAt: time.Now().Add(8 * 24 * time.Hour).Unix()}} {
		if err := d.DB.UpsertDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"ed:thread:staff", "ed:thread:student"} {
		if err := d.DB.InsertEvent(store.Event{ID: fmt.Sprintf("event:%d", i), CourseID: contentCourse, ItemID: id, Kind: "staff_post", DetectedAt: int64(100 + i), Baseline: i == 0}); err != nil {
			t.Fatal(err)
		}
	}
	cs := connect(t, d)
	out := contentCall(t, cs, "upcoming_deadlines", nil)
	rows := contentRows(t, out)
	if len(rows) != 1 {
		t.Fatalf("default window/completion filter: %+v", out)
	}
	entry := rows[0].(map[string]any)
	if entry["submission_status"] != "unknown" || entry["completion_known"] != false {
		t.Fatalf("invented submission state: %+v", entry)
	}
	if len(contentRows(t, contentCall(t, cs, "upcoming_deadlines", map[string]any{"days": 10, "include_completed": true}))) != 3 {
		t.Fatal("explicit deadline window/completed filter")
	}
	if len(contentRows(t, contentCall(t, cs, "whats_new", map[string]any{"include_baseline": true, "staff_only": true, "since": 100, "kinds": []string{"staff_post"}}))) != 2 {
		t.Fatal("recorded staff event kind filter failed")
	}
	if len(contentRows(t, contentCall(t, cs, "whats_new", map[string]any{"since": 102}))) != 0 {
		t.Fatal("event since filter failed")
	}
}
