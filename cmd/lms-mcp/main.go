// Command lms-mcp synchronizes course materials and serves the stdio MCP API.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/sbot5/lms-mcp/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := app.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lms-mcp:", err)
		os.Exit(1)
	}
}
