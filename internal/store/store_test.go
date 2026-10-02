package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// openTemp opens a fresh store in a temp-FILE database (not :memory:), so WAL
// and multiple connections behave as they do in production.
func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func boolPtr(b bool) *bool { return &b }

func TestOpenMigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	v, err := db.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("schema version = %d, want 1", v)
	}

	var count int
	if err := db.pool.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("schema_migrations rows = %d, want 1", count)
	}

	// Every contract table must exist.
	for _, tbl := range []string{"courses", "items", "item_fts", "files", "grades",
		"deadlines", "events", "sync_runs", "leases", "meta", "schema_migrations"} {
		var name string
		err := db.pool.QueryRow(`SELECT name FROM sqlite_master WHERE name = ?`, tbl).Scan(&name)
		if err != nil {
			t.Fatalf("table %q missing: %v", tbl, err)
		}
	}
	db.Close()

	// Re-opening the same file applies no further migrations.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()
	if v, err = db2.SchemaVersion(); err != nil || v != 1 {
		t.Fatalf("after reopen version = %d err = %v, want 1/nil", v, err)
	}
	if err := db2.pool.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("recount: %v", err)
	}
	if count != 1 {
		t.Fatalf("after reopen schema_migrations rows = %d, want 1 (migrations must not re-run)", count)
	}
}

func TestOpenPragmas(t *testing.T) {
	db := openTemp(t)
	checks := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"synchronous", "1"}, // NORMAL
		{"busy_timeout", "5000"},
	}
	for _, c := range checks {
		var got string
		if err := db.pool.QueryRow("PRAGMA " + c.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", c.pragma, err)
		}
		if !strings.EqualFold(got, c.want) {
			t.Errorf("PRAGMA %s = %q, want %q", c.pragma, got, c.want)
		}
	}
}

func TestCourses(t *testing.T) {
	db := openTemp(t)

	c := Course{ID: "ABC1234-2026S2", Code: "ABC1234", Term: "2026S2",
		Title: "Intro", EdCourseID: 42, MoodleCourseID: 99, Active: true}
	if err := db.UpsertCourse(c); err != nil {
		t.Fatalf("UpsertCourse: %v", err)
	}
	// Update in place.
	c.Title = "Introduction to Things"
	c.Active = false
	if err := db.UpsertCourse(c); err != nil {
		t.Fatalf("UpsertCourse update: %v", err)
	}

	got, err := db.ListCourses()
	if err != nil {
		t.Fatalf("ListCourses: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListCourses len = %d, want 1", len(got))
	}
	g := got[0]
	if g.Title != "Introduction to Things" || g.Active || g.EdCourseID != 42 || g.MoodleCourseID != 99 || g.Code != "ABC1234" || g.Term != "2026S2" {
		t.Fatalf("round-trip mismatch: %+v", g)
	}

	if err := db.SetCourseExcluded(c.ID, true); err != nil {
		t.Fatalf("SetCourseExcluded: %v", err)
	}
	got, _ = db.ListCourses()
	if !got[0].Excluded {
		t.Fatalf("Excluded not set")
	}
	if err := db.SetCourseExcluded(c.ID, false); err != nil {
		t.Fatalf("SetCourseExcluded off: %v", err)
	}
	got, _ = db.ListCourses()
	if got[0].Excluded {
		t.Fatalf("Excluded not cleared")
	}
}

func TestItemsChangedSemantics(t *testing.T) {
	db := openTemp(t)

	it := Item{ID: "ed:thread:9", CourseID: "C1", Provider: "ed", Kind: "thread",
		Title: "Title", BodyMD: "Body", ContentHash: "h1", CreatedAt: 500, UpdatedAt: 500}

	changed, err := db.UpsertItem(it)
	if err != nil || !changed {
		t.Fatalf("new upsert changed = %v err = %v, want true/nil", changed, err)
	}
	changed, err = db.UpsertItem(it)
	if err != nil || changed {
		t.Fatalf("same-hash upsert changed = %v err = %v, want false/nil", changed, err)
	}
	it.ContentHash = "h2"
	it.Title = "Title v2"
	changed, err = db.UpsertItem(it)
	if err != nil || !changed {
		t.Fatalf("changed-hash upsert changed = %v err = %v, want true/nil", changed, err)
	}

	got, ok, err := db.GetItem("ed:thread:9")
	if err != nil || !ok {
		t.Fatalf("GetItem ok = %v err = %v", ok, err)
	}
	if got.Title != "Title v2" || got.ContentHash != "h2" {
		t.Fatalf("GetItem mismatch: %+v", got)
	}

	if _, ok, _ := db.GetItem("nope"); ok {
		t.Fatalf("GetItem on missing id returned ok=true")
	}
}

func TestItemsFilterAndRemoval(t *testing.T) {
	db := openTemp(t)

	items := []Item{
		{ID: "ed:thread:1", CourseID: "C1", Provider: "ed", Kind: "thread", Title: "A", ContentHash: "1", UpdatedAt: 100},
		{ID: "ed:thread:2", CourseID: "C1", Provider: "ed", Kind: "thread", Title: "B", ContentHash: "2", UpdatedAt: 200},
		{ID: "ed:thread:3", CourseID: "C1", Provider: "ed", Kind: "thread", Title: "C", ContentHash: "3", UpdatedAt: 300},
		{ID: "moodle:cm:1", CourseID: "C1", Provider: "moodle", Kind: "material", Title: "M", ContentHash: "4", UpdatedAt: 250},
		{ID: "ed:thread:9", CourseID: "C2", Provider: "ed", Kind: "thread", Title: "Z", ContentHash: "5", UpdatedAt: 400},
	}
	for _, it := range items {
		if _, err := db.UpsertItem(it); err != nil {
			t.Fatalf("UpsertItem %s: %v", it.ID, err)
		}
	}

	// Filter by course.
	got, _ := db.ListItems(ItemFilter{CourseID: "C1"})
	if len(got) != 4 {
		t.Fatalf("course C1 len = %d, want 4", len(got))
	}
	// Newest first.
	if got[0].ID != "ed:thread:3" {
		t.Fatalf("ordering: first = %s, want ed:thread:3", got[0].ID)
	}
	// Filter by provider.
	if got, _ = db.ListItems(ItemFilter{Provider: "moodle"}); len(got) != 1 {
		t.Fatalf("provider moodle len = %d, want 1", len(got))
	}
	// Filter by kind.
	if got, _ = db.ListItems(ItemFilter{CourseID: "C1", Kinds: []string{"thread"}}); len(got) != 3 {
		t.Fatalf("C1 threads len = %d, want 3", len(got))
	}
	// Filter by since.
	if got, _ = db.ListItems(ItemFilter{Since: 250}); len(got) != 3 {
		t.Fatalf("since 250 len = %d, want 3", len(got))
	}
	// Limit/offset.
	if got, _ = db.ListItems(ItemFilter{CourseID: "C1", Limit: 2}); len(got) != 2 {
		t.Fatalf("limit 2 len = %d, want 2", len(got))
	}
	if got, _ = db.ListItems(ItemFilter{CourseID: "C1", Limit: 2, Offset: 3}); len(got) != 1 {
		t.Fatalf("limit 2 offset 3 len = %d, want 1", len(got))
	}

	// Remove C1/ed/thread items except thread:2.
	removed, err := db.MarkItemsRemoved("C1", "ed", "thread", []string{"ed:thread:2"})
	if err != nil {
		t.Fatalf("MarkItemsRemoved: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (thread:1 and thread:3)", removed)
	}
	got, _ = db.ListItems(ItemFilter{CourseID: "C1", Kinds: []string{"thread"}})
	if len(got) != 1 || got[0].ID != "ed:thread:2" {
		t.Fatalf("after removal threads = %+v, want only ed:thread:2", got)
	}
	// Removed item is still retrievable with RemovedAt set.
	ri, ok, _ := db.GetItem("ed:thread:1")
	if !ok || ri.RemovedAt == 0 {
		t.Fatalf("removed item: ok = %v RemovedAt = %d", ok, ri.RemovedAt)
	}
	// Idempotent: second call changes nothing.
	if removed, _ = db.MarkItemsRemoved("C1", "ed", "thread", []string{"ed:thread:2"}); removed != 0 {
		t.Fatalf("second MarkItemsRemoved = %d, want 0", removed)
	}
	// Empty keepIDs removes all remaining of that kind.
	if removed, _ = db.MarkItemsRemoved("C1", "ed", "thread", nil); removed != 1 {
		t.Fatalf("remove all threads = %d, want 1", removed)
	}
}

func TestFiles(t *testing.T) {
	db := openTemp(t)

	f := File{ID: "f1", ItemID: "moodle:cm:1", Name: "slides.pdf", MIME: "application/pdf",
		Size: 1024, SourceURL: "https://example.edu/pluginfile/slides.pdf", LocalPath: "/x/slides.pdf",
		SHA256: "abc", Status: "ok"}
	if err := db.UpsertFile(f); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	f.Status = "too_large"
	f.Size = 2048
	if err := db.UpsertFile(f); err != nil {
		t.Fatalf("UpsertFile update: %v", err)
	}

	got, ok, err := db.GetFile("f1")
	if err != nil || !ok {
		t.Fatalf("GetFile ok = %v err = %v", ok, err)
	}
	if got.Status != "too_large" || got.Size != 2048 || got.Name != "slides.pdf" {
		t.Fatalf("GetFile mismatch: %+v", got)
	}

	if err := db.UpsertFile(File{ID: "f2", ItemID: "moodle:cm:1", Name: "notes.pdf", Status: "ok"}); err != nil {
		t.Fatalf("UpsertFile f2: %v", err)
	}
	if err := db.UpsertFile(File{ID: "f3", ItemID: "other", Name: "x", Status: "ok"}); err != nil {
		t.Fatalf("UpsertFile f3: %v", err)
	}
	list, err := db.ListFiles("moodle:cm:1")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListFiles len = %d, want 2", len(list))
	}
	// Ordered by name: notes.pdf before slides.pdf.
	if list[0].Name != "notes.pdf" || list[1].Name != "slides.pdf" {
		t.Fatalf("ListFiles order: %q, %q", list[0].Name, list[1].Name)
	}
	if _, ok, _ := db.GetFile("missing"); ok {
		t.Fatalf("GetFile missing returned ok=true")
	}
}

func TestSearch(t *testing.T) {
	db := openTemp(t)

	items := []Item{
		{ID: "ed:thread:1", CourseID: "C1", Provider: "ed", Kind: "thread",
			Title: "Photosynthesis overview", BodyMD: "Plants make sugar from light.", ContentHash: "1", UpdatedAt: 1000},
		{ID: "ed:thread:2", CourseID: "C1", Provider: "ed", Kind: "reply",
			Title: "Weekly reading list", BodyMD: "Note: photosynthesis is covered in chapter three.", ContentHash: "2", UpdatedAt: 1100},
		{ID: "moodle:cm:1", CourseID: "C2", Provider: "moodle", Kind: "material",
			Title: "机器学习", BodyMD: "这是 机器学习 的课程笔记 包含 神经网络", ContentHash: "3", UpdatedAt: 1200},
	}
	for _, it := range items {
		if _, err := db.UpsertItem(it); err != nil {
			t.Fatalf("UpsertItem %s: %v", it.ID, err)
		}
	}

	// English query: title match outranks body-only match.
	hits, err := db.Search("photosynthesis", ItemFilter{})
	if err != nil {
		t.Fatalf("Search english: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("english hits = %d, want 2", len(hits))
	}
	if hits[0].ItemID != "ed:thread:1" {
		t.Fatalf("ranking: first = %s, want ed:thread:1 (title match)", hits[0].ItemID)
	}
	for _, h := range hits {
		if strings.TrimSpace(h.Snippet) == "" {
			t.Fatalf("empty snippet for %s", h.ItemID)
		}
	}

	// CJK query.
	cjk, err := db.Search("机器学习", ItemFilter{})
	if err != nil {
		t.Fatalf("Search cjk: %v", err)
	}
	if len(cjk) != 1 || cjk[0].ItemID != "moodle:cm:1" {
		t.Fatalf("cjk hits = %+v, want one moodle:cm:1", cjk)
	}
	if strings.TrimSpace(cjk[0].Snippet) == "" {
		t.Fatalf("empty CJK snippet")
	}
	// CJK body-only token also matches.
	if body, _ := db.Search("神经网络", ItemFilter{}); len(body) != 1 {
		t.Fatalf("cjk body hits = %d, want 1", len(body))
	}

	// Filter inside search.
	if f, _ := db.Search("photosynthesis", ItemFilter{CourseID: "C1"}); len(f) != 2 {
		t.Fatalf("filtered C1 hits = %d, want 2", len(f))
	}
	if f, _ := db.Search("photosynthesis", ItemFilter{Provider: "moodle"}); len(f) != 0 {
		t.Fatalf("filtered moodle hits = %d, want 0", len(f))
	}

	// Removed items drop out of search.
	if _, err := db.MarkItemsRemoved("C1", "ed", "thread", nil); err != nil {
		t.Fatalf("MarkItemsRemoved: %v", err)
	}
	after, _ := db.Search("photosynthesis", ItemFilter{})
	if len(after) != 1 || after[0].ItemID != "ed:thread:2" {
		t.Fatalf("after removal hits = %+v, want only ed:thread:2", after)
	}

	// Empty query returns nothing.
	if e, _ := db.Search("   ", ItemFilter{}); len(e) != 0 {
		t.Fatalf("empty query hits = %d, want 0", len(e))
	}

	// ReindexItem standalone keeps search working after a body edit.
	if err := db.ReindexItem("ed:thread:2", "Weekly reading list", "mitochondria powerhouse"); err != nil {
		t.Fatalf("ReindexItem: %v", err)
	}
	if m, _ := db.Search("mitochondria", ItemFilter{}); len(m) != 1 {
		t.Fatalf("after reindex hits = %d, want 1", len(m))
	}
}

func TestEvents(t *testing.T) {
	db := openTemp(t)

	// Dedup on ID: the second insert is ignored.
	if err := db.InsertEvent(Event{ID: "ed:thread:1:staff_post", CourseID: "C1", Kind: "staff_post", DetectedAt: 100, Title: "first"}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if err := db.InsertEvent(Event{ID: "ed:thread:1:staff_post", CourseID: "C1", Kind: "staff_post", DetectedAt: 100, Title: "second"}); err != nil {
		t.Fatalf("InsertEvent dup: %v", err)
	}
	all, _ := db.ListEvents(EventFilter{})
	if len(all) != 1 {
		t.Fatalf("after dup insert len = %d, want 1", len(all))
	}
	if all[0].Title != "first" {
		t.Fatalf("dedup kept %q, want first", all[0].Title)
	}

	// More events, including a baseline.
	db.InsertEvent(Event{ID: "e200", CourseID: "C1", Kind: "announcement", DetectedAt: 200, Title: "ann"})
	db.InsertEvent(Event{ID: "e300", CourseID: "C2", Kind: "new_grade", DetectedAt: 300, Title: "grade"})
	db.InsertEvent(Event{ID: "base", CourseID: "C1", Kind: "new_material", DetectedAt: 50, Title: "baseline", Baseline: true})

	// Since filter, newest first.
	since, _ := db.ListEvents(EventFilter{Since: 200})
	if len(since) != 2 || since[0].ID != "e300" || since[1].ID != "e200" {
		t.Fatalf("since 200 = %+v, want e300 then e200", ids(since))
	}
	// Exclude baseline.
	nonBase, _ := db.ListEvents(EventFilter{Baseline: boolPtr(false)})
	for _, e := range nonBase {
		if e.Baseline {
			t.Fatalf("baseline leaked into non-baseline listing")
		}
	}
	if len(nonBase) != 3 {
		t.Fatalf("non-baseline len = %d, want 3", len(nonBase))
	}
	// Only baseline.
	if onlyBase, _ := db.ListEvents(EventFilter{Baseline: boolPtr(true)}); len(onlyBase) != 1 {
		t.Fatalf("baseline-only len = %d, want 1", len(onlyBase))
	}
	// Course filter.
	if c1, _ := db.ListEvents(EventFilter{CourseID: "C1"}); len(c1) != 3 {
		t.Fatalf("course C1 events = %d, want 3", len(c1))
	}
	// Kind filter.
	if k, _ := db.ListEvents(EventFilter{Kinds: []string{"new_grade", "announcement"}}); len(k) != 2 {
		t.Fatalf("kind filter = %d, want 2", len(k))
	}

	// Unnotified then MarkNotified.
	un, _ := db.ListEvents(EventFilter{Unnotified: true})
	if len(un) != 4 {
		t.Fatalf("unnotified = %d, want 4", len(un))
	}
	if err := db.MarkNotified([]string{"e200", "e300"}, 9999); err != nil {
		t.Fatalf("MarkNotified: %v", err)
	}
	un, _ = db.ListEvents(EventFilter{Unnotified: true})
	if len(un) != 2 {
		t.Fatalf("unnotified after mark = %d, want 2", len(un))
	}
	// MarkNotified with no ids is a no-op.
	if err := db.MarkNotified(nil, 1); err != nil {
		t.Fatalf("MarkNotified nil: %v", err)
	}
}

func ids(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func TestLeases(t *testing.T) {
	db := openTemp(t)
	const name = "sync"
	h1, h2, h3 := "proc-1", "proc-2", "proc-3"

	ok, err := db.AcquireLease(name, h1, 60)
	if err != nil || !ok {
		t.Fatalf("acquire h1 ok = %v err = %v, want true", ok, err)
	}
	// Different holder is blocked by a live lease.
	if ok, _ = db.AcquireLease(name, h2, 60); ok {
		t.Fatalf("h2 acquired a live lease held by h1")
	}
	// Same holder refreshes (ok=true).
	if ok, _ = db.AcquireLease(name, h1, 60); !ok {
		t.Fatalf("h1 refresh returned false")
	}
	// Force expiry deterministically: refresh with a negative ttl so expires_at
	// lands in the past, then a different holder may steal it.
	if ok, _ = db.AcquireLease(name, h1, -1); !ok {
		t.Fatalf("h1 negative-ttl refresh returned false")
	}
	if ok, _ = db.AcquireLease(name, h2, 60); !ok {
		t.Fatalf("h2 failed to steal an expired lease")
	}
	// Renew by current holder extends.
	if ok, _ = db.RenewLease(name, h2, 60); !ok {
		t.Fatalf("h2 renew returned false")
	}
	// Renew by a non-holder fails.
	if ok, _ = db.RenewLease(name, h1, 60); ok {
		t.Fatalf("h1 renewed a lease it does not hold")
	}
	// Release by a non-holder is a no-op: the lease is still held by h2.
	if err := db.ReleaseLease(name, h1); err != nil {
		t.Fatalf("ReleaseLease wrong holder: %v", err)
	}
	if ok, _ = db.AcquireLease(name, h3, 60); ok {
		t.Fatalf("h3 acquired after a no-op release; lease should still be h2's")
	}
	// Release by the holder frees it.
	if err := db.ReleaseLease(name, h2); err != nil {
		t.Fatalf("ReleaseLease holder: %v", err)
	}
	if ok, _ = db.AcquireLease(name, h3, 60); !ok {
		t.Fatalf("h3 could not acquire after release")
	}
}

func TestSyncRuns(t *testing.T) {
	db := openTemp(t)

	if _, ok, err := db.LastSyncRun("all"); err != nil || ok {
		t.Fatalf("LastSyncRun on empty: ok = %v err = %v", ok, err)
	}

	if err := db.StartSyncRun(SyncRun{ID: "r1", Scope: "all", Trigger: "manual", StartedAt: 100, Status: "running"}); err != nil {
		t.Fatalf("StartSyncRun r1: %v", err)
	}
	if err := db.StartSyncRun(SyncRun{ID: "r2", Scope: "all", Trigger: "schedule", StartedAt: 200, Status: "running"}); err != nil {
		t.Fatalf("StartSyncRun r2: %v", err)
	}
	if err := db.StartSyncRun(SyncRun{ID: "r3", Scope: "ed", Trigger: "mcp", StartedAt: 150, Status: "running"}); err != nil {
		t.Fatalf("StartSyncRun r3: %v", err)
	}

	// Latest by started_at for scope "all" is r2.
	last, ok, err := db.LastSyncRun("all")
	if err != nil || !ok {
		t.Fatalf("LastSyncRun all: ok = %v err = %v", ok, err)
	}
	if last.ID != "r2" || last.Trigger != "schedule" {
		t.Fatalf("LastSyncRun all = %+v, want r2/schedule", last)
	}

	if err := db.FinishSyncRun("r2", "ok", `{"items":3}`, "", 250); err != nil {
		t.Fatalf("FinishSyncRun: %v", err)
	}
	last, _, _ = db.LastSyncRun("all")
	if last.Status != "ok" || last.FinishedAt != 250 || last.CountsJSON != `{"items":3}` {
		t.Fatalf("after finish = %+v", last)
	}

	// Scope isolation.
	if ed, ok, _ := db.LastSyncRun("ed"); !ok || ed.ID != "r3" {
		t.Fatalf("LastSyncRun ed = %+v ok=%v, want r3", ed, ok)
	}
}

func TestMeta(t *testing.T) {
	db := openTemp(t)

	if _, ok, err := db.GetMeta("first_use"); err != nil || ok {
		t.Fatalf("GetMeta missing: ok = %v err = %v", ok, err)
	}
	if err := db.SetMeta("first_use", "1700000000"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	v, ok, err := db.GetMeta("first_use")
	if err != nil || !ok || v != "1700000000" {
		t.Fatalf("GetMeta = %q ok = %v err = %v", v, ok, err)
	}
	// Overwrite.
	if err := db.SetMeta("first_use", "1800000000"); err != nil {
		t.Fatalf("SetMeta overwrite: %v", err)
	}
	if v, _, _ = db.GetMeta("first_use"); v != "1800000000" {
		t.Fatalf("GetMeta after overwrite = %q", v)
	}
}

func TestGradesAndDeadlines(t *testing.T) {
	db := openTemp(t)

	g := Grade{CourseID: "C1", ItemKey: "quiz1", Name: "Quiz 1", Grade: "8", GradeMax: "10", Percentage: "80", Hash: "h1", GradedAt: 100}
	changed, err := db.UpsertGrade(g)
	if err != nil || !changed {
		t.Fatalf("new grade changed = %v err = %v", changed, err)
	}
	if changed, _ = db.UpsertGrade(g); changed {
		t.Fatalf("same-hash grade reported changed")
	}
	g.Grade = "9"
	g.Hash = "h2"
	if changed, _ = db.UpsertGrade(g); !changed {
		t.Fatalf("changed-hash grade not reported changed")
	}
	grades, _ := db.ListGrades("C1")
	if len(grades) != 1 || grades[0].Grade != "9" {
		t.Fatalf("ListGrades = %+v", grades)
	}

	db.UpsertDeadline(Deadline{CourseID: "C1", ItemID: "a1", Kind: "assign", DueAt: 500, SubmissionStatus: "submitted", Completed: true})
	db.UpsertDeadline(Deadline{CourseID: "C1", ItemID: "a2", Kind: "assign", DueAt: 1500})
	db.UpsertDeadline(Deadline{CourseID: "C1", ItemID: "a3", Kind: "quiz", DueAt: 2500})

	win, err := db.ListDeadlines(400, 2000)
	if err != nil {
		t.Fatalf("ListDeadlines: %v", err)
	}
	if len(win) != 2 || win[0].ItemID != "a1" || win[1].ItemID != "a2" {
		t.Fatalf("ListDeadlines window = %+v, want a1,a2", win)
	}
	if !win[0].Completed {
		t.Fatalf("Completed flag lost")
	}
	// Upper bound 0 = open ended.
	if open, _ := db.ListDeadlines(400, 0); len(open) != 3 {
		t.Fatalf("open-ended ListDeadlines = %d, want 3", len(open))
	}
}

// TestConcurrentWrites exercises the in-process write serialization: many
// goroutines write at once and none must see SQLITE_BUSY.
func TestConcurrentWrites(t *testing.T) {
	db := openTemp(t)

	const workers, each = 16, 20
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id := fmt.Sprintf("ed:thread:%d-%d", w, i)
				if _, err := db.UpsertItem(Item{ID: id, CourseID: "C1", Provider: "ed", Kind: "thread", Title: id, ContentHash: id, UpdatedAt: int64(i)}); err != nil {
					errs <- err
					return
				}
				if err := db.InsertEvent(Event{ID: id + ":e", CourseID: "C1", Kind: "staff_post", DetectedAt: int64(i)}); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write: %v", err)
	}

	var n int
	if err := db.pool.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != workers*each {
		t.Fatalf("items = %d, want %d", n, workers*each)
	}
}

// TestConcurrentProcesses simulates two processes sharing one database file via
// two independent handles writing at the same time. WAL + busy_timeout must let
// both complete without error.
func TestConcurrentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	defer db1.Close()
	db2, err := Open(path) // second "process" on the same file
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()

	const each = 25
	var wg sync.WaitGroup
	errs := make(chan error, 2*each)
	write := func(db *DB, tag string) {
		defer wg.Done()
		for i := 0; i < each; i++ {
			id := fmt.Sprintf("%s-%d", tag, i)
			if _, err := db.UpsertItem(Item{ID: id, CourseID: "C1", Provider: "ed", Kind: "thread", Title: id, ContentHash: id}); err != nil {
				errs <- fmt.Errorf("%s: %w", tag, err)
				return
			}
		}
	}
	wg.Add(2)
	go write(db1, "a")
	go write(db2, "b")
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("cross-process write: %v", err)
	}

	var n int
	if err := db1.pool.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2*each {
		t.Fatalf("items = %d, want %d", n, 2*each)
	}
}
