package cli

import (
	"github.com/sbot5/lms-mcp/internal/config"
	"github.com/sbot5/lms-mcp/internal/mcpserver"
	"github.com/sbot5/lms-mcp/internal/secrets"
	"github.com/sbot5/lms-mcp/internal/store"
	"github.com/sbot5/lms-mcp/internal/syncer"
)

// buildStatus reports provider and credential readiness without exposing any
// secret value, plus the last sync run. Shared by the status command and the
// MCP get_status tool.
func buildStatus(cfg *config.Config, sec secrets.Store, db *store.DB) mcpserver.StatusReport {
	p, _ := syncer.Build(cfg, sec)
	r := mcpserver.StatusReport{CredentialBackend: sec.Backend()}
	r.Ed = mcpserver.ProviderStatus{Available: p.Ed != nil, Auth: p.EdAuth, Reason: p.EdReason}
	r.Moodle = mcpserver.ProviderStatus{Available: p.MoodleAuth != "", Auth: p.MoodleAuth, Reason: p.MoodleReason}
	if run, ok, err := db.LastSyncRun("all"); err == nil && ok {
		var courses int
		r.LastSync = &mcpserver.RunStatus{
			Scope:      run.Scope,
			Status:     run.Status,
			FinishedAt: run.FinishedAt,
			Courses:    courses,
			Error:      run.Error,
		}
	}
	return r
}
