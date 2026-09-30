#requires -Version 7.0
[CmdletBinding()]
param([string]$OutputPath)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $OutputPath) {
    $name = if ($IsWindows) { 'lms-mcp.exe' } else { 'lms-mcp' }
    $OutputPath = Join-Path $root "bin/$name"
} else {
    $OutputPath = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputPath)
}
[IO.Directory]::CreateDirectory((Split-Path -Parent $OutputPath)) | Out-Null
Push-Location $root
try {
    & go build -trimpath -buildvcs=false -o $OutputPath ./cmd/lms-mcp
    if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
} finally { Pop-Location }
Write-Output $OutputPath
