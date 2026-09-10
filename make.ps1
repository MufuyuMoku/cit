#!/usr/bin/env pwsh
# CIT task runner for Windows, where GNU make is usually absent.
# Mirrors the Makefile exactly.
#
#   .\make.ps1 dev     - jalankan aplikasi dengan hot reload
#   .\make.ps1 test    - go vet + go test (./internal/... dan ./cmd/...)
#   .\make.ps1 build   - bangun binari untuk platform saat ini (build/bin/)
#   .\make.ps1 clean   - hapus keluaran build

[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('help', 'dev', 'test', 'build', 'clean')]
    [string]$Task = 'help'
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$Module = 'github.com/MufuyuMoku/cit'

function Get-GitValue {
    param([string[]]$GitArgs, [string]$Fallback)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $value = & git @GitArgs 2>$null
        if ($LASTEXITCODE -eq 0 -and $value) { return ([string]$value).Trim() }
    }
    catch { }
    finally { $ErrorActionPreference = $previous }
    return $Fallback
}

function Invoke-Step {
    param([string]$Exe, [string[]]$Arguments)
    Write-Host ">> $Exe $($Arguments -join ' ')" -ForegroundColor DarkGray
    & $Exe @Arguments
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

$version = Get-GitValue -GitArgs @('describe', '--tags', '--always', '--dirty') -Fallback '0.0.0-dev'
$commit = Get-GitValue -GitArgs @('rev-parse', 'HEAD') -Fallback 'unknown'
$date = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')

$ldflags = "-X $Module/cmd.Version=$version -X $Module/cmd.Commit=$commit -X $Module/cmd.BuildDate=$date"

# Non-negotiable: a cgo build breaks cross-compilation in CI.
$env:CGO_ENABLED = '0'
$env:GOPROXY = 'https://proxy.golang.org,direct'

switch ($Task) {
    'dev' {
        Invoke-Step 'wails' @('dev', '-ldflags', $ldflags)
    }
    'test' {
        Invoke-Step 'go' @('vet', './internal/...')
        Invoke-Step 'go' @('test', '-count=1', './internal/...')
        # The application layer too. On Windows Wails needs no cgo, so ./cmd can
        # be tested here under the same CGO_ENABLED=0 rule as everything else --
        # which is why this lives in make.ps1 and not in the Makefile.
        Invoke-Step 'go' @('vet', './cmd/...')
        Invoke-Step 'go' @('test', '-count=1', './cmd/...')
    }
    'build' {
        Invoke-Step 'wails' @('build', '-clean', '-trimpath', '-ldflags', $ldflags)
    }
    'clean' {
        foreach ($path in @('build/bin', 'frontend/.svelte-kit')) {
            if (Test-Path $path) { Remove-Item -Recurse -Force -Confirm:$false $path }
        }
        Get-ChildItem -Path 'frontend/build' -Force -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -ne '.gitkeep' } |
            Remove-Item -Recurse -Force -Confirm:$false
        Write-Host 'clean: selesai.'
    }
    default {
        Write-Host 'dev    - jalankan aplikasi dengan hot reload'
        Write-Host 'test   - go vet + go test (./internal/... dan ./cmd/...)'
        Write-Host 'build  - bangun binari untuk platform saat ini (build/bin/)'
        Write-Host 'clean  - hapus keluaran build'
    }
}
