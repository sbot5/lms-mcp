#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Executable,
    [string]$EnvFile,
    [Parameter(Mandatory)][string]$Config,
    [Parameter(Mandatory)][string]$LogDirectory
)
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
[IO.Directory]::CreateDirectory($LogDirectory) | Out-Null
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss-fff'
$logPath = Join-Path $LogDirectory "$stamp.log"
try {
    $configHash = (Get-FileHash -LiteralPath $Config -Algorithm SHA256).Hash
    $latestPath = Join-Path $LogDirectory 'latest.json'
    if (Test-Path -LiteralPath $latestPath) {
        try {
            $previous = Get-Content -LiteralPath $latestPath -Raw | ConvertFrom-Json
            $lastRun = [DateTimeOffset]::Parse($previous.finished_at)
            if ($previous.exit_code -eq 0 -and $previous.config_hash -eq $configHash -and $lastRun.LocalDateTime.Date -eq (Get-Date).Date) { exit 0 }
        } catch { # A missing/corrupt status is repaired by this run.
        }
    }
    $syncArgs = @('sync', '-config', $Config, '-full')
    if ($EnvFile) { $syncArgs += @('-env-file', $EnvFile) }
    & $Executable @syncArgs *> $logPath
    $resultCode = $LASTEXITCODE
    if ($null -eq $resultCode) { $resultCode = 1 }
    $status = [ordered]@{
        finished_at = [DateTimeOffset]::Now.ToString('o')
        exit_code = $resultCode
        log = $logPath
        config_hash = $configHash
    }
    $tmp = Join-Path $LogDirectory "latest-$stamp.tmp"
    $status | ConvertTo-Json | Set-Content -LiteralPath $tmp -Encoding utf8
    Move-Item -LiteralPath $tmp -Destination (Join-Path $LogDirectory 'latest.json') -Force
    exit $resultCode
} catch {
    $_.Exception.Message | Add-Content -LiteralPath $logPath -Encoding utf8
    [ordered]@{finished_at=[DateTimeOffset]::Now.ToString('o');exit_code=1;log=$logPath} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $LogDirectory 'latest.json') -Encoding utf8
    exit 1
}
