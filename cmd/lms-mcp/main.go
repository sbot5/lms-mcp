// Command lms-mcp mirrors Ed and Moodle course data locally and serves a
// read-only stdio MCP server.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/sbot5/lms-mcp/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lms-mcp:", err)
		os.Exit(1)
	}
}
