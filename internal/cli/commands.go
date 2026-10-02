package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/sbot5/lms-mcp/internal/config"
)

// parseConfigFlag parses a flag set that accepts -config and returns the
// resolved config path plus the remaining positional arguments.
func parseConfigFlag(name string, args []string, errOut io.Writer) (*config.Config, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	configPath := fs.String("config", "", "configuration JSON path")
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

// notImplemented marks a command that a later M1 step fills in.
func notImplemented(name string) error {
	return fmt.Errorf("%s: not implemented yet in this build", name)
}

func runSetup(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	return notImplemented("setup")
}

func runDoctor(ctx context.Context, args []string, out, errOut io.Writer) error {
	return notImplemented("doctor")
}

func runSync(ctx context.Context, args []string, out, errOut io.Writer) error {
	return notImplemented("sync")
}

func runStatus(ctx context.Context, args []string, out io.Writer) error {
	return notImplemented("status")
}

func runList(ctx context.Context, args []string, out io.Writer) error {
	return notImplemented("list")
}

func runSearch(ctx context.Context, args []string, out io.Writer) error {
	return notImplemented("search")
}

func runCapture(ctx context.Context, args []string, out, errOut io.Writer) error {
	return notImplemented("capture")
}

func runMCP(ctx context.Context, args []string, errOut io.Writer) error {
	return notImplemented("mcp")
}
