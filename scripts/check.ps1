#requires -Version 7.0
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $unformatted = @(& gofmt -l cmd internal)
    if ($LASTEXITCODE -ne 0) { throw 'gofmt check failed.' }
    if ($unformatted.Count) { throw ('Run gofmt on: ' + ($unformatted -join ', ')) }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed.' }
    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    $bin = if ($IsWindows) { 'bin/lms-mcp.exe' } else { 'bin/lms-mcp' }
    & go build -trimpath -o $bin ./cmd/lms-mcp
    if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
    & $bin version
    if ($LASTEXITCODE -ne 0) { throw 'Version smoke test failed.' }
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
    & go build -trimpath -o (Join-Path ([System.IO.Path]::GetTempPath()) 'lms-mcp-crosscheck.exe') ./cmd/lms-mcp
    Remove-Item Env:GOOS, Env:GOARCH
    if ($LASTEXITCODE -ne 0) { throw 'Windows cross-build failed.' }
} finally { Pop-Location }
