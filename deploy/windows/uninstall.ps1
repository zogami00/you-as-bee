#Requires -Version 5.1
<#
.SYNOPSIS
    Remove the you-as-bee Windows client (yab).

.DESCRIPTION
    Removes the highest-privilege logon Scheduled Task and the installed
    program directory. The client config in %ProgramData%\you-as-bee is kept
    unless -KeepConfig is not specified. This does not remove usbip-win2 or its
    driver.

    Windows PowerShell 5.1 compatible; ASCII only. Supports -WhatIf, which can
    be used without elevation (it performs no changes).

.PARAMETER KeepConfig
    Keep %ProgramData%\you-as-bee\client.json.

.EXAMPLE
    .\uninstall.ps1
    .\uninstall.ps1 -KeepConfig
    .\uninstall.ps1 -WhatIf
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [switch]$KeepConfig
)

$ErrorActionPreference = 'Stop'

$DryRun = [bool]$WhatIfPreference

function Write-Step([string]$Message) {
    Write-Host ("== {0} ==" -f $Message)
}

function Write-Ok([string]$Message) {
    Write-Host ("  {0}" -f $Message) -ForegroundColor Green
}

function Write-Info([string]$Message) {
    Write-Host ("  {0}" -f $Message)
}

function Fail([string]$Message) {
    throw ("uninstall: {0}" -f $Message)
}

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
$isAdmin = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin -and -not $DryRun) {
    Fail 'must run from an elevated PowerShell prompt (or use -WhatIf to preview).'
}
if ($DryRun -and -not $isAdmin) {
    Write-Info 'not elevated: -WhatIf dry run, no changes will be made.'
}

# --- scheduled task ----------------------------------------------------------

Write-Step 'logon task'

$taskName = 'you-as-bee-client'
$existing = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($existing) {
    if ($PSCmdlet.ShouldProcess($taskName, 'Delete Scheduled Task')) {
        & schtasks /Delete /TN $taskName /F | Out-Null
        if ($LASTEXITCODE -ne 0) { Fail ("schtasks /Delete failed (exit {0})" -f $LASTEXITCODE) }
    }
    Write-Ok 'task removed'
} else {
    Write-Info 'task not present'
}

# --- program directory -------------------------------------------------------

Write-Step 'program directory'

$installDir = Join-Path $env:ProgramFiles 'you-as-bee'
if (Test-Path -LiteralPath $installDir) {
    if ($PSCmdlet.ShouldProcess($installDir, 'Remove directory')) {
        Remove-Item -LiteralPath $installDir -Recurse -Force
    }
    Write-Ok 'removed program directory'
} else {
    Write-Info 'program directory not present'
}

# --- config ------------------------------------------------------------------

Write-Step 'client config'

$dataDir = Join-Path $env:ProgramData 'you-as-bee'
if ($KeepConfig) {
    Write-Ok ("kept {0} (-KeepConfig)" -f $dataDir)
} elseif (Test-Path -LiteralPath $dataDir) {
    if ($PSCmdlet.ShouldProcess($dataDir, 'Remove directory')) {
        Remove-Item -LiteralPath $dataDir -Recurse -Force
    }
    Write-Ok 'removed config directory'
} else {
    Write-Info 'config directory not present'
}

Write-Host ''
if ($DryRun) {
    Write-Host 'Dry run complete: no changes were made.' -ForegroundColor Cyan
} else {
    Write-Host 'Uninstall complete.' -ForegroundColor Green
    Write-Host 'usbip-win2 and its driver were not touched.'
}
