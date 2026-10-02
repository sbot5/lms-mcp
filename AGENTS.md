# AGENTS.md

Guidance for coding agents (Codex, Claude Code and their subagents) working on lms-mcp.

## What this project is

lms-mcp is a personal, **read-only** sync tool and stdio MCP server. It mirrors one student's university Moodle site (SSO + MFA; the site URL comes from local configuration or the `MOODLE_BASE_URL` environment variable and is never committed) and Ed (`https://edstem.org`, AU region) data into a local folder plus a SQLite index. Claude Code (primary) and Codex (secondary) then query that index through MCP tools. The only target platform is Windows; there is a single user.

- **Plan, decisions and milestone status:** `docs/plan.md`. Read it before starting any work, and update its status table when a milestone changes state.
- `README.md`, `docs/architecture.md` and `docs/moodle.md` still describe v0.3.0 and the old Moodle preview. Milestone M1 rewrites them. Compatibility with v0.3.0 configuration, output or checkpoint formats is **not** required.

## Hard rules

1. **Read-only toward Ed and Moodle.** Ed: GET requests only, and never the `?view=1` parameter (it records lesson progress). Moodle: call only web-service functions on the read-only allowlist in `docs/plan.md` §4.4. Never post, submit, answer, star, explicitly mark anything as read or viewed, enrol or change preferences. Enforce this in the HTTP layer as well as by convention. The user has accepted that reading Ed thread details or Moodle forum posts may implicitly update the platform's read state (decision D7).
2. **Credentials** come from Windows Credential Manager, or from the environment variables `ED_API_TOKEN` and `MOODLE_TOKEN` (cloud development only). In cloud sessions the Ed token may instead be injected by the environment's API-credential proxy (`ED_AUTH=proxy`); in that case send no Authorization header. Never print, log, commit or paste them, and never accept them as command-line arguments or MCP tool arguments. Error messages must not contain request URLs that carry tokens.
3. **No real course data or identifying details in the repository; it is public.** Never commit the user's school name, Moodle hostname, course names or anything else that identifies the user. This covers code, docs, fixtures and commit messages. Raw API captures stay outside the repo and are deleted after use. Committed fixtures under `testdata/` must be anonymized as `docs/plan.md` describes: synthetic free text, remapped IDs, example hosts, and no names, emails, student numbers or tokens.
4. In `lms-mcp mcp` mode, stdout carries JSON-RPC only. Logs go to stderr or the log file.
5. Pure-Go dependencies only (no CGO), so `GOOS=windows` cross-compilation from Linux keeps working.

## Commands

```sh
gofmt -l cmd internal                     # must print nothing
go vet ./...
go test ./...
go build -trimpath -o bin/lms-mcp ./cmd/lms-mcp
GOOS=windows GOARCH=amd64 go build -trimpath -o bin/lms-mcp.exe ./cmd/lms-mcp   # Windows build check
```

## Conventions

- Go 1.25+, standard library first. Code lives under `cmd/` and `internal/`. Provider packages (`internal/ed`, `internal/moodle`) talk HTTP only and never import application packages or each other.
- MCP tools:
  - Names are snake_case, and each returns an object-root structured result. Claude Code and Codex show the model only `structuredContent`, so never put information only in text content.
  - Every tool has `readOnlyHint: true`.
  - List tools paginate with `limit` and `cursor`. Keep a result under about 8k tokens, and mark any truncation explicitly.
  - Descriptions stay under 1,000 characters, and the first sentence says what the tool is for, including synonyms (slides/lecture/课件, grades/marks).
- Tests are offline. They use anonymized fixtures in `testdata/` and loopback `httptest` servers. Every bug fix gets a regression test.
- Branches and PRs: one PR per milestone (see `docs/plan.md`). Commit messages are imperative and explain why.

## Subagents

`.claude/agents/` defines `researcher` (read-only research) and `implementer` (one well-scoped coding task in an isolated worktree). Both run Opus at high effort. Give them a self-contained brief with the goal, the relevant files, constraints and acceptance criteria. They must not depend on the parent conversation's history.
