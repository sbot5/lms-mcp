package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sbot5/lms-mcp/internal/config"
)

// runSetup writes config.json interactively. It refuses to overwrite an
// existing config unless -force is given. Credentials are not handled here; the
// wizard points the user at `auth ed` and `auth moodle`.
func runSetup(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(errOut)
	configPath := fs.String("config", "", "configuration JSON path")
	force := fs.Bool("force", false, "overwrite an existing configuration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := *configPath
	if path == "" {
		path = defaultConfigPath()
	}
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s already exists; use -force to overwrite", path)
	}

	r := bufio.NewReader(in)
	ask := func(prompt, def string) string {
		fmt.Fprintf(out, "%s [%s]: ", prompt, def)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}
	yes := func(prompt string, def bool) bool {
		d := "y"
		if !def {
			d = "n"
		}
		v := strings.ToLower(ask(prompt+" (y/n)", d))
		return strings.HasPrefix(v, "y")
	}

	cfg := config.Config{}
	cfg.DataDir = ask("Data directory (holds the index and downloads)", "data")
	cfg.Ed.Enabled = yes("Sync Ed?", true)
	if cfg.Ed.Enabled {
		cfg.Ed.Region = ask("Ed region (au/us/eu)", "au")
	}
	cfg.Moodle.Enabled = yes("Sync Moodle?", true)
	if cfg.Moodle.Enabled {
		cfg.Moodle.BaseURL = ask("Moodle base URL (e.g. https://moodle.example.edu)", "")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Fprintf(out, "\nWrote %s\nNext steps:\n", path)
	if cfg.Ed.Enabled {
		fmt.Fprintln(out, "  lms-mcp auth ed       # store your Ed API token")
	}
	if cfg.Moodle.Enabled {
		fmt.Fprintln(out, "  lms-mcp auth moodle   # store your Moodle session")
	}
	fmt.Fprintln(out, "  lms-mcp doctor        # verify connectivity")
	fmt.Fprintln(out, "  lms-mcp sync          # first sync")
	return nil
}
