#Requires -Version 5.1
<#
.SYNOPSIS
    Deploy the you-as-bee agent to a Raspberry Pi over SSH.

.DESCRIPTION
    Copies the cross-compiled yabd binary and the deploy/pi support files to the
    Pi with scp, then runs deploy/pi/provision.sh over ssh with sudo.

    Authentication is whatever your ssh client already uses (keys or its own
    prompt). No credentials are embedded or passed on the command line.

    Windows PowerShell 5.1 compatible; ASCII only.

.PARAMETER HostName
    Pi host name or IP address.

.PARAMETER User
    SSH user. Defaults to the current Windows user name.

.PARAMETER Arch
    arm64 (Pi 4B, default) or armv7 (32-bit Raspberry Pi OS).

.PARAMETER SshPort
    SSH port. Defaults to 22.

.PARAMETER IdentityFile
    Optional private key for ssh/scp.

.PARAMETER RemoteDir
    Staging directory on the Pi. Defaults to /tmp/you-as-bee-deploy.

.PARAMETER ClientCidr
    Optional CIDR allowlist passed to provision.sh (comma-separated).

.EXAMPLE
    .\deploy-pi.ps1 -HostName raspberrypi.local -ClientCidr 192.168.1.0/24
    .\deploy-pi.ps1 -HostName 10.0.0.5 -User pi -Arch armv7 -IdentityFile $HOME\.ssh\id_ed25519
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [Alias('Host')]
    [string]$HostName,

    [string]$User = $env:USERNAME,

    [ValidateSet('arm64', 'armv7')]
    [string]$Arch = 'arm64',

    [int]$SshPort = 22,

    [string]$IdentityFile,

    [string]$RemoteDir = '/tmp/you-as-bee-deploy',

    [string]$ClientCidr
)

$ErrorActionPreference = 'Stop'

function Write-Step([string]$Message) {
    Write-Host ("== {0} ==" -f $Message)
}

function Write-Ok([string]$Message) {
    Write-Host ("  {0}" -f $Message) -ForegroundColor Green
}

function Fail([string]$Message) {
    throw ("deploy-pi: {0}" -f $Message)
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$piDir = Join-Path $repoRoot 'deploy\pi'

switch ($Arch) {
    'arm64' { $binaryName = 'yabd-linux-arm64' }
    'armv7' { $binaryName = 'yabd-linux-armv7' }
}
$binaryPath = Join-Path $repoRoot ("dist\{0}" -f $binaryName)

if (-not (Get-Command ssh -ErrorAction SilentlyContinue)) { Fail 'ssh was not found on PATH.' }
if (-not (Get-Command scp -ErrorAction SilentlyContinue)) { Fail 'scp was not found on PATH.' }
if (-not (Test-Path -LiteralPath $binaryPath)) {
    Fail ("{0} not found; run scripts\build.ps1 first." -f $binaryPath)
}

$files = @(
    'provision.sh',
    'uninstall.sh',
    'agent.example.json',
    'yabd.service',
    'usbipd.service',
    'yab-modprobe.conf',
    '90-you-as-bee.rules'
)
foreach ($f in $files) {
    if (-not (Test-Path -LiteralPath (Join-Path $piDir $f))) {
        Fail ("missing deploy file: {0}" -f (Join-Path $piDir $f))
    }
}

$target = ("{0}@{1}" -f $User, $HostName)
$sshArgs = @()
$scpArgs = @()
if ($SshPort -ne 22) {
    $sshArgs += @('-p', "$SshPort")
    $scpArgs += @('-P', "$SshPort")
}
if ($IdentityFile) {
    $sshArgs += @('-i', $IdentityFile)
    $scpArgs += @('-i', $IdentityFile)
}

Write-Step 'staging directory'
& ssh @sshArgs $target ("mkdir -p {0}" -f $RemoteDir)
if ($LASTEXITCODE -ne 0) { Fail ("ssh mkdir failed (exit {0})" -f $LASTEXITCODE) }
Write-Ok $RemoteDir

Write-Step 'copying files'
$toCopy = @()
foreach ($f in $files) { $toCopy += (Join-Path $piDir $f) }
$toCopy += $binaryPath
& scp @scpArgs $toCopy ("{0}:{1}/" -f $target, $RemoteDir)
if ($LASTEXITCODE -ne 0) { Fail ("scp failed (exit {0})" -f $LASTEXITCODE) }
Write-Ok ("copied {0} files + {1}" -f $files.Count, $binaryName)

Write-Step 'provisioning'
$remoteBin = ("{0}/{1}" -f $RemoteDir, $binaryName)
$remoteProvision = ("{0}/provision.sh" -f $RemoteDir)
$args1 = ("--binary '{0}'" -f $remoteBin)
if ($ClientCidr) { $args1 += (" --client-cidr '{0}'" -f $ClientCidr) }
$remoteCmd = ("sudo bash '{0}' {1}" -f $remoteProvision, $args1)
& ssh @sshArgs $target $remoteCmd
if ($LASTEXITCODE -ne 0) { Fail ("provision.sh failed (exit {0})" -f $LASTEXITCODE) }

Write-Host ''
Write-Host 'Deploy complete. Copy the printed bearer token into the Windows client config.' -ForegroundColor Green
