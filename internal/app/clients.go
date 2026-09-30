package app

import (
	"fmt"

	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/moodle"
)

// CLI presentation settings belong to the application, not the Ed transport.
type edClient struct {
	*ed.Client
	region          string
	includeIdentity bool
	includePrivate  bool
}

func newEdClient(token string) *edClient {
	return &edClient{Client: ed.NewClient(token, nil)}
}

func newMoodleClient(cfg moodleConfig) (*moodle.Client, error) {
	env, err := readEnvFile(cfg.EnvFile)
	if err != nil {
		return nil, fmt.Errorf("read Moodle credential file: %w", err)
	}
	return moodle.NewClient(cfg.BaseURL, cfg.Auth, env)
}
