package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sbot5/lms-mcp/internal/store"
	"github.com/sbot5/lms-mcp/internal/syncer"
	"github.com/sbot5/lms-mcp/internal/version"
)

// Deps are the dependencies the MCP tools read. All tools are read-only toward
// Ed and Moodle; sync only touches the local cache.
type Deps struct {
	DB     *store.DB
	Sync   *syncer.Service
	Status func() StatusReport // provider/credential status without secret values
}

// StatusReport summarizes configuration without exposing any secret value.
type StatusReport struct {
	CredentialBackend string         `json:"credential_backend"`
	Ed                ProviderStatus `json:"ed"`
	Moodle            ProviderStatus `json:"moodle"`
	LastSync          *RunStatus     `json:"last_sync,omitempty"`
}

// ProviderStatus is one provider's readiness.
type ProviderStatus struct {
	Available bool   `json:"available"`
	Auth      string `json:"auth,omitempty"`   // token, proxy, cookie
	Reason    string `json:"reason,omitempty"` // why unavailable
}

// RunStatus is the most recent sync run.
type RunStatus struct {
	Scope      string `json:"scope"`
	Status     string `json:"status"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	Courses    int    `json:"courses,omitempty"`
	Error      string `json:"error,omitempty"`
}

const instructions = `lms-mcp mirrors your Ed and Moodle courses into a local, read-only index.
Workflow: read from the local index first; if a tool reports stale data, call sync_start and poll sync_status, then read again. Nothing is ever posted or submitted to Ed or Moodle.
Lists paginate with limit (default 25, max 100) and an opaque cursor; pass the returned next_cursor to continue. Results are capped in size and mark truncation explicitly.`

func falsePtr() *bool { b := false; return &b }

// New builds the MCP server with the read-only tool set.
func New(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "lms-mcp", Version: version.Version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	readOnly := func(title string) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: falsePtr()}
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_courses",
		Description: "List the configured courses (units) with their Ed/Moodle pairing and last sync time. Reads the local index; does not contact Ed or Moodle.",
		Annotations: readOnly("List courses"),
	}, d.listCourses)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_status",
		Description: "Show which credentials and providers are configured (values hidden) and the last sync result. Use this to diagnose missing data.",
		Annotations: readOnly("Status"),
	}, d.getStatus)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "sync_start",
		Description: "Start a background sync of the local index from Ed and Moodle. Returns a job_id immediately; poll sync_status. Reuses a running job. Only the local cache changes.",
		Annotations: readOnly("Start sync"),
	}, d.syncStart)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "sync_status",
		Description: "Report a sync job's progress by job_id, or the current job when job_id is omitted.",
		Annotations: readOnly("Sync status"),
	}, d.syncStatus)

	return s
}

// Serve runs the MCP server over stdio until the client disconnects.
func Serve(ctx context.Context, d Deps) error {
	return New(d).Run(ctx, &mcp.StdioTransport{})
}

// ---- tool inputs/outputs and handlers ----

type listCoursesIn struct {
	IncludeInactive bool   `json:"include_inactive,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
}

type courseOut struct {
	ID        string `json:"id"`
	Code      string `json:"code,omitempty"`
	Term      string `json:"term,omitempty"`
	Title     string `json:"title"`
	Providers string `json:"providers"` // "ed", "moodle", or "ed+moodle"
	Active    bool   `json:"active"`
	Excluded  bool   `json:"excluded,omitempty"`
}

type listCoursesOut struct {
	Courses    []courseOut `json:"courses"`
	Total      int         `json:"total"`
	HasMore    bool        `json:"has_more"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

func (d Deps) listCourses(ctx context.Context, _ *mcp.CallToolRequest, in listCoursesIn) (*mcp.CallToolResult, listCoursesOut, error) {
	all, err := d.DB.ListCourses()
	if err != nil {
		return nil, listCoursesOut{}, err
	}
	filtered := all[:0:0]
	for _, c := range all {
		if !in.IncludeInactive && !c.Active {
			continue
		}
		filtered = append(filtered, c)
	}
	offset, err := DecodeCursor(in.Cursor)
	if err != nil {
		return nil, listCoursesOut{}, err
	}
	lo, hi, page := Paginate(len(filtered), offset, in.Limit)
	out := listCoursesOut{Total: len(filtered), HasMore: page.HasMore, NextCursor: page.NextCursor}
	for _, c := range filtered[lo:hi] {
		out.Courses = append(out.Courses, courseOut{
			ID: c.ID, Code: c.Code, Term: c.Term, Title: c.Title,
			Providers: providerLabel(c), Active: c.Active, Excluded: c.Excluded,
		})
	}
	return nil, out, nil
}

func providerLabel(c store.Course) string {
	switch {
	case c.EdCourseID != 0 && c.MoodleCourseID != 0:
		return "ed+moodle"
	case c.MoodleCourseID != 0:
		return "moodle"
	default:
		return "ed"
	}
}

func (d Deps) getStatus(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, StatusReport, error) {
	if d.Status == nil {
		return nil, StatusReport{}, fmt.Errorf("status unavailable")
	}
	return nil, d.Status(), nil
}

type syncStartIn struct {
	Scope string `json:"scope,omitempty"` // all (default), ed, moodle
	Full  bool   `json:"full,omitempty"`
}

func (d Deps) syncStart(ctx context.Context, _ *mcp.CallToolRequest, in syncStartIn) (*mcp.CallToolResult, syncer.JobStatus, error) {
	scope := in.Scope
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "ed" && scope != "moodle" {
		return nil, syncer.JobStatus{}, fmt.Errorf("scope must be all, ed or moodle")
	}
	return nil, d.Sync.Start(scope, in.Full), nil
}

type syncStatusIn struct {
	JobID string `json:"job_id,omitempty"`
}

func (d Deps) syncStatus(ctx context.Context, _ *mcp.CallToolRequest, in syncStatusIn) (*mcp.CallToolResult, syncer.JobStatus, error) {
	if in.JobID == "" {
		if st, ok := d.Sync.Current(); ok {
			return nil, st, nil
		}
		return nil, syncer.JobStatus{Phase: "idle"}, nil
	}
	st, ok := d.Sync.Status(in.JobID)
	if !ok {
		return nil, syncer.JobStatus{}, fmt.Errorf("unknown job_id")
	}
	return nil, st, nil
}
