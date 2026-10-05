#Requires -Version 5.1
<#
.SYNOPSIS
    Install the you-as-bee Windows client (yab) for a Raspberry Pi agent.

.DESCRIPTION
    Deploy-side installer for the machine that will connect over USB/IP.

    It does NOT install usbip-win2 or its driver; install that first (see
    docs/setup-windows.md). This script:

      * checks that usbip.exe (usbip-win2) can be found and reports its path;
      * optionally verifies the SHA-256 of a usbip-win2 release archive;
      * reports the current test-signing and Secure Boot state (it never
        changes either);
      * copies yab.exe to %ProgramFiles%\you-as-bee\;
      * writes %ProgramData%\you-as-bee\client.json from client.example.json;
      * restricts %ProgramData%\you-as-bee to Administrators and SYSTEM;
      * registers a highest-privilege logon Scheduled Task running "yab.exe tray".

    Windows PowerShell 5.1 compatible; ASCII only. Supports -WhatIf. -WhatIf can
    be used without elevation (it performs no changes).

.PARAMETER SourcePath
    Path to yab.exe. Defaults to ..\..\dist\yab-windows-amd64.exe.

.PARAMETER ConfigExample
    Path to client.example.json. Defaults to the copy next to this script.

.PARAMETER PiHost
    When set, replaces the first server's host in the written config.

.PARAMETER Token
    When set (64 lower-case hex characters), replaces the first server's token.

.PARAMETER UsbipArchive
    Optional path to a usbip-win2 release archive to verify.

.PARAMETER UsbipSha256
    Expected SHA-256 of UsbipArchive. Required when UsbipArchive is given.

.PARAMETER Force
    Overwrite an existing client.json and re-register the Scheduled Task.

.EXAMPLE
    .\install.ps1 -PiHost raspberrypi.local -Token 0123... -UsbipArchive .\usbip-win2.zip -UsbipSha256 ABCD...
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [string]$SourcePath,
    [string]$ConfigExample,
    [string]$PiHost,
    [string]$Token,
    [string]$UsbipArchive,
    [string]$UsbipSha256,
    [switch]$Force
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

function Write-Warn([string]$Message) {
    Write-Host ("  WARNING: {0}" -f $Message) -ForegroundColor Yellow
}

function Fail([string]$Message) {
    throw ("install: {0}" -f $Message)
}

# --- admin gate (skipped for a dry run, which changes nothing) ---------------

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
$isAdmin = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin -and -not $DryRun) {
    Fail 'must run from an elevated PowerShell prompt (or use -WhatIf to preview).'
}
if ($DryRun -and -not $isAdmin) {
    Write-Info 'not elevated: -WhatIf dry run, no changes will be made.'
}

if (-not $SourcePath) {
    $SourcePath = Join-Path $PSScriptRoot '..\..\dist\yab-windows-amd64.exe'
}
if (-not $ConfigExample) {
    $ConfigExample = Join-Path $PSScriptRoot 'client.example.json'
}

if (-not (Test-Path -LiteralPath $SourcePath)) {
    Fail ("yab.exe not found: {0} (build it with scripts\build.ps1)" -f $SourcePath)
}
$SourcePath = (Resolve-Path -LiteralPath $SourcePath).Path
if (-not (Test-Path -LiteralPath $ConfigExample)) {
    Fail ("config example not found: {0}" -f $ConfigExample)
}
$ConfigExample = (Resolve-Path -LiteralPath $ConfigExample).Path

if ($Token -and ($Token -notmatch '^[0-9a-f]{64}$')) {
    Fail 'Token must be exactly 64 lower-case hex characters.'
}

function Get-Sha256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash
}

function Find-UsbipExe {
    $candidates = @()
    if ($env:ProgramFiles) { $candidates += (Join-Path $env:ProgramFiles 'USBip\usbip.exe') }
    $x86 = ${env:ProgramFiles(x86)}
    if ($x86) { $candidates += (Join-Path $x86 'USBip\usbip.exe') }
    foreach ($c in $candidates) {
        if ($c -and (Test-Path -LiteralPath $c)) { return $c }
    }
    $cmd = Get-Command usbip.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return $null
}

function Get-TestSigning {
    try {
        $out = (& bcdedit /enum '{current}' 2>&1 | Out-String)
    } catch {
        return 'unknown'
    }
    foreach ($line in ($out -split "`r?`n")) {
        if ($line -match '(?i)testsigning') {
            if ($line -match '(?i)\byes\b') { return 'enabled' }
            if ($line -match '(?i)\bno\b') { return 'disabled' }
        }
    }
    return 'unknown'
}

function Get-SecureBootState {
    try {
        $v = Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\SecureBoot\State' `
            -Name UEFISecureBootEnabled -ErrorAction Stop
        if ($v.UEFISecureBootEnabled -eq 1) { return 'enabled' }
        return 'disabled'
    } catch {
        return 'unknown'
    }
}

# --- usbip-win2 --------------------------------------------------------------

Write-Step 'usbip-win2'

if ($UsbipArchive) {
    if (-not (Test-Path -LiteralPath $UsbipArchive)) {
        Fail ("usbip-win2 archive not found: {0}" -f $UsbipArchive)
    }
    $archiveHash = Get-Sha256 $UsbipArchive
    if (-not $UsbipSha256) {
        Write-Warn 'no -UsbipSha256 supplied; reporting the archive hash only.'
        Write-Info ("archive SHA-256: {0}" -f $archiveHash)
    } elseif ($archiveHash -ine $UsbipSha256) {
        Fail ("usbip-win2 archive SHA-256 mismatch: got {0}, want {1}" -f $archiveHash, $UsbipSha256)
    } else {
        Write-Ok ("archive SHA-256 verified: {0}" -f $archiveHash)
    }
}

$usbip = Find-UsbipExe
if ($usbip) {
    Write-Ok ("usbip.exe found: {0}" -f $usbip)
    try {
        $ver = (& $usbip version 2>&1 | Select-Object -First 1)
        if ($ver) { Write-Info ("usbip version output: {0}" -f ($ver.ToString().Trim())) }
    } catch {
        Write-Info 'usbip version output unavailable.'
    }
} else {
    Write-Warn 'usbip.exe not found. Install usbip-win2 and its driver first (docs/setup-windows.md).'
}

Write-Host ''
Write-Step 'driver-signing state (reported, never changed)'
$testSigning = Get-TestSigning
$secureBoot = Get-SecureBootState
Write-Info ("test signing: {0}" -f $testSigning)
Write-Info ("secure boot:  {0}" -f $secureBoot)
Write-Warn 'kernel anti-cheat refuses to run in test-signing mode; see docs/setup-windows.md.'

# --- binary ------------------------------------------------------------------

Write-Step 'binary'

$installDir = Join-Path $env:ProgramFiles 'you-as-bee'
$targetExe = Join-Path $installDir 'yab.exe'
$needCopy = $true
if (Test-Path -LiteralPath $targetExe) {
    if ((Get-Sha256 $targetExe) -eq (Get-Sha256 $SourcePath)) {
        $needCopy = $false
    }
}

if ($needCopy) {
    if (-not (Test-Path -LiteralPath $installDir)) {
        if ($PSCmdlet.ShouldProcess($installDir, 'Create directory')) {
            New-Item -ItemType Directory -Path $installDir -Force | Out-Null
        }
    }
    if ($PSCmdlet.ShouldProcess($targetExe, 'Copy yab.exe')) {
        Copy-Item -LiteralPath $SourcePath -Destination $targetExe -Force
    }
    Write-Ok ("installed {0}" -f $targetExe)
} else {
    Write-Ok 'yab.exe already up to date'
}

# --- config ------------------------------------------------------------------

Write-Step 'client config'

$dataDir = Join-Path $env:ProgramData 'you-as-bee'
$configPath = Join-Path $dataDir 'client.json'

if (-not (Test-Path -LiteralPath $dataDir)) {
    if ($PSCmdlet.ShouldProcess($dataDir, 'Create directory')) {
        New-Item -ItemType Directory -Path $dataDir -Force | Out-Null
    }
}

$writeConfig = (-not (Test-Path -LiteralPath $configPath)) -or $Force
if ($writeConfig) {
    if ($PSCmdlet.ShouldProcess($configPath, 'Write client.json')) {
        if ($PiHost -or $Token) {
            $json = Get-Content -Raw -LiteralPath $ConfigExample | ConvertFrom-Json
            if ($json.servers -and $json.servers.Count -gt 0) {
                if ($PiHost) { $json.servers[0].host = $PiHost }
                if ($Token) { $json.servers[0].token = $Token }
            }
            # ASCII avoids the UTF-8 BOM that breaks the strict JSON loader.
            ($json | ConvertTo-Json -Depth 10) | Set-Content -LiteralPath $configPath -Encoding ASCII
        } else {
            Copy-Item -LiteralPath $ConfigExample -Destination $configPath -Force
        }
    }
    Write-Ok ("wrote {0}" -f $configPath)
    if (-not $PiHost -or -not $Token) {
        Write-Warn 'edit host and token in client.json before use (see docs/configuration.md).'
    }
} else {
    Write-Ok 'client.json already present (use -Force to overwrite)'
}

if ($PSCmdlet.ShouldProcess($dataDir, 'Restrict ACL to Administrators and SYSTEM')) {
    & icacls $dataDir /inheritance:r /grant:r '*S-1-5-32-544:(OI)(CI)F' /grant:r '*S-1-5-18:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) { Fail ("icacls failed (exit {0})" -f $LASTEXITCODE) }
    Write-Ok 'ACL restricted to Administrators and SYSTEM'
}

# --- scheduled task ----------------------------------------------------------

Write-Step 'logon task'

$taskName = 'you-as-bee-client'
$taskCommand = ('"{0}" tray' -f $targetExe)
$existing = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
$register = $true
if ($existing -and -not $Force) {
    $register = $false
    Write-Ok 'task already registered (use -Force to re-register)'
}
if ($register) {
    if ($PSCmdlet.ShouldProcess($taskName, 'Register logon Scheduled Task')) {
        & schtasks /Create /TN $taskName /TR $taskCommand /SC ONLOGON /RL HIGHEST /F | Out-Null
        if ($LASTEXITCODE -ne 0) { Fail ("schtasks /Create failed (exit {0})" -f $LASTEXITCODE) }
    }
    Write-Ok ("registered task '{0}'" -f $taskName)
}

Write-Host ''
if ($DryRun) {
    Write-Host 'Dry run complete: no changes were made.' -ForegroundColor Cyan
} else {
    Write-Host 'Install complete.' -ForegroundColor Green
    Write-Host 'Next: run "yab doctor" and attach a device, or log off and on again for the tray task.'
}
