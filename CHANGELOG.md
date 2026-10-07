# Changelog

## Unreleased

- M2 v1 development: read-only Ed lessons, resources, discussions, own responses/submissions, released solutions/marks and recorded deadlines.
- Staged streaming mirrors with conditional/full downloads, hash conflict protection, version backups and rollback coordinated with atomic SQLite snapshots and events.
- Seven bounded local MCP content tools and synthetic fixture capture; unknown fields/names are anonymized and API credentials are excluded from exports.
- Course failure isolation, daily discussion reconciliation, withdrawn-grade visibility and removed-parent descendant reconciliation. Live course acceptance remains pending.

- Reorganize executable entry, application orchestration, provider clients, rendering and file locks into `cmd/` and `internal/` packages; move templates to `examples/`.
- Add common build/check scripts, documented package boundaries, and release/CI checks for the new layout. Command arguments and saved configuration/checkpoint formats are unchanged.

- Moodle preview: token/Cookie material sync, conditional downloads, private iCalendar feeds, and cached calendar MCP queries.
- Shared CLI/MCP status and updates support Moodle-only or combined setups; course failures remain isolated.
- Synthetic tests cover credential boundaries, expired sessions, content changes, recovery, and calendar failure. Live Moodle acceptance remains pending.

## 0.3.0

Initial public release: configurable Ed sync, interactive setup, offline validation/status, privacy defaults, MCP tools, Windows daily scheduling, and cross-platform release archives.
