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
    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed.' }
    $binary = & (Join-Path $PSScriptRoot 'build.ps1')
    & $binary version
    if ($LASTEXITCODE -ne 0) { throw 'Version smoke test failed.' }
    foreach ($example in @('sync.example.json','moodle.example.json')) {
        & $binary config validate -config (Join-Path $root "examples/$example")
        if ($LASTEXITCODE -ne 0) { throw "Config smoke test failed: $example" }
    }
} finally { Pop-Location }
