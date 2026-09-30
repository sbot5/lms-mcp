# Contributing

Use Go 1.25 or newer. PowerShell 7 runs the cross-platform helper scripts; it is also required for Windows scheduling. All project Go code is under `cmd/` and `internal/`.

## Build and verify

```powershell
pwsh -File scripts/build.ps1
pwsh -File scripts/check.ps1
```

The build goes to `bin/lms-mcp.exe` on Windows or `bin/lms-mcp` elsewhere. `check.ps1` checks gofmt, runs tests and vet, builds the actual executable, and validates both sample configurations without credentials or network requests to learning platforms.

Without PowerShell:

```sh
gofmt -w cmd internal
go test ./...
go vet ./...
go build -trimpath -buildvcs=false -o bin/lms-mcp ./cmd/lms-mcp
```

CI performs the same checks on Windows, Linux and macOS with Go 1.25.x and stable. Keep tests beside the code they exercise. Integration tests currently live in `internal/app` because they cover provider calls, export policy and filesystem transactions together. Use synthetic fixtures and temporary directories; do not check in credentials, authentic course exports or private filesystem paths.

## Package boundaries

Read [the architecture guide](docs/architecture.md) before adding another package. Put protocol-only logic in the relevant provider. Keep configuration, privacy decisions, output paths and checkpoint commit ordering in `internal/app`. Pure conversion belongs in `internal/render`; platform lock implementations belong in `internal/filelock`. Avoid reverse imports and broad interfaces that have no second use.

When moving symbols or files, preserve CLI flags and saved JSON formats unless the change explicitly includes a migration. Update README commands, examples, scripts and workflows together. Use gofmt; `.editorconfig` describes encoding and indentation for other files.

## Releases

1. Run `scripts/check.ps1` and complete any provider-specific live acceptance still required.
2. Update `internal/app/version.go`, `CHANGELOG.md`, and `docs/releases/<tag>.md`.
3. Run `scripts/build-release.ps1 -OutputDirectory <fresh-directory>` to check six OS/CPU archives and `checksums.txt`.
4. Inspect the archive whitelist: binary, public documentation, synthetic examples and scheduling scripts only.
5. After the change is reviewed and CI passes, tag the intended commit. The release workflow reads notes from `docs/releases/<tag>.md` and rebuilds the archives.

No release is created by ordinary builds, tests or a branch push. Existing running instances must be upgraded separately. Report security issues through the repository's private vulnerability reporting feature; see [SECURITY.md](SECURITY.md).
