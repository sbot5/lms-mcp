#requires -Version 7.0
[CmdletBinding()]
param(
    [string]$EnvFile,
    [Parameter(Mandatory)][string]$Config,
    [string]$Executable,
    [string]$TaskName,
    [string]$At,
    [switch]$Remove
)
$ErrorActionPreference = 'Stop'
$Config = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Config)
$explicitEnvFile = [bool]$EnvFile
if (-not $TaskName) {
    $hash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($Config.ToLowerInvariant())))
    $TaskName = 'LMS-MCP-Ed-' + $hash.Substring(0, 10)
}
$description = 'lms-mcp: read Ed daily, preserve course materials, write per-course updates. Managed by lms-mcp install-task.ps1.'
$existing = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($existing -and $existing.Description -ne $description) { throw "Task $TaskName exists and is not managed by this project." }
if ($Remove) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
    return
}
if (-not $Executable) {
    $projectRoot = Split-Path -Parent $PSScriptRoot
    $builtBinary = Join-Path $projectRoot 'bin/lms-mcp.exe'
    $Executable = if (Test-Path -LiteralPath $builtBinary) { $builtBinary } else { Join-Path $projectRoot 'lms-mcp.exe' }
}
$Executable = (Resolve-Path -LiteralPath $Executable).ProviderPath
$Config = (Resolve-Path -LiteralPath $Config).ProviderPath
& $Executable config validate -config $Config | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Configuration validation failed.' }
$settingsFile = Get-Content -LiteralPath $Config -Raw | ConvertFrom-Json
if (-not $At) { $At = $settingsFile.schedule.daily_at; if (-not $At) { $At = '09:00' } }
$clock = [datetime]::ParseExact($At, 'HH:mm', [Globalization.CultureInfo]::InvariantCulture)
if (-not $EnvFile) {
    $EnvFile = $settingsFile.env_file
    if ($settingsFile.courses.Count -gt 0 -and -not $EnvFile) { throw 'Set env_file for Ed or pass -EnvFile.' }
    if ($EnvFile -and -not [IO.Path]::IsPathRooted($EnvFile)) { $EnvFile = Join-Path (Split-Path -Parent $Config) $EnvFile }
}
if ($EnvFile) { $EnvFile = (Resolve-Path -LiteralPath $EnvFile).ProviderPath }
if ($settingsFile.moodle) {
    $moodleEnv = $settingsFile.moodle.env_file
    if (-not [IO.Path]::IsPathRooted($moodleEnv)) { $moodleEnv = Join-Path (Split-Path -Parent $Config) $moodleEnv }
    $null = Resolve-Path -LiteralPath $moodleEnv
}
$runner = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot 'run-sync.ps1')).ProviderPath
$pwshPath = (Get-Command pwsh -CommandType Application | Select-Object -First 1).Source
$logDir = Join-Path (Split-Path -Parent $Config) '.lms-sync-logs'
foreach ($value in @($Executable, $EnvFile, $Config, $runner, $logDir)) {
    if ($value -and $value.Contains('"')) { throw 'Task paths cannot contain double quotes.' }
}
$arguments = '-NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File "{0}" -Executable "{1}" -Config "{2}" -LogDirectory "{3}"' -f $runner, $Executable, $Config, $logDir
if ($explicitEnvFile) { $arguments += ' -EnvFile "{0}"' -f $EnvFile }
$action = New-ScheduledTaskAction -Execute $pwshPath -Argument $arguments -WorkingDirectory (Split-Path -Parent $Executable)
$daily = New-ScheduledTaskTrigger -Daily -At $clock
$user = [Security.Principal.WindowsIdentity]::GetCurrent().Name
# InteractiveToken needs no stored password; catches missed runs after the user logs in.
$logon = New-ScheduledTaskTrigger -AtLogOn -User $user
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Hours 2) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 15) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger @($daily, $logon) -Settings $settings -Principal $principal -Description $description -Force | Select-Object TaskName, State
Get-ScheduledTaskInfo -TaskName $TaskName | Select-Object NextRunTime, LastTaskResult
