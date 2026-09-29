package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
)

const usage = `lms-mcp — configurable Ed/Moodle synchronization and stdio MCP

  init           interactive Ed course configuration (requires an existing token file)
  config validate  validate settings without network access or credentials
  status         show local synchronization freshness without credentials
  whoami         list enrolled courses (account identity hidden by default)
  threads <id>   list threads in a course
  thread <id>    read a thread and nested replies
  sync           synchronize configured courses; -full refreshes all attachments
  whats-new      list detected changes; -since, -course, -staff-only, -limit
  mcp            serve MCP over stdio
  version        print the version

Flags go before positional IDs:
  -config <file>    defaults to the OS user config directory/lms-mcp/config.json
  -env-file <file>  overrides config.env_file; never pass a token as an argument
  -region <code>    source-link region for init or standalone commands (au/us/eu)

With -env-file but no -config, sync.json beside the env file is used for compatibility.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lms-mcp:", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(out, usage)
		return err
	}
	cmd := args[0]
	args = args[1:]
	if cmd == "version" {
		_, err := fmt.Fprintln(out, version)
		return err
	}
	if cmd == "config" {
		if len(args) == 0 || args[0] != "validate" {
			return fmt.Errorf("usage: lms-mcp config validate -config <file>")
		}
		args = args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errOut)
	configPath := fs.String("config", "", "configuration JSON path")
	envFile := fs.String("env-file", "", "private .env path")
	region := fs.String("region", "", "source-link region (au/us/eu)")
	full := fs.Bool("full", false, "refresh all thread details and attachments")
	since := fs.String("since", "", "RFC3339 detection time")
	course := fs.String("course", "", "course code")
	staff := fs.Bool("staff-only", false, "staff changes only")
	limit := fs.Int("limit", 100, "maximum changes (1..1000)")
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	nargs := 0
	if cmd == "thread" || cmd == "threads" {
		nargs = 1
	}
	if fs.NArg() != nargs {
		return fmt.Errorf("invalid arguments; flags must precede positional IDs")
	}
	if *configPath == "" {
		if *envFile != "" {
			*configPath = filepath.Join(filepath.Dir(*envFile), "sync.json")
		} else {
			*configPath = defaultConfigPath()
		}
	}
	if cmd == "init" {
		return initConfig(ctx, *configPath, *envFile, *region, in, out)
	}
	var cfg syncConfig
	var err error
	switch cmd {
	case "config", "status", "sync", "whats-new", "mcp":
		cfg, err = loadConfig(*configPath)
	case "whoami", "thread", "threads":
		cfg, err = loadConfig(*configPath)
		if errors.Is(err, os.ErrNotExist) {
			cfg = syncConfig{Region: "au"}
			err = nil
		}
	default:
		return fmt.Errorf("unknown command %q; use --help", cmd)
	}
	if err != nil {
		return err
	}
	if *region != "" {
		if *region != "au" && *region != "us" && *region != "eu" {
			return fmt.Errorf("region must be au/us/eu")
		}
		if cmd != "whoami" && cmd != "thread" && cmd != "threads" {
			return fmt.Errorf("set region in the configuration for this command")
		}
		cfg.Region = *region
	}
	encode := func(v any) error { e := json.NewEncoder(out); e.SetIndent("", "  "); return e.Encode(v) }
	switch cmd {
	case "config":
		return encode(map[string]any{"valid": true, "courses": len(allStorageCourses(cfg)), "region": cfg.Region, "daily_at": cfg.Schedule.DailyAt})
	case "status":
		r, err := syncStatus(cfg)
		if err != nil {
			return err
		}
		return encode(r)
	case "whats-new":
		r, err := whatsNew(cfg, newsInput{Since: *since, Course: *course, StaffOnly: *staff, Limit: *limit})
		if err != nil {
			return err
		}
		return encode(r)
	}
	if *envFile == "" {
		*envFile = cfg.EnvFile
	}
	var c *edClient
	if len(cfg.Courses) > 0 || cmd == "whoami" || cmd == "thread" || cmd == "threads" {
		if *envFile == "" {
			err = fmt.Errorf("set env_file in your config or use -env-file")
		} else {
			var token string
			token, err = loadToken(*envFile)
			if err == nil {
				c = newEdClient(token)
				c.region = cfg.Region
				c.includeIdentity = cfg.Privacy.IncludeIdentity
				c.includePrivate = cfg.Privacy.IncludePrivatePosts
			}
		}
		if err != nil && cmd != "sync" && cmd != "mcp" {
			return err
		}
	}
	switch cmd {
	case "sync":
		r, err := syncAll(ctx, c, cfg, *full)
		if writeErr := encode(r); writeErr != nil {
			return writeErr
		}
		return err
	case "mcp":
		return runMCP(ctx, c, *configPath)
	case "whoami":
		return runWhoami(ctx, c, nil)
	case "threads":
		return runThreads(ctx, c, fs.Args())
	case "thread":
		return runThread(ctx, c, fs.Args())
	}
	return nil
}

func loadToken(path string) (string, error) {
	env, err := readEnvFile(path)
	if err != nil {
		return "", err
	}
	token := env["ED_API_TOKEN"]
	if token == "" {
		return "", fmt.Errorf("ED_API_TOKEN is missing or empty")
	}
	return token, nil
}
