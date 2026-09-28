package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mockClient(t *testing.T, handler func(*http.Request) (int, any)) *edClient {
	t.Helper()
	c := newEdClient("test-secret")
	c.hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing auth")
		}
		status, v := handler(r)
		b, _ := json.Marshal(v)
		return &http.Response{StatusCode: status, Status: fmt.Sprint(status), Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(b))), Request: r}, nil
	})
	return c
}
func testCourse(t *testing.T) courseConfig {
	return courseConfig{ID: 1, Code: "TEST", Name: "Test course", Directory: t.TempDir()}
}
func testDetail() edThreadDetail {
	return edThreadDetail{edThread: edThread{ID: 10, CourseID: 1, Number: 7, UserID: 9, Title: "Announcement", Content: "<document><paragraph>Hello</paragraph></document>", Document: "Hello", UpdatedAt: "2026-09-28T00:00:00Z", IsPinned: true}}
}

func TestSyncIdempotentReplyCountAndFullRefresh(t *testing.T) {
	co := testCourse(t)
	cfg := syncConfig{FullRefreshHours: 24}
	detail := testDetail()
	calls := 0
	c := mockClient(t, func(r *http.Request) (int, any) {
		if strings.Contains(r.URL.Path, "/courses/") {
			ts := []edThread{}
			if r.URL.Query().Get("offset") == "0" {
				ts = append(ts, detail.edThread)
			}
			return 200, map[string]any{"threads": ts}
		}
		calls++
		return 200, map[string]any{"thread": detail, "users": []edUser{{ID: 9, CourseRole: "student"}, {ID: 12, CourseRole: "admin"}}}
	})
	first, err := syncCourse(context.Background(), c, cfg, co, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Threads != 1 || first.Changes != 1 {
		t.Fatalf("first: %+v", first)
	}
	news, err := whatsNew(syncConfig{Courses: []courseConfig{co}}, newsInput{})
	if err != nil || len(news.Changes) != 0 {
		t.Fatalf("baseline is news: %+v %v", news, err)
	}
	b1, _ := os.ReadFile(filepath.Join(co.Directory, "_discussion.jsonl"))
	second, err := syncCourse(context.Background(), c, cfg, co, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changes != 0 || second.DetailsFetched != 0 || calls != 1 {
		t.Fatalf("second: %+v calls %d", second, calls)
	}
	b2, _ := os.ReadFile(filepath.Join(co.Directory, "_discussion.jsonl"))
	if string(b1) != string(b2) {
		t.Fatal("unchanged output rewritten differently")
	}
	// Ed can update metadata without changing the actual post (observed live).
	detail.UpdatedAt = "2026-09-28T00:01:00Z"
	metadata, err := syncCourse(context.Background(), c, cfg, co, false)
	if err != nil || metadata.Changes != 0 {
		t.Fatalf("timestamp-only false alert: %+v %v", metadata, err)
	}
	// A reply can change while the parent updated_at stays unchanged.
	detail.ReplyCount = 1
	detail.Answers = []edReply{{ID: 20, UserID: 12, Type: "answer", Content: "<paragraph>Staff reply</paragraph>", Document: "Staff reply"}}
	third, err := syncCourse(context.Background(), c, cfg, co, false)
	if err != nil || third.Changes != 1 {
		t.Fatalf("reply count not detected: %+v %v", third, err)
	}
	news, err = whatsNew(syncConfig{Courses: []courseConfig{co}}, newsInput{StaffOnly: true})
	if err != nil || len(news.Changes) != 1 {
		t.Fatalf("staff reply missing: %+v %v", news, err)
	}
	detail.Answers[0].Document = "Edited reply"
	detail.Answers[0].Content = "<paragraph>Edited reply</paragraph>"
	fourth, err := syncCourse(context.Background(), c, cfg, co, true)
	if err != nil || fourth.Changes != 1 {
		t.Fatalf("full refresh failed: %+v %v", fourth, err)
	}
}

func TestFailedDetailDoesNotAdvanceState(t *testing.T) {
	co := testCourse(t)
	detail := testDetail()
	fail := false
	c := mockClient(t, func(r *http.Request) (int, any) {
		if strings.Contains(r.URL.Path, "/courses/") {
			ts := []edThread{}
			if r.URL.Query().Get("offset") == "0" {
				ts = append(ts, detail.edThread)
			}
			return 200, map[string]any{"threads": ts}
		}
		if fail {
			return 403, map[string]any{"token": "test-secret"}
		}
		return 200, map[string]any{"thread": detail}
	})
	if _, err := syncCourse(context.Background(), c, syncConfig{FullRefreshHours: 24}, co, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(co.Directory, ".lms-sync", "state.json")
	before, _ := os.ReadFile(path)
	fail = true
	detail.ReplyCount++
	_, err := syncCourse(context.Background(), c, syncConfig{FullRefreshHours: 24}, co, false)
	if err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("unsafe or missing failure %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("advanced state on failure")
	}
}

func TestPaginationShortPagesAndDuplicates(t *testing.T) {
	var offsets []string
	c := mockClient(t, func(r *http.Request) (int, any) {
		off := r.URL.Query().Get("offset")
		offsets = append(offsets, off)
		var ids []int
		switch off {
		case "0":
			ids = []int{1, 2}
		case "2":
			ids = []int{2, 3}
		}
		ts := []edThread{}
		for _, id := range ids {
			ts = append(ts, edThread{ID: id, CourseID: 1})
		}
		return 200, map[string]any{"threads": ts}
	})
	ts, err := c.threads(context.Background(), 1)
	if err != nil || len(ts) != 3 || strings.Join(offsets, ",") != "0,2,4" {
		t.Fatalf("%v %v %v", ts, offsets, err)
	}
}

func TestStorageBackupConflictAndCrashRecovery(t *testing.T) {
	co := testCourse(t)
	s, err := readState(co)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(co.Directory, "_discussion.md")
	os.WriteFile(path, []byte("legacy"), 0600)
	if err = writeOutputs(co, &s, map[string][]byte{"_discussion.md": []byte("v1")}); err != nil {
		t.Fatal(err)
	}
	backup, _ := os.ReadFile(filepath.Join(co.Directory, ".lms-sync", "backups", digest([]byte("legacy")), "_discussion.md"))
	if string(backup) != "legacy" {
		t.Fatal("missing legacy backup")
	}
	os.WriteFile(path, []byte("user edit"), 0600)
	if err = writeOutputs(co, &s, map[string][]byte{"_discussion.md": []byte("v2")}); err == nil {
		t.Fatal("overwrote local edits")
	}
	// Crash after file v2 is written but before state v2. Remote now has v3.
	os.WriteFile(path, []byte("v2"), 0600)
	b, _ := json.Marshal(map[string][]string{"_discussion.md": {digest([]byte("v2"))}})
	os.WriteFile(filepath.Join(co.Directory, ".lms-sync", "pending.json"), b, 0600)
	if err = writeOutputs(co, &s, map[string][]byte{"_discussion.md": []byte("v3")}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v3" {
		t.Fatal("recovery failed")
	}
	if _, err = os.Stat(filepath.Join(co.Directory, ".lms-sync", "pending.json")); !os.IsNotExist(err) {
		t.Fatal("journal not cleared")
	}
}

func TestCourseLockReleasedOnClose(t *testing.T) {
	co := testCourse(t)
	a, err := lockCourse(co)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := lockCourse(co); err == nil {
		b.Close()
		t.Fatal("second lock acquired")
	}
	a.Close()
	b, err := lockCourse(co)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
}

func TestXMLPrivacyAttachmentsAndCode(t *testing.T) {
	src := `<document><paragraph>Hello <mention id="9">Jane &amp; Doe</mention></paragraph><file url="https://static.edusercontent.com/report.pdf" filename="Report.pdf"/><snippet language="go"><snippet-file>one</snippet-file><snippet-file>two</snippet-file></snippet><table><table-row><table-cell>A</table-cell><table-cell>B</table-cell></table-row><table-row><table-cell>C</table-cell><table-cell>D</table-cell></table-row></table></document>`
	md, txt, err := renderBody(src, "Hello Jane & Doe", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"@[用户]", "https://static.edusercontent.com/report.pdf", "one\ntwo", "| A | B |"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %s in %s", want, md)
		}
	}
	if strings.Contains(md, "Jane") || strings.Contains(txt, "Jane") {
		t.Fatal("mention leaked")
	}
	refs := map[string]string{}
	if err = collectAssets(src, refs); err != nil || len(refs) != 1 {
		t.Fatalf("file not collected %v %v", refs, err)
	}
	if _, _, err = renderBody("<bad>", "", nil); err == nil {
		t.Fatal("malformed XML accepted")
	}
	for _, raw := range []string{"http://static.edusercontent.com/a", "https://evil-edusercontent.com/a", "https://edusercontent.com.evil/a", "https://edusercontent.com:444/a", "https://localhost/a"} {
		if allowedAsset(raw) {
			t.Errorf("allowed %s", raw)
		}
	}
}

func TestLessonChangesWithoutUpdatedAt(t *testing.T) {
	co := testCourse(t)
	co.Lessons = true
	content := "<paragraph>Version 1</paragraph>"
	s, _ := readState(co)
	c := mockClient(t, func(r *http.Request) (int, any) {
		if strings.Contains(r.URL.Path, "/courses/") {
			return 200, map[string]any{"lessons": []edLesson{{ID: 2, CourseID: 1, ModuleID: 3}}, "modules": []edModule{{ID: 3, Name: "- Week 1"}}}
		}
		return 200, map[string]any{"lesson": edLesson{ID: 2, CourseID: 1, ModuleID: 3, Title: "Lesson", Slides: []edSlide{{ID: 4, Type: "document", Title: "Slide", Content: content}}}}
	})
	files := map[string][]byte{}
	n, err := syncLessons(context.Background(), c, co, &s, files, time.Now(), false)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	n, err = syncLessons(context.Background(), c, co, &s, map[string][]byte{}, time.Now(), false)
	if err != nil || n != 0 {
		t.Fatalf("unchanged: %d %v", n, err)
	}
	content = "<paragraph>Version 2</paragraph>"
	n, err = syncLessons(context.Background(), c, co, &s, map[string][]byte{}, time.Now(), false)
	if err != nil || n != 1 {
		t.Fatalf("changed: %d %v", n, err)
	}
}

func TestSafeNamesAndPaths(t *testing.T) {
	for _, s := range []string{"CON", "../bad/name:*?", "...", "x."} {
		n := safeName(s, 50)
		if strings.ContainsAny(n, `/\:*?`) || n == "CON" || strings.HasSuffix(n, ".") {
			t.Fatalf("unsafe %q", n)
		}
	}
	if _, err := safeTarget(t.TempDir(), "../escape"); err == nil {
		t.Fatal("traversal allowed")
	}
}

func TestRecoveryRetainsRenamedPathOwnership(t *testing.T) {
	co := testCourse(t)
	s, _ := readState(co)
	if err := writeOutputs(co, &s, map[string][]byte{"old.md": []byte("v1")}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(co.Directory, "old.md"), []byte("v2"), 0600)
	journal, _ := json.Marshal(map[string][]string{"old.md": {digest([]byte("v2"))}})
	os.WriteFile(filepath.Join(co.Directory, ".lms-sync", "pending.json"), journal, 0600)
	if err := writeOutputs(co, &s, map[string][]byte{"new.md": []byte("v3")}); err != nil {
		t.Fatal(err)
	}
	if s.Files["old.md"] != digest([]byte("v2")) {
		t.Fatal("lost ownership of interrupted path")
	}
	if err := writeOutputs(co, &s, map[string][]byte{"old.md": []byte("v4")}); err != nil {
		t.Fatalf("renaming back failed: %v", err)
	}
}

func TestLessonAttachmentRefreshWithoutLeakingToken(t *testing.T) {
	co := testCourse(t)
	s, _ := readState(co)
	assetBody := "v1"
	downloads := 0
	original := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("API credential sent to attachment host")
		}
		downloads++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(assetBody)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	c := mockClient(t, func(r *http.Request) (int, any) {
		if strings.Contains(r.URL.Path, "/courses/") {
			return 200, map[string]any{"lessons": []edLesson{{ID: 2, CourseID: 1}}, "modules": []edModule{}}
		}
		return 200, map[string]any{"lesson": edLesson{ID: 2, CourseID: 1, Title: "Files", Slides: []edSlide{{ID: 3, Type: "document", Content: `<file url="https://static.edusercontent.com/a.pdf" filename="a.pdf"/>`}}}}
	})
	files := map[string][]byte{}
	n, err := syncLessons(context.Background(), c, co, &s, files, time.Now(), false)
	if err != nil || n != 1 {
		t.Fatalf("initial: %d %v", n, err)
	}
	if err = writeOutputs(co, &s, files); err != nil {
		t.Fatal(err)
	}
	n, err = syncLessons(context.Background(), c, co, &s, map[string][]byte{}, time.Now(), false)
	if err != nil || n != 0 || downloads != 1 {
		t.Fatalf("cache: %d %d %v", n, downloads, err)
	}
	assetBody = "v2"
	n, err = syncLessons(context.Background(), c, co, &s, map[string][]byte{}, time.Now(), true)
	if err != nil || n != 1 || downloads != 2 {
		t.Fatalf("same-URL replacement missed: %d %d %v", n, downloads, err)
	}
	// Simulate attachment v2 persisted but checkpoint still v1. The next full run
	// must recover through the early attachment conflict check before writeOutputs.
	persisted, err := readState(co)
	if err != nil {
		t.Fatal(err)
	}
	journal := map[string][]string{}
	for rel := range files {
		if strings.Contains(rel, "_assets") {
			os.WriteFile(filepath.Join(co.Directory, rel), []byte("v2"), 0600)
			journal[rel] = []string{digest([]byte("v2"))}
		}
	}
	j, _ := json.Marshal(journal)
	os.WriteFile(filepath.Join(co.Directory, ".lms-sync", "pending.json"), j, 0600)
	assetBody = "v3"
	recovered := map[string][]byte{}
	n, err = syncLessons(context.Background(), c, co, &persisted, recovered, time.Now(), true)
	if err != nil || n != 1 || downloads != 3 {
		t.Fatalf("interrupted asset refresh not recovered: %d %d %v", n, downloads, err)
	}
	if err = writeOutputs(co, &persisted, recovered); err != nil {
		t.Fatal(err)
	}
}
