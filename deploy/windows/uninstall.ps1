#Requires -Version 5.1
<#
.SYNOPSIS
    Remove the you-as-bee Windows client (yab).

.DESCRIPTION
    Removes the highest-privilege logon Scheduled Task and the installed
    program directory, stopping the running tray first so the locked yab.exe
    can be removed. The client config in %ProgramData%\you-as-bee is kept by
    default, matching `yab uninstall`; use -RemoveConfig to delete it too. This
    does not remove usbip-win2 or its driver.

    Windows PowerShell 5.1 compatible; ASCII only. Supports -WhatIf, which can
    be used without elevation (it performs no changes).

.PARAMETER RemoveConfig
    Also remove %ProgramData%\you-as-bee (client.json and its token).

.EXAMPLE
    .\uninstall.ps1
    .\uninstall.ps1 -RemoveConfig
    .\uninstall.ps1 -WhatIf
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [switch]$RemoveConfig
)

$ErrorActionPreference = 'Stop'

# Shared task helpers: Test-YabTaskExists falls back to schtasks when
# Get-ScheduledTask throws (see ScheduledTask.ps1).
. (Join-Path $PSScriptRoot 'ScheduledTask.ps1')

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
if (Test-YabTaskExists $taskName) {
    # Stop the running instance before deleting: its yab.exe locks the file we
    # are about to remove.
    if (-not $DryRun) {
        [void](Stop-YabTask $taskName)
        Get-Process -Name 'yab' -ErrorAction SilentlyContinue | ForEach-Object {
            Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
        }
    }
    if ($PSCmdlet.ShouldProcess($taskName, 'Delete Scheduled Task')) {
        if (-not (Remove-YabTask $taskName)) {
            Fail ("failed to delete the scheduled task '{0}'" -f $taskName)
        }
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
if (-not $RemoveConfig) {
    Write-Ok ("kept {0} (use -RemoveConfig to delete it)" -f $dataDir)
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
