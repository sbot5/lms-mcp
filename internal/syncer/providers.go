// Package syncer orchestrates read-only synchronization: it builds the provider
// clients, discovers and pairs courses, and runs sync as an async job guarded
// by a cross-process lease. Content sync for each provider is layered on in
// later milestones; M1 discovers and persists courses.
package syncer

import (
	"fmt"
	"net/url"

	"github.com/sbot5/lms-mcp/internal/config"
	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/httpx"
	"github.com/sbot5/lms-mcp/internal/moodle"
	"github.com/sbot5/lms-mcp/internal/secrets"
	"github.com/sbot5/lms-mcp/internal/version"
)

// Providers holds the configured, credentialed clients for a run. A provider
// is nil/absent when it is disabled or its credentials are unavailable.
type Providers struct {
	Cfg *config.Config

	Ed       *ed.Client
	EdAuth   string // "token", "proxy", or "" when Ed is unavailable
	EdReason string // why Ed is unavailable, for status/doctor

	Moodle       *moodle.Session
	MoodleBase   *url.URL
	MoodleAuth   string // "cookie", "proxy", or "" when unavailable
	MoodleCookie string // raw Cookie header; empty in proxy mode
	MoodleReason string
}

// Build constructs the providers from configuration and stored credentials. It
// never fails for a missing credential; it records the reason so doctor and
// status can report it, and leaves that provider absent.
func Build(cfg *config.Config, store secrets.Store) (*Providers, error) {
	p := &Providers{Cfg: cfg}
	ua := httpx.UserAgent(version.Version)

	if cfg.Ed.Enabled {
		if err := p.buildEd(cfg, store, ua); err != nil {
			p.EdReason = err.Error()
		}
	} else {
		p.EdReason = "Ed is disabled in config"
	}

	if cfg.Moodle.Enabled {
		if err := p.buildMoodle(cfg, store, ua); err != nil {
			p.MoodleReason = err.Error()
		}
	} else {
		p.MoodleReason = "Moodle is disabled (set moodle.base_url or MOODLE_BASE_URL)"
	}
	return p, nil
}

func (p *Providers) buildEd(cfg *config.Config, store secrets.Store, ua string) error {
	host := ed.RegionHost(cfg.Ed.Region)
	doer, err := httpx.New(httpx.Options{
		Guard:     httpx.EdGuard{APIHost: host},
		UserAgent: ua,
	})
	if err != nil {
		return err
	}
	var token string
	if secrets.Proxy("ed") {
		p.EdAuth = "proxy"
	} else {
		t, ok, err := store.Get(secrets.EdToken)
		if err != nil {
			return fmt.Errorf("reading Ed token failed")
		}
		if !ok {
			return fmt.Errorf("no Ed token; run `lms-mcp auth ed` or set ED_API_TOKEN")
		}
		token, p.EdAuth = t, "token"
	}
	p.Ed = ed.NewClient(doer, ed.APIBaseURL(cfg.Ed.Region), token)
	return nil
}

func (p *Providers) buildMoodle(cfg *config.Config, store secrets.Store, ua string) error {
	base, err := url.Parse(cfg.Moodle.BaseURL)
	if err != nil {
		return fmt.Errorf("invalid Moodle base URL")
	}
	p.MoodleBase = base
	if secrets.Proxy("moodle") {
		p.MoodleAuth = "proxy"
	} else {
		cookie, ok, err := store.Get(secrets.MoodleCookie)
		if err != nil {
			return fmt.Errorf("reading Moodle cookie failed")
		}
		if !ok {
			return fmt.Errorf("no Moodle session; run `lms-mcp auth moodle` or set MOODLE_COOKIE")
		}
		p.MoodleCookie, p.MoodleAuth = cookie, "cookie"
	}
	doer, err := httpx.New(httpx.Options{
		Guard:     httpx.MoodleGuard{Base: base},
		UserAgent: ua,
	})
	if err != nil {
		return err
	}
	p.Moodle = moodle.NewSession(doer, base, p.MoodleCookie, p.MoodleAuth == "proxy")
	return nil
}
