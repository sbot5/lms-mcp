# lms-mcp

[![CI](https://github.com/sbot5/lms-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/sbot5/lms-mcp/actions/workflows/ci.yml)

A configurable, read-only Ed Discussion and Lessons synchronizer with a stdio MCP server. Save course posts, nested replies, lessons and attachments into local folders, then ask an MCP client what changed.

**Ed is supported; this development branch adds a Moodle preview.** Moodle token/Cookie materials and iCalendar feeds are covered by offline fixtures, with live-site acceptance still pending. See [Moodle setup and limitations](docs/moodle.md). The published v0.3.0 release remains Ed-only. There is no GUI; Ed's setup wizard and JSON settings cover configuration.

## Install

Download an archive for your OS and CPU from [Releases](https://github.com/sbot5/lms-mcp/releases). Extract the whole archive, including `scripts/` if you want Windows scheduling. Compare the archive SHA-256 with `checksums.txt`. On macOS/Linux, run `chmod +x lms-mcp` after extracting.

Or build from source with **Go 1.25 or newer**:

```sh
git clone --branch codex/moodle-sync https://github.com/sbot5/lms-mcp.git
cd lms-mcp
go build -trimpath -buildvcs=false -o bin/lms-mcp ./cmd/lms-mcp
# Windows: pwsh -File scripts/build.ps1 (writes bin/lms-mcp.exe)
```

These source commands target the current development preview; the published v0.3.0 release remains Ed-only. The examples below assume `lms-mcp` is on PATH. For a source checkout, use `./bin/lms-mcp` or `.\bin\lms-mcp.exe`. Release archives contain the binary at their root.

## First setup

1. Create an Ed API token in your own Ed account settings.
2. In a **private folder outside this repository**, create `.env` from `examples/.env.example` and replace the placeholder:

```dotenv
ED_API_TOKEN=replace-with-your-own-token
```

3. Run the setup wizard:

```sh
lms-mcp init -env-file /absolute/private/.env -config /absolute/private/config.json
```

The wizard reads your enrolled courses, lets you select courses, and asks for an output folder, Ed source-link region, Lessons/attachment preferences and daily local time. It never prints the token. It refuses to overwrite an existing configuration and does not install scheduling automatically.

4. Validate and run:

```sh
lms-mcp config validate -config /absolute/private/config.json
lms-mcp sync -config /absolute/private/config.json
lms-mcp status -config /absolute/private/config.json
lms-mcp whats-new -config /absolute/private/config.json -staff-only
```

`config validate`, `status`, and `whats-new` use local files only and do not require a token. With no `-config`, the default is the OS user config directory plus `lms-mcp/config.json`. For compatibility, providing `-env-file` without `-config` uses `sync.json` beside that env file.

## Configuration

Copy `examples/sync.example.json` to a private folder or edit the wizard-generated file. Relative paths resolve against the config file location, not the working directory.

```json
{
  "region": "au",
  "env_file": ".env",
  "full_refresh_hours": 24,
  "download_attachments": true,
  "privacy": {
    "include_identity": false,
    "include_private_posts": false
  },
  "schedule": { "daily_at": "09:00" },
  "courses": [
    {
      "id": 12345,
      "code": "COURSE101",
      "name": "Example course",
      "directory": "courses/COURSE101",
      "discussions": true,
      "lessons": true
    }
  ]
}
```

| Setting | Behavior |
| --- | --- |
| `region` | `au` (default), `us`, or `eu`; controls Ed source links. Choose the prefix shown in your Ed course URL. Authentication still uses the shared Ed API host; access depends on your token. |
| `env_file` | Private token file. A CLI `-env-file` overrides it. No token value belongs in JSON or MCP arguments. |
| `full_refresh_hours` | Discussion detail refresh interval, 1–8760 hours; default 24. A manual or scheduled `-full` always refreshes details and attachments. |
| `download_attachments` | Default true. Set false to keep lesson attachments as remote links. Discussion attachments and videos remain links. |
| `privacy.include_identity` | Default false; hides account name and user ID from `whoami` / `ed_whoami`. |
| `privacy.include_private_posts` | Default false; excludes private posts/replies from current discussion exports and queries. Old state/backups are retained; see SECURITY.md. |
| `schedule.daily_at` | Daily Windows local time. Rerun the task installer after changing it. |
| `courses[].discussions` | Default true. False skips discussion synchronization and preserves existing files. |
| `courses[].lessons` | Set true to synchronize visible Lessons; false skips them and preserves previous files. |
| `courses[].directory` | Output folder. Course folders must be distinct and must not contain one another. |

Unknown settings, duplicate IDs, overlapping output folders and malformed schedules are rejected. Configuration changes take effect on the next sync or MCP tool call. Restart the MCP process after changing the token file. If changing region, use a fresh output directory rather than mixing checkpoints.

## Daily Windows scheduling

Requires **PowerShell 7** and a signed-in Windows user. From the extracted release/source folder:

```powershell
.\scripts\install-task.ps1 -Config C:\private\lms\config.json
# Override the configured time:
.\scripts\install-task.ps1 -Config C:\private\lms\config.json -At '08:30'
Get-ScheduledTask -TaskName 'LMS-MCP-Ed-*'
```

The task name is derived from the config path, so separate configurations have separate tasks. The installer prints the name and next run time. It first looks for `bin/lms-mcp.exe` in a source checkout, then the root-level binary in a release archive. Supply `-Executable` for any other location. Supply `-EnvFile` to override the config's token-file path. `-TaskName` is available for an explicit name; unmanaged tasks are never overwritten or removed.

The task runs hidden with limited privileges. It checks at the configured time and at logon, catches missed starts when possible, and skips repeated runs after a success on the same local date unless the configuration changes. It runs `sync -full`, including attachment content checks. Failed runs retry up to three times at 15-minute intervals. It cannot run while the computer is off or the user is logged out. No popup notifications are installed.

Logs and `latest.json` are stored in `.lms-sync-logs/` beside the config. Inspect the exit code and log path there. To remove only this configuration's task:

```powershell
.\scripts\install-task.ps1 -Config C:\private\lms\config.json -Remove
```

Linux/macOS users can invoke `lms-mcp sync -config /absolute/private/config.json -full` from their preferred scheduler. This release does not install systemd, cron, or launchd jobs.

## MCP connection

Configure your MCP client with an absolute executable path and the private config path:

```json
{
  "mcpServers": {
    "lms-mcp": {
      "command": "/absolute/path/to/lms-mcp",
      "args": ["mcp", "-config", "/absolute/private/config.json"]
    }
  }
}
```

On Windows, use the `.exe` path and escape backslashes in JSON. Keep the connection file private: it contains your local paths, though it contains no token. Restart existing clients after updating the binary.

| Tool | Purpose |
| --- | --- |
| `ed_whoami` | List enrolled courses; account identity is hidden by default. |
| `whats_new` | Query detected changes. Optional `since` (RFC3339), `course`, `staff_only`, and `limit`; returns per-course freshness. |
| `get_thread` | Fetch a complete thread by `thread_id`, restricted to configured courses and the private-post policy. |
| `sync_now` | Synchronize configured courses. Optional `full` forces detail and attachment refresh. |
| `get_moodle_calendar` | Read a Moodle course's successfully cached calendar events and freshness by `course` code; no network request. |

MCP uses stdio, with progress on stderr. Scheduling is independent: the daily job continues when the MCP client is closed.

Other CLI commands: `whoami`, `threads <course-id>`, `thread <thread-id>`, and `version`. Put flags before positional IDs.

## Output and reliability

Each course output folder contains `_discussion.jsonl` (full threads/replies), `_discussion.md` (index), `_updates.md` (detection history), `ed-lessons/` (module-organized Markdown and `_assets/`), and private `.lms-sync/` checkpoint/backups/recovery data.

First import is marked `baseline` and excluded from recent-change queries. Pinned posts and zero-reply posts are included. Staff filtering includes changes to staff replies under student posts. Parent timestamp/view-count-only changes do not create alerts. Ed lists have no snapshot cursor: two matching full scans reduce pagination races, and later activity appears on the next run.

Lessons are fetched on every run because their timestamps may be null. Accessible challenge text and quiz questions are exported; nothing is submitted. `-full` also detects replacement attachments at the same URL. Ed-hosted attachments have a 64 MiB per-file limit; external sites and videos remain links.

Existing output is backed up before replacement. Human edits to managed files cause a visible conflict instead of being overwritten. Each file is replaced atomically, and a durable recovery journal handles interrupted writes. The checkpoint advances after the whole course succeeds; one course failing does not block the others. Kernel locks prevent concurrent writes to one course. The entire folder is not an atomic snapshot; readers should consult the successful checkpoint while a sync is running.

Unavailable lessons leave the current index but their old files remain. Backups and checkpoints can contain old private data. Exported course content stays private; this project is not a general anonymizer. Read [SECURITY.md](SECURITY.md) before sharing logs or exports.

## Troubleshooting

- **401/403:** check the API token and course access. Renew the token when needed, then restart MCP clients. No credentials are printed in API errors.
- **Local edit conflict:** move your edited copy outside the managed path, then rerun sync. Backups are under `.lms-sync/backups/<hash>/`. Do not delete state to force an overwrite.
- **No fresh changes:** initial import is a baseline. Check `status`, the scheduler's last result, and `latest.json` for freshness.
- **Task did not run:** confirm PowerShell 7 is installed and the user is signed in. Rerun the installer after changing the schedule or moving files.
- **Busy course list:** retry; list pagination may have changed during the scan.

## Development

```text
cmd/lms-mcp/         executable entry point
internal/app/        CLI, MCP, configuration, synchronization and export policy
internal/ed/         Ed HTTP client and response models
internal/moodle/     Moodle HTTP, HTML discovery and iCalendar parsing
internal/render/     pure Ed XML-to-Markdown conversion
internal/filelock/   Windows/Unix process locks
examples/           synthetic configuration and credential templates
docs/               architecture, Moodle guide and versioned release notes
scripts/            build, checks, packaging and Windows scheduling
bin/                local builds (ignored)
dist/               release archives (ignored)
```

See [architecture](docs/architecture.md) and [contributing](CONTRIBUTING.md) for package boundaries and the release checklist.

```sh
go test ./...
go vet ./...
go build -trimpath -buildvcs=false -o bin/lms-mcp ./cmd/lms-mcp
# All checks plus offline CLI smoke tests (PowerShell 7):
pwsh -File scripts/check.ps1
```

Tests use synthetic fixtures; no real token or course data is required. CI tests Windows, Linux and macOS. Tagged releases produce amd64/arm64 archives and SHA-256 checksums. Build locally with `pwsh -File scripts/build-release.ps1`; choose a fresh output directory for each build.

The Moodle preview also supports Moodle-only configurations (`courses: []` with `moodle.courses`). `sync`, `status`, and `whats-new` include both providers; use `moodle:COURSE_CODE` to filter Moodle changes. Moodle credentials and optional calendar URLs live in its separate private env file. The Ed `init` wizard is unchanged; start Moodle setup from `examples/moodle.example.json`.

MIT licensed. This is an independent project, not an official Ed product.
