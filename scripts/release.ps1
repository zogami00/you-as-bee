#Requires -Version 5.1
<#
.SYNOPSIS
    Cut a GitHub release with the you-as-bee build and deploy artefacts.

.DESCRIPTION
    Runs `gh release create` for a tag and attaches the cross-built binaries
    (dist/) plus the deploy files, so the curl bootstrap can fetch them:

        deploy/pi/install.sh  curl -fsSL <url> | sudo bash -s -- --release <tag>

    It prints the resulting asset URLs and a ready-to-paste curl command. The
    repository is public, so the printed command needs no token; a private fork
    would add the token flow.

    This is a manual helper: it is never invoked by CI or by any other script.
    Run it yourself after `scripts/build.ps1` and after committing the release
    notes.

    Windows PowerShell 5.1 compatible; ASCII only. Supports -WhatIf (it then
    prints the command and the URLs without touching GitHub).

.PARAMETER Tag
    Release tag, for example v1.0.0.

.PARAMETER Title
    Release title. Defaults to the tag.

.PARAMETER Notes
    Release notes text. Defaults to a one-line summary.

.PARAMETER Repo
    owner/repo slug. Defaults to zogami00/you-as-bee.

.EXAMPLE
    .\release.ps1 -Tag v1.0.0
    .\release.ps1 -Tag v1.0.0 -WhatIf
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [Parameter(Mandatory = $true)]
    [string]$Tag,

    [string]$Title,
    [string]$Notes,
    [string]$Repo = 'zogami00/you-as-bee'
)

$ErrorActionPreference = 'Stop'

function Write-Step([string]$Message) {
    Write-Host ("== {0} ==" -f $Message)
}

function Write-Ok([string]$Message) {
    Write-Host ("  {0}" -f $Message) -ForegroundColor Green
}

function Write-Info([string]$Message) {
    Write-Host ("  {0}" -f $Message)
}

function Write-Warn([string]$Message) {
    Write-Host ("  WARNING: {0}" -f $Message) -ForegroundColor Yellow
}

function Fail([string]$Message) {
    throw ("release: {0}" -f $Message)
}

$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot
try {
    if (-not $Title) { $Title = $Tag }
    if (-not $Notes) { $Notes = ("you-as-bee {0}" -f $Tag) }

    $assetPaths = @(
        'dist\yabd-linux-arm64',
        'dist\yabd-linux-armv7',
        'dist\yab-windows-amd64.exe',
        'dist\yabw-windows-amd64.exe',
        'deploy\pi\install.sh',
        'deploy\pi\provision.sh',
        'deploy\pi\uninstall.sh',
        'deploy\pi\agent.example.json',
        'deploy\pi\yabd.service',
        'deploy\pi\usbipd.service',
        'deploy\pi\yab-modprobe.conf',
        'deploy\pi\90-you-as-bee.rules',
        'deploy\windows\install.ps1',
        'deploy\windows\uninstall.ps1',
        'deploy\windows\ScheduledTask.ps1',
        'deploy\windows\ScheduledTask.Tests.ps1',
        'deploy\windows\client.example.json'
    )

    Write-Step 'assets'
    foreach ($p in $assetPaths) {
        if (-not (Test-Path -LiteralPath $p)) {
            Fail ("missing asset: {0} (run scripts\build.ps1 first?)" -f $p)
        }
        Write-Info $p
    }

    $assetUrls = @()
    foreach ($p in $assetPaths) {
        $leaf = Split-Path -Leaf $p
        $assetUrls += ("https://github.com/{0}/releases/download/{1}/{2}" -f $Repo, $Tag, $leaf)
    }

    Write-Step 'release'
    $ghArgs = @('release', 'create', $Tag, '--repo', $Repo, '--title', $Title, '--notes', $Notes)
    $ghArgs += ($assetPaths -replace '\\', '/')

    if ($PSCmdlet.ShouldProcess($Tag, ("Create release on {0}" -f $Repo))) {
        if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
            Fail 'gh (GitHub CLI) was not found on PATH.'
        }
        & gh @ghArgs
        if ($LASTEXITCODE -ne 0) { Fail ("gh release create failed (exit {0})" -f $LASTEXITCODE) }
        Write-Ok ("created release {0}" -f $Tag)
    } else {
        Write-Info ("would run: gh {0}" -f ($ghArgs -join ' '))
        if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
            Write-Warn 'gh (GitHub CLI) was not found on PATH.'
        }
    }

    Write-Host ''
    Write-Step 'asset URLs'
    foreach ($u in $assetUrls) { Write-Host ("  {0}" -f $u) }

    $installUrl = ("https://github.com/{0}/releases/download/{1}/install.sh" -f $Repo, $Tag)
    Write-Host ''
    Write-Step 'Pi one-liner'
    Write-Host ("  curl -fsSL {0} | sudo bash -s -- --release {1}" -f $installUrl, $Tag)
    Write-Host '  # Private fork only: export GITHUB_TOKEN (repo scope) and use "sudo -E bash -s --"'
    Write-Host '  # instead, so the token stays off the command line.'
    Write-Host ''
    Write-Ok 'release helper complete'
}
finally {
    Pop-Location
}
