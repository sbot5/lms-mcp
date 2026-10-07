package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
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
