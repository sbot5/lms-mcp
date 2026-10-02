package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/sbot5/lms-mcp/internal/syncer"
)

// check is one diagnostic line.
type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

func runDoctor(ctx context.Context, args []string, out, errOut io.Writer) error {
	cfg, _, err := parseConfigFlag("doctor", args, errOut, nil)
	if err != nil {
		return err
	}
	db, _, sec, err := open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	var checks []check
	checks = append(checks, check{Name: "credential backend", OK: true, Detail: sec.Backend()})

	p, _ := syncer.Build(cfg, sec)

	// Ed: live identity and course discovery.
	if p.Ed == nil {
		checks = append(checks, check{Name: "ed", OK: !cfg.Ed.Enabled, Detail: p.EdReason})
	} else {
		who, err := p.Ed.Whoami(ctx)
		if err != nil {
			checks = append(checks, check{Name: "ed.whoami", OK: false, Detail: "request failed; check token and access"})
		} else {
			checks = append(checks, check{Name: "ed.whoami", OK: true, Detail: fmt.Sprintf("%s auth, %d courses", p.EdAuth, len(who.Courses))})
		}
	}

	// Moodle: readiness. Live probing arrives with the session client.
	if p.MoodleAuth == "" {
		checks = append(checks, check{Name: "moodle", OK: !cfg.Moodle.Enabled, Detail: p.MoodleReason})
	} else {
		checks = append(checks, check{Name: "moodle", OK: true, Detail: p.MoodleAuth + " session configured (live probe pending)"})
	}

	ok := true
	for _, c := range checks {
		if !c.OK {
			ok = false
		}
	}
	if err := encode(out, map[string]any{"ok": ok, "checks": checks}); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("doctor found problems")
	}
	return nil
}
