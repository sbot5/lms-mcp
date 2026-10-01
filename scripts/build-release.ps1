#requires -Version 7.0
[CmdletBinding()]
param([string]$OutputDirectory = (Join-Path (Split-Path -Parent $PSScriptRoot) 'dist'))
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
if (Test-Path -LiteralPath $OutputDirectory) { throw 'Output directory exists; choose a fresh directory.' }
New-Item -ItemType Directory -Path $OutputDirectory | Out-Null
$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED
$archives = @()
Push-Location $root
try {
    $env:CGO_ENABLED = '0'
    foreach ($platform in @('windows-amd64','windows-arm64','linux-amd64','linux-arm64','darwin-amd64','darwin-arm64')) {
        $env:GOOS, $env:GOARCH = $platform.Split('-')
        $stage = Join-Path $OutputDirectory $platform
        New-Item -ItemType Directory -Path $stage | Out-Null
        $binary = if ($env:GOOS -eq 'windows') { 'lms-mcp.exe' } else { 'lms-mcp' }
        & go build -trimpath -buildvcs=false '-ldflags=-s -w' -o (Join-Path $stage $binary) ./cmd/lms-mcp
        if ($LASTEXITCODE -ne 0) { throw "Build failed for $platform" }
        foreach ($name in @('README.md','LICENSE','SECURITY.md','CONTRIBUTING.md')) { Copy-Item -LiteralPath (Join-Path $root $name) -Destination $stage }
        New-Item -ItemType Directory -Path (Join-Path $stage 'examples') | Out-Null
        foreach ($name in @('sync.example.json','.env.example','moodle.example.json','moodle.env.example')) { Copy-Item -LiteralPath (Join-Path $root "examples/$name") -Destination (Join-Path $stage 'examples') }
        New-Item -ItemType Directory -Path (Join-Path $stage 'docs') | Out-Null
        foreach ($name in @('moodle.md','architecture.md')) { Copy-Item -LiteralPath (Join-Path $root "docs/$name") -Destination (Join-Path $stage 'docs') }
        New-Item -ItemType Directory -Path (Join-Path $stage 'scripts') | Out-Null
        foreach ($name in @('install-task.ps1','run-sync.ps1')) { Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination (Join-Path $stage 'scripts') }
        if ($env:GOOS -eq 'windows') {
            $archive = Join-Path $OutputDirectory "lms-mcp-$platform.zip"
            [IO.Compression.ZipFile]::CreateFromDirectory($stage, $archive)
        } else {
            $archive = Join-Path $OutputDirectory "lms-mcp-$platform.tar.gz"
            # Unix users should chmod +x after extracting Windows-built archives.
            & tar -czf $archive -C $stage .
            if ($LASTEXITCODE -ne 0) { throw "Archive failed for $platform" }
        }
        $archives += $archive
    }
    $checksums = foreach ($archive in $archives) { '{0}  {1}' -f (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant(), [IO.Path]::GetFileName($archive) }
    [IO.File]::WriteAllLines((Join-Path $OutputDirectory 'checksums.txt'), $checksums, [Text.UTF8Encoding]::new($false))
} finally {
    $env:GOOS = $oldGOOS
    $env:GOARCH = $oldGOARCH
    $env:CGO_ENABLED = $oldCGO
    Pop-Location
}
Write-Output "Release archives: $OutputDirectory"
