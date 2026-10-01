# Changelog

## Unreleased

- Reorganize executable entry, application orchestration, provider clients, rendering and file locks into `cmd/` and `internal/` packages; move templates to `examples/`.
- Add common build/check scripts, documented package boundaries, and release/CI checks for the new layout. Command arguments and saved configuration/checkpoint formats are unchanged.

- Moodle preview: token/Cookie material sync, conditional downloads, private iCalendar feeds, and cached calendar MCP queries.
- Shared CLI/MCP status and updates support Moodle-only or combined setups; course failures remain isolated.
- Synthetic tests cover credential boundaries, expired sessions, content changes, recovery, and calendar failure. Live Moodle acceptance remains pending.

## 0.3.0

Initial public release: configurable Ed sync, interactive setup, offline validation/status, privacy defaults, MCP tools, Windows daily scheduling, and cross-platform release archives.
