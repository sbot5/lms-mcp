# Architecture

lms-mcp builds one executable that mirrors Ed and Moodle course data into a
local folder and SQLite index, and serves that index to MCP clients. It is
strictly read-only toward Ed and Moodle (see `AGENTS.md`). This document
describes the v1 package layout; the development plan in
[plan.md](plan.md) is the source of truth for decisions and milestones.

```text
cmd/lms-mcp
    └── internal/cli
          ├── internal/config
          ├── internal/secrets
          ├── internal/syncer ── internal/mcpserver ── internal/notify
          │        └── internal/store · internal/files · internal/extract
          │        └── internal/ed · internal/moodle · internal/render
          └── internal/anonymize
internal/ed · internal/moodle ──> internal/httpx
```

Dependency direction: `cmd` → `cli` → orchestration (`syncer`, `mcpserver`,
`notify`) → local engines (`store`, `files`, `extract`, `render`) and provider
clients (`ed`, `moodle`) → the shared HTTP layer (`httpx`). Provider packages
never import `cli`, the orchestration layer, or each other.

| Package | Responsibility |
| --- | --- |
| `cmd/lms-mcp` | OS signals, standard streams, exit status. Calls `cli.Run`. |
| `internal/cli` | Parse subcommands and flags, wire configuration, credentials, clients, store and the MCP server, and print terminal output. In `mcp` mode stdout carries the protocol only. |
| `internal/config` | Load and validate `config.json`; resolve the data directory; pair Ed and Moodle courses by code; `MOODLE_BASE_URL` enables Moodle. Holds no credentials. |
| `internal/secrets` | Named credentials. Windows Credential Manager, or read-only environment variables elsewhere and in cloud development. `ED_AUTH`/`MOODLE_AUTH=proxy` mark proxy-injected headers. Values are never logged. |
| `internal/httpx` | The shared HTTP layer. Enforces the read-only invariant in the transport via a `Guard` (`EdGuard`: GET to Ed hosts only; `MoodleGuard`: a page/file allowlist plus AJAX POSTs whose methodnames are allowlisted, verified by parsing the batch). Adds a stable User-Agent, per-host rate limiting, bounded 429/5xx retries, and redacts request URLs from errors. |
| `internal/ed` | Ed API client over `httpx`. GET only; token optional (proxy mode sends no Authorization header). Response models for user, threads, lessons, resources. |
| `internal/moodle` | Moodle browser-session client over `httpx`: the unauthenticated public-config probe, sesskey retrieval, allowlisted AJAX calls, page and file reads, and iCalendar parsing. Also retains the earlier HTML discovery helpers. |
| `internal/render` | Convert Ed XML and Moodle HTML to Markdown; collect asset URLs; escape and redact. No network. |
| `internal/store` | SQLite (WAL) index: schema and migrations, items/files/grades/deadlines/events, FTS5 search, sync runs and cross-process leases. No knowledge of provider HTTP. |
| `internal/files` | Mirror materials to disk: streaming downloads, atomic replace, the per-file size cap, and video-as-link. (M3) |
| `internal/extract` | Per-page text extraction from PDF, PPTX, DOCX, ipynb and HTML, cached by content hash. (M4) |
| `internal/syncer` | Orchestrate a sync: discover and pair courses, run each provider incrementally, detect changes and emit events. Owns the async job (lease, goroutine, progress). |
| `internal/notify` | Notification rules, aggregation and Windows toasts. (M6) |
| `internal/mcpserver` | MCP tools, resources and prompts, plus pagination, opaque cursors and output budgeting. Every tool is read-only. |
| `internal/anonymize` | Structure-preserving redaction for the `capture` command's fixtures. |
| `internal/filelock` | Platform process locks (retained from v0.3.0). |

## Read-only invariant

Read-only is enforced in three layers: by convention in the provider clients,
by the `httpx` guards at the transport (every request is checked before it is
sent, and the Moodle AJAX allowlist is verified by parsing the request body),
and by never constructing side-effecting requests in the first place. A plain
GET of Ed thread details or Moodle forum posts may still update the platform's
own read state; this is accepted (plan D7). "New" content is decided by
lms-mcp's own change detection, not the platform's unread flags.

Ed lesson requests carrying `view` are refused before sending. Remote requests
require HTTPS, and attachment requests must not carry API credentials. When
redirect following is explicitly enabled, each destination is checked again;
credentials are removed on an origin change and POST redirects are refused.

## Local data

```text
<data dir>/
  lms.db (+ -wal, -shm)   metadata, bodies, extracted text, FTS, events, runs
  logs/                   sync logs (no credentials, no token-bearing URLs)
  <CODE>-<TERM>/          per course
    ed/lessons/… ed/resources/…
    moodle/<section>/…
```

Discussion posts, announcements, grades and deadlines live in SQLite and are
rendered to Markdown by tools. Materials and attachments are written to disk so
Claude Code can open the originals. A remote deletion marks the index row
removed; it never deletes the local copy.

## Credentials and cloud development

Credentials never live in the repository. On Windows they are stored in
Credential Manager; in a cloud development session they come from environment
variables, or from the environment's API-credential proxy (`ED_AUTH=proxy`,
`MOODLE_AUTH=proxy`), which injects the Authorization or Cookie header after a
request leaves the sandbox so the secret never enters it. The Moodle site URL
is supplied by `MOODLE_BASE_URL` (or local config) and is not committed.

## Concurrency

The MCP server, a scheduled sync and an interactive command may each run as a
separate process against the same database. The store opens SQLite in WAL mode
with a busy timeout; a sync holds a cross-process lease so only one runs at a
time. Read tools serve the index and never sync implicitly.

A job remains running until its outcome is persisted, renewal stops and its
lease is released. Loss of the lease cancels the run. Provider errors are
collected independently so a failing Ed account does not prevent Moodle
discovery, or vice versa. Paired courses are counted once.

Ed discovery compares known year/semester values, leaving unknown terms
discoverable without guessing dates. Moodle requests the `inprogress` timeline.
Stable provider IDs preserve pairings; historical rows remain in the index but
are not used as candidates for a new current-course pairing.
