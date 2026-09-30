# Architecture

The repository builds one executable. Internal packages separate network protocols and pure transformations from application policy without introducing a public library API.

```text
cmd/lms-mcp
    └── internal/app
          ├── internal/ed
          ├── internal/moodle
          ├── internal/render
          └── internal/filelock
```

Providers never import `app` or each other. `render` and `filelock` do not import providers. Shared configuration, export records, checkpoint schemas and commit ordering remain in `app`, because they describe the local application rather than a remote service.

| Package | Responsibility |
| --- | --- |
| `cmd/lms-mcp` | OS signals, standard streams and process exit status. Calls `app.Run`. |
| `internal/app` | Parse arguments/configuration, load private credential files, wire clients, implement MCP handlers, select content, render files, query status and commit checkpoints. |
| `internal/ed` | Ed HTTP authentication, retries, pagination and response models. Accepts a token and an optional HTTP transport; no filesystem access. |
| `internal/moodle` | Site-scoped HTTP requests, token/Cookie handling, supported HTML discovery and iCalendar parsing. Accepts already-loaded credentials; no configuration-file access. |
| `internal/render` | Convert Ed XML into Markdown, redact mention tags, escape Markdown and collect asset URLs. Network allowlisting stays in `app`. |
| `internal/filelock` | Acquire platform-specific process locks; callers own and close the returned file. |

## Application organization

- `cli.go`, `ed_commands.go`, `setup.go`, `status.go`: command dispatch, terminal output, setup and local queries.
- `config.go`, `moodle_config.go`, `env.go`, `clients.go`: configuration normalization, credentials and provider construction.
- `mcp.go`, `version.go`: MCP registration and the shared application version.
- `sync.go`, `ed_lessons.go`, `ed_records.go`, `moodle_sync.go`, `moodle_calendar.go`: provider orchestration, exported content and calendar cache queries.
- `storage.go`: hashes, safe output paths, atomic writes, backups, local-edit conflicts and the durable recovery journal.
- `*_test.go`: synthetic HTTP/filesystem/MCP integration tests alongside the application that coordinates them. Test servers use loopback and temporary directories; no live credentials are required.

## Synchronization flow

1. Load and validate configuration. Resolve relative paths against its location.
2. Construct clients from private credentials and lock one course's output folder.
3. Read the successful checkpoint, fetch changed data and prepare complete output.
4. Check local edits, save a recovery journal, back up replaced files and atomically replace each output.
5. Commit the new checkpoint only after that course's outputs succeed. Report individual failures and continue other courses.

Moving files into Go packages does not change command names, JSON field names, output ownership or checkpoint formats. Existing private configurations do not need migration. The root module is no longer an executable target: build `./cmd/lms-mcp`, or use `scripts/build.ps1`.

## Repository and runtime data

`examples/` contains synthetic templates only. `docs/` contains guides and versioned release notes. `scripts/` owns build, verification, packaging and optional Windows scheduling. `bin/` and `dist/` are ignored generated output. Tokens, user configuration, downloaded material, checkpoints, logs and backups belong outside the repository.

Source builds go into `bin/`. Release archives keep the binary at the archive root and retain `examples/`, `docs/` and the two scheduling scripts. The task installer handles either layout. Development work does not replace an existing installed instance automatically.

Moodle remains a development preview pending live-site acceptance; package separation and offline CI do not establish site coverage. See [Moodle validation](moodle.md).
