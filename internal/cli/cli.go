// Package cli dispatches lms-mcp subcommands. It wires configuration,
// credentials, provider clients, the local store and the MCP server, and keeps
// terminal output on stdout (except in mcp mode, where stdout is the protocol).
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sbot5/lms-mcp/internal/version"
)

const usage = `lms-mcp — read-only Ed and Moodle mirror with a stdio MCP server

  setup            interactive first-time configuration
  auth ed          store the Ed API token
  auth moodle      store the Moodle session (cloud dev reads the environment)
  auth status      show which credentials are configured (values hidden)
  doctor           check credentials, connectivity, identity and courses
  capture ed       save a synthetic offline fixture (-kind, -id/-course-id, -out)
  sync             synchronize now (-full for a full refresh)
  status           show local freshness without network access
  list             list configured courses
  search <query>   full-text search the local index
  mcp              serve MCP over stdio
  version          print the version

Global flags (before subcommand arguments):
  -config <file>   config path; default is the OS config dir + lms-mcp/config.json
`

// Run dispatches args[0] as a subcommand. in/out/errOut are the process
// streams; out carries command results, errOut carries diagnostics.
func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := io.WriteString(out, usage)
		return err
	}
	cmd, rest := args[0], args[1:]
	if cmd == "version" {
		_, err := fmt.Fprintln(out, version.Version)
		return err
	}

	switch cmd {
	case "setup":
		return runSetup(ctx, rest, in, out, errOut)
	case "auth":
		return runAuth(ctx, rest, in, out, errOut)
	case "doctor":
		return runDoctor(ctx, rest, out, errOut)
	case "capture":
		return runCapture(ctx, rest, out, errOut)
	case "sync":
		return runSync(ctx, rest, out, errOut)
	case "status":
		return runStatus(ctx, rest, out)
	case "list":
		return runList(ctx, rest, out)
	case "search":
		return runSearch(ctx, rest, out)
	case "mcp":
		return runMCP(ctx, rest, errOut)
	default:
		return fmt.Errorf("unknown command %q; run `lms-mcp help`", cmd)
	}
}

// defaultConfigPath is the OS user config dir plus lms-mcp/config.json.
func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "config.json"
	}
	return filepath.Join(dir, "lms-mcp", "config.json")
}
