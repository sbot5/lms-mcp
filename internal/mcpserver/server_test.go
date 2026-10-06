package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sbot5/lms-mcp/internal/store"
	"github.com/sbot5/lms-mcp/internal/syncer"
)

func testDeps(t *testing.T) Deps {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "lms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.UpsertCourse(store.Course{ID: "ABC1234-2026S2", Code: "ABC1234", Term: "2026S2", Title: "Algorithms", EdCourseID: 10, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCourse(store.Course{ID: "OLD1000-2025S1", Code: "OLD1000", Term: "2025S1", Title: "Old", EdCourseID: 9, Active: false}); err != nil {
		t.Fatal(err)
	}
	svc := syncer.NewService(db, func() (*syncer.Providers, error) {
		return &syncer.Providers{}, nil // no providers: a sync does nothing but still completes
	})
	return Deps{
		DB:   db,
		Sync: svc,
		Status: func() StatusReport {
			return StatusReport{CredentialBackend: "env", Ed: ProviderStatus{Available: true, Auth: "token"}}
		},
	}
}

// connect wires an in-memory client to the server.
func connect(t *testing.T, d Deps) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	srv := New(d)
	go func() { _ = srv.Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestToolsListReadOnly(t *testing.T) {
	cs := connect(t, testDeps(t))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"list_courses": false, "get_status": false, "sync_start": false, "sync_status": false}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; !ok {
			continue
		}
		want[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s must have readOnlyHint=true", tool.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %s missing from tools/list", name)
		}
	}
}

func TestListCoursesStructured(t *testing.T) {
	cs := connect(t, testDeps(t))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_courses"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	// The model sees structuredContent; assert it carries the active course only.
	var out listCoursesOut
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structuredContent not decodable: %v", err)
	}
	if out.Total != 1 || len(out.Courses) != 1 || out.Courses[0].ID != "ABC1234-2026S2" {
		t.Fatalf("unexpected courses: %+v", out)
	}
	if out.Courses[0].Providers != "ed" {
		t.Fatalf("providers label = %q", out.Courses[0].Providers)
	}

	// include_inactive returns both.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_courses", Arguments: map[string]any{"include_inactive": true}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &out)
	if out.Total != 2 {
		t.Fatalf("include_inactive total = %d, want 2", out.Total)
	}
}

func TestSyncStartStatus(t *testing.T) {
	cs := connect(t, testDeps(t))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sync_start", Arguments: map[string]any{"scope": "all"}})
	if err != nil {
		t.Fatal(err)
	}
	var job syncer.JobStatus
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &job); err != nil || job.ID == "" {
		t.Fatalf("sync_start job: %+v err=%v", job, err)
	}
	// Poll sync_status until the job leaves running.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sync_status", Arguments: map[string]any{"job_id": job.ID}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(r.StructuredContent)
		_ = json.Unmarshal(raw, &job)
		if job.Phase == "done" || job.Phase == "error" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// This fixture deliberately has no providers. The completed background job
	// must expose a failure in structuredContent rather than report a false success.
	if job.Phase != "error" || job.Message != "no sync providers are available" {
		t.Fatalf("missing provider failure in structuredContent: %+v", job)
	}
}

func TestSyncStartRejectsBadScope(t *testing.T) {
	cs := connect(t, testDeps(t))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sync_start", Arguments: map[string]any{"scope": "bogus"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("bad scope should produce a tool error")
	}
}
