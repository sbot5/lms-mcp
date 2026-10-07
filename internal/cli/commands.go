package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sbot5/lms-mcp/internal/config"
	"github.com/sbot5/lms-mcp/internal/mcpserver"
	"github.com/sbot5/lms-mcp/internal/secrets"
	"github.com/sbot5/lms-mcp/internal/store"
	"github.com/sbot5/lms-mcp/internal/syncer"
)

// parseConfigFlag parses a flag set that accepts -config plus extra flags
// registered by setup, and returns the resolved config and positional args.
func parseConfigFlag(name string, args []string, errOut io.Writer, extra func(*flag.FlagSet)) (*config.Config, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	configPath := fs.String("config", "", "configuration JSON path")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	path := *configPath
	if path == "" {
		path = defaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	return cfg, fs.Args(), nil
}

// open wires the store and a sync service for a command. The caller closes db.
func open(cfg *config.Config) (*store.DB, *syncer.Service, secrets.Store, error) {
	sec, err := secrets.Open(credentialTarget)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := os.MkdirAll(cfg.DataPath(), 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, nil, nil, err
	}
	build := func() (*syncer.Providers, error) { return syncer.Build(cfg, sec) }
	return db, syncer.NewService(db, build), sec, nil
}

func encode(out io.Writer, v any) error {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

func runSync(ctx context.Context, args []string, out, errOut io.Writer) error {
	var full bool
	var scope string
	cfg, _, err := parseConfigFlag("sync", args, errOut, func(fs *flag.FlagSet) {
		fs.BoolVar(&full, "full", false, "full refresh")
		fs.StringVar(&scope, "scope", "all", "all, ed or moodle")
	})
	if err != nil {
		return err
	}
	db, svc, _, err := open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	job := svc.Start(scope, full)
	// CLI sync runs to completion: poll until the job leaves "running".
	for job.Phase == "running" {
		time.Sleep(200 * time.Millisecond)
		if s, ok := svc.Status(job.ID); ok {
			job = s
		} else {
			break
		}
	}
	if err := encode(out, job); err != nil {
		return err
	}
	if job.Phase == "error" {
		return fmt.Errorf("sync failed")
	}
	return nil
}

func runStatus(ctx context.Context, args []string, out io.Writer) error {
	cfg, _, err := parseConfigFlag("status", args, io.Discard, nil)
	if err != nil {
		return err
	}
	db, _, sec, err := open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	return encode(out, buildStatus(cfg, sec, db))
}

func runList(ctx context.Context, args []string, out io.Writer) error {
	var all bool
	cfg, _, err := parseConfigFlag("list", args, io.Discard, func(fs *flag.FlagSet) {
		fs.BoolVar(&all, "all", false, "include inactive courses")
	})
	if err != nil {
		return err
	}
	db, _, _, err := open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	courses, err := db.ListCourses()
	if err != nil {
		return err
	}
	type row struct {
		ID, Code, Term, Title string
		Active                bool
	}
	rows := []row{}
	for _, c := range courses {
		if !all && !c.Active {
			continue
		}
		rows = append(rows, row{c.ID, c.Code, c.Term, c.Title, c.Active})
	}
	return encode(out, rows)
}

func runSearch(ctx context.Context, args []string, out io.Writer) error {
	cfg, rest, err := parseConfigFlag("search", args, io.Discard, nil)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: lms-mcp search [-config <file>] <query>")
	}
	db, _, _, err := open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	hits, err := db.Search(rest[0], store.ItemFilter{Limit: 20})
	if err != nil {
		return err
	}
	return encode(out, hits)
}

func runMCP(ctx context.Context, args []string, errOut io.Writer) error {
	cfg, _, err := parseConfigFlag("mcp", args, errOut, nil)
	if err != nil {
		return err
	}
	db, svc, sec, err := open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	return mcpserver.Serve(ctx, mcpserver.Deps{
		DB:       db,
		Sync:     svc,
		DataRoot: cfg.DataPath(),
		Status:   func() mcpserver.StatusReport { return buildStatus(cfg, sec, db) },
	})
}
