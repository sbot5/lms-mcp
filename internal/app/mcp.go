package app

import (
	"context"
	"fmt"

	"github.com/sbot5/lms-mcp/internal/ed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// runMCP serves MCP over stdin/stdout until the client disconnects.
// stdout carries the protocol, so nothing else may print to it; log goes to stderr.
func runMCP(ctx context.Context, c *edClient, configPath string) error {
	return newMCPServer(c, configPath).Run(ctx, &mcp.StdioTransport{})
}

func newMCPServer(c *edClient, configPath string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "lms-mcp", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ed_whoami",
		Description: "List enrolled Ed courses, newest semester first. Account name and user ID are hidden unless privacy.include_identity is enabled. Past courses are included; use year and session to find the current semester.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ed.WhoamiResult, error) {
		if c == nil {
			return nil, ed.WhoamiResult{}, fmt.Errorf("Ed is not configured or its credentials are unavailable")
		}
		cfg, err := loadConfig(configPath)
		if err != nil {
			return nil, ed.WhoamiResult{}, err
		}
		res, err := c.Whoami(ctx)
		if !cfg.Privacy.IncludeIdentity {
			res.Name = ""
			res.UserID = 0
		}
		return nil, res, err
	})

	mcp.AddTool(server, &mcp.Tool{Name: "whats_new", Description: "List locally detected Ed and Moodle changes. Moodle course filters use moodle:CODE. First sync is a baseline. last_success shows freshness; this tool does not synchronize."}, func(ctx context.Context, _ *mcp.CallToolRequest, in newsInput) (*mcp.CallToolResult, newsResult, error) {
		cfg, err := loadConfig(configPath)
		if err != nil {
			return nil, newsResult{}, err
		}
		res, err := whatsNew(cfg, in)
		return nil, res, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "sync_now", Description: "Synchronize configured Ed and Moodle sources to local course folders. Moodle credentials are read from its private env file. Can take several minutes."}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Full bool `json:"full,omitempty"`
	}) (*mcp.CallToolResult, syncResult, error) {
		cfg, err := loadConfig(configPath)
		if err != nil {
			return nil, syncResult{}, err
		}
		res, err := syncAll(ctx, c, cfg, in.Full)
		return nil, res, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_thread", Description: "Fetch a complete Ed thread and its nested replies by thread_id. Returns redacted Markdown, plain text, staff roles, post number and source URL."}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ThreadID int `json:"thread_id"`
	}) (*mcp.CallToolResult, record, error) {
		if c == nil {
			return nil, nil, fmt.Errorf("Ed is not configured or its credentials are unavailable")
		}
		if in.ThreadID <= 0 {
			return nil, nil, fmt.Errorf("thread_id must be positive")
		}
		cfg, err := loadConfig(configPath)
		if err != nil {
			return nil, nil, err
		}
		t, users, err := c.Thread(ctx, in.ThreadID)
		if err != nil {
			return nil, nil, err
		}
		if t.IsPrivate && !cfg.Privacy.IncludePrivatePosts {
			return nil, nil, fmt.Errorf("private posts are disabled")
		}
		for _, co := range cfg.Courses {
			if co.ID == t.CourseID {
				r, err := threadRecord(t, users, co)
				return nil, r, err
			}
		}
		return nil, nil, fmt.Errorf("thread belongs to an unconfigured course")
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_moodle_calendar", Description: "Read a Moodle course's cached calendar events and freshness, without a network request. Times/TZID and recurrence rules are retained; events are not necessarily assignment deadlines."}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		Course string `json:"course"`
	}) (*mcp.CallToolResult, moodleCalendarResult, error) {
		cfg, err := loadConfig(configPath)
		if err != nil {
			return nil, moodleCalendarResult{}, err
		}
		r, err := readMoodleCalendar(cfg, in.Course)
		return nil, r, err
	})
	return server
}
