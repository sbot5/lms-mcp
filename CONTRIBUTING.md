# Contributing

Use Go 1.25 or newer. All Go code lives under `cmd/` and `internal/`. Read
[AGENTS.md](AGENTS.md) for the hard rules (read-only toward Ed and Moodle,
credential handling, nothing identifying in this public repository) and
[docs/plan.md](docs/plan.md) for the milestone plan and decisions.

## Build and verify

```sh
gofmt -l cmd internal      # must print nothing
go vet ./...
go test ./...
go build -trimpath -o bin/lms-mcp ./cmd/lms-mcp
GOOS=windows GOARCH=amd64 go build -trimpath -o bin/lms-mcp.exe ./cmd/lms-mcp
```

CI runs gofmt, vet, test and build on Ubuntu and Windows with Go 1.25.x and
stable, plus a Windows amd64/arm64 cross-build. `scripts/check.ps1` runs the
same checks locally under PowerShell 7.

Dependencies must be pure Go (no CGO) so the Windows cross-build keeps working.
Pin `modernc.org/sqlite` to a version that still allows the `go` directive to
stay at 1.25 (1.60+ requires Go 1.26); see [docs/dev/m1-contracts.md](docs/dev/m1-contracts.md).

## Package boundaries

Read [the architecture guide](docs/architecture.md) before adding a package.
Provider clients (`internal/ed`, `internal/moodle`) talk HTTP only, through
`internal/httpx`, and never import the orchestration layer or each other. The
read-only invariant is enforced by the `httpx` guards, not only by convention.

## Tests

Tests are offline: anonymized fixtures under `testdata/` and loopback
`httptest` servers; no real credentials or course data. Keep tests beside the
code they exercise. Every bug fix gets a regression test. Never commit real
course content or anything identifying the user.

## Milestones

Work proceeds one milestone per pull request (see the plan's status table).
Commit messages are imperative and explain why. Update the plan's status table
when a milestone changes state.
