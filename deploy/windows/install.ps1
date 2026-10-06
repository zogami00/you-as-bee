#Requires -Version 5.1
<#
.SYNOPSIS
    Install the you-as-bee Windows client (yab/yabw) for a Raspberry Pi agent.

.DESCRIPTION
    Deploy-side installer for the machine that will connect over USB/IP.

    It does NOT install usbip-win2 or its driver; install that first (see
    docs/setup-windows.md). This script:

      * checks that usbip.exe (usbip-win2) can be found and reports its path;
      * optionally verifies the SHA-256 of a usbip-win2 release archive;
      * reports the current test-signing and Secure Boot state (it never
        changes either);
      * copies yab.exe (CLI) and yabw.exe (windowless tray) to
        %ProgramFiles%\you-as-bee\;
      * writes %ProgramData%\you-as-bee\client.json from client.example.json;
      * restricts %ProgramData%\you-as-bee to Administrators and SYSTEM;
      * registers a highest-privilege logon Scheduled Task running "yabw.exe
        tray" (the GUI-subsystem build, so logon opens no console window for
        the tray; console children such as usbip.exe are started hidden).

    The binaries come from one of four places, in this order of preference:

      1. a local bundle: -BundleDir, or the script's own directory when it
         already contains yab.exe and yabw.exe (the default for a packaged
         install);
      2. a GitHub release: -Release <tag> (or 'latest'). The public repository
         needs no token: assets come from the plain browser_download_url. For a
         private fork, set GITHUB_TOKEN (preferred; it stays off the command
         line) or pass -BundleToken, which switches to the authenticated API
         path;
      3. a generic download: -BundleUrl (https:// only; http:// is allowed just
         for 127.0.0.1/localhost testing) with an optional -BundleToken bearer;
      4. the legacy checkout layout: -SourcePath, default
         ..\..\dist\yab-windows-amd64.exe, with yabw-windows-amd64.exe beside
         it.

    Windows PowerShell 5.1 compatible; ASCII only. Supports -WhatIf in every
    mode. A -WhatIf run still downloads a -BundleUrl/-Release to a temporary
    directory so the URL and the file headers can be validated; it changes
    nothing on the system.

.PARAMETER SourcePath
    Path to yab.exe (legacy checkout mode). Defaults to
    ..\..\dist\yab-windows-amd64.exe. yabw-windows-amd64.exe is expected beside
    it.

.PARAMETER ConfigExample
    Path to client.example.json. Defaults to the copy next to this script, or
    inside the bundle.

.PARAMETER BundleDir
    Directory containing yab.exe, yabw.exe and (optionally) client.example.json.

.PARAMETER BundleUrl
    Base URL of a bundle; the installer fetches <url>/yab.exe, <url>/yabw.exe
    and <url>/client.example.json. Must be https://; http:// is allowed only
    for 127.0.0.1 or localhost testing.

.PARAMETER Release
    GitHub release tag to install from, or 'latest' to resolve the newest
    stable release. The public repository needs no token: assets come from the
    plain browser_download_url. For a private fork, set GITHUB_TOKEN (preferred)
    or -BundleToken, which switches to the authenticated API path.

.PARAMETER Repo
    owner/repo slug used by -Release. Defaults to zogami00/you-as-bee.

.PARAMETER BundleToken
    Optional bearer token. For -BundleUrl it is sent as "Authorization: Bearer
    <token>" on every download. For -Release it selects the authenticated API
    path for a PRIVATE FORK; this repository is public and needs no token. Prefer
    the GITHUB_TOKEN environment variable (honoured only with -Release) because a
    command-line token is visible in the process list.

.PARAMETER PiHost
    When set, replaces the first server's host in the written config.

.PARAMETER Token
    When set (64 lower-case hex characters), replaces the first server's token.

.PARAMETER UsbipArchive
    Optional path to a usbip-win2 release archive to verify.

.PARAMETER UsbipSha256
    Expected SHA-256 of UsbipArchive. Required when UsbipArchive is given.

.PARAMETER Force
    Overwrite an existing client.json and re-register the Scheduled Task. Also
    use this to migrate an existing install whose task still runs yab.exe.

.EXAMPLE
    .\install.ps1 -PiHost raspberrypi.local -Token 0123... -UsbipArchive .\usbip-win2.zip -UsbipSha256 ABCD...
    .\install.ps1 -Release v1.0.0 -PiHost raspberrypi.local
    $env:GITHUB_TOKEN = '...'; .\install.ps1 -Release v1.0.0 -PiHost raspberrypi.local
    .\install.ps1 -BundleUrl https://example.invalid/yab/
    .\install.ps1 -WhatIf
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [string]$SourcePath,
    [string]$ConfigExample,
    [string]$BundleDir,
    [string]$BundleUrl,
    [string]$Release,
    [string]$Repo = 'zogami00/you-as-bee',
    [string]$BundleToken,
    [string]$PiHost,
    [string]$Token,
    [string]$UsbipArchive,
    [string]$UsbipSha256,
    [switch]$Force
)

$ErrorActionPreference = 'Stop'

# Origin overrides for a self-hosted mirror and for offline testing.
$githubWeb = $env:YAB_GITHUB_WEB
if (-not $githubWeb) { $githubWeb = 'https://github.com' }
$githubApi = $env:YAB_GITHUB_API
if (-not $githubApi) { $githubApi = 'https://api.github.com' }

# Only the private-fork -Release path honours an ambient GITHUB_TOKEN. -BundleUrl
# requires an explicit -BundleToken, so a token is never sent to whatever host
# -BundleUrl names. Preferring the environment variable keeps the token out of
# this process's command line.
if (-not $BundleToken -and $Release) { $BundleToken = $env:GITHUB_TOKEN }

# Shared task helpers: Test-YabTaskExists falls back to schtasks when
# Get-ScheduledTask throws (see ScheduledTask.ps1).
. (Join-Path $PSScriptRoot 'ScheduledTask.ps1')

$DryRun = [bool]$WhatIfPreference
$script:Changed = $false

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

# Get-Sha256 uses .NET directly rather than Get-FileHash: Get-FileHash honours
# -WhatIfPreference (it emits "What if: ... ProviderPath" and returns nothing),
# which would make every hash comparison a null-equals-null and silently skip
# the copy in a dry run. It returns $null when the file cannot be read (for
# example an ACL-restricted existing install in a non-elevated dry run).
function Get-Sha256([string]$Path) {
    try {
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try {
            $stream = [System.IO.File]::OpenRead($Path)
            try {
                $hash = $sha.ComputeHash($stream)
            } finally {
                $stream.Dispose()
            }
        } finally {
            $sha.Dispose()
        }
    } catch {
        return $null
    }
    return ([System.BitConverter]::ToString($hash)).Replace('-', '')
}

# Test-PathExists is Test-Path that treats an access-denied probe as "present".
# On a machine with a real install, %ProgramData%\you-as-bee is restricted to
# Administrators and SYSTEM, so an unelevated -WhatIf run cannot read
# client.json and Test-Path would throw instead of previewing.
function Test-PathExists([string]$Path) {
    try {
        return (Test-Path -LiteralPath $Path -ErrorAction Stop)
    } catch {
        return $true
    }
}

# Test-PeFile reports whether a file is a plausible Windows PE executable: an
# "MZ" DOS header whose e_lfanew points at a "PE\0\0" signature. It is used to
# reject an HTML error page (or any other non-binary body) saved as yab.exe
# before it reaches %ProgramFiles%.
function Test-PeFile([string]$Path) {
    try {
        $bytes = [System.IO.File]::ReadAllBytes($Path)
    } catch {
        return $false
    }
    if ($bytes.Length -lt 64) { return $false }
    if ($bytes[0] -ne 0x4D -or $bytes[1] -ne 0x5A) { return $false }  # "MZ"
    $lfanew = [System.BitConverter]::ToInt32($bytes, 0x3C)
    if ($lfanew -lt 0 -or ($lfanew + 4) -gt $bytes.Length) { return $false }
    if ($bytes[$lfanew] -ne 0x50 -or $bytes[$lfanew + 1] -ne 0x45 -or
        $bytes[$lfanew + 2] -ne 0x00 -or $bytes[$lfanew + 3] -ne 0x00) {
        return $false
    }
    return $true
}

# Get-YabFile downloads Uri to Dest, with an optional bearer token and Accept
# header. The token goes only into the request header and is never written to
# disk. It fails on an empty body and, when RequirePe is set, on a body without
# a valid PE header.
function Get-YabFile([string]$Uri, [string]$Dest, [string]$Token, [bool]$RequirePe, [string]$Accept) {
    $headers = @{ 'User-Agent' = 'you-as-bee-install' }
    if ($Token) { $headers['Authorization'] = ("Bearer {0}" -f $Token) }
    if ($Accept) { $headers['Accept'] = $Accept }
    # The default progress bar makes a ~10 MB download very slow on Windows
    # PowerShell 5.1; suppress it locally and restore it afterwards.
    $savedProgress = $ProgressPreference
    $ProgressPreference = 'SilentlyContinue'
    try {
        try {
            Invoke-WebRequest -Uri $Uri -OutFile $Dest -Headers $headers -UseBasicParsing -ErrorAction Stop
        } catch {
            Fail ("download failed: {0}: {1}" -f $Uri, $_.Exception.Message)
        }
    } finally {
        $ProgressPreference = $savedProgress
    }
    if (-not (Test-Path -LiteralPath $Dest) -or (Get-Item -LiteralPath $Dest).Length -eq 0) {
        Fail ("downloaded file is empty: {0}" -f $Uri)
    }
    if ($RequirePe -and -not (Test-PeFile $Dest)) {
        Fail ("downloaded file is not a Windows PE binary (an HTML error page?): {0}" -f $Uri)
    }
    Write-Ok ("fetched {0}" -f (Split-Path -Leaf $Dest))
}

# Get-YabBundleFromUrl fetches the three bundle files into DestDir from a
# generic base URL (-BundleUrl). A non-empty body is required for all three;
# the two binaries must also be plausible PE files.
function Get-YabBundleFromUrl([string]$Url, [string]$Token, [string]$DestDir) {
    $base = $Url.TrimEnd('/')
    Get-YabFile -Uri ("{0}/yab.exe" -f $base)  -Dest (Join-Path $DestDir 'yab.exe')  -Token $Token -RequirePe $true -Accept ''
    Get-YabFile -Uri ("{0}/yabw.exe" -f $base) -Dest (Join-Path $DestDir 'yabw.exe') -Token $Token -RequirePe $true -Accept ''
    Get-YabFile -Uri ("{0}/client.example.json" -f $base) -Dest (Join-Path $DestDir 'client.example.json') -Token $Token -RequirePe $false -Accept ''
}

# Resolve-YabLatestTag returns the newest STABLE release tag for RepoSlug by
# following the public releases/latest redirect (no token, no API call). This
# matches the Pi installer's resolution: releases.atom also lists prereleases,
# so the two platforms could otherwise pick different tags.
function Resolve-YabLatestTag([string]$RepoSlug) {
    $latestUrl = ("{0}/{1}/releases/latest" -f $githubWeb, $RepoSlug)
    $req = [System.Net.WebRequest]::Create($latestUrl)
    $req.AllowAutoRedirect = $false
    $req.Method = 'GET'
    $resp = $null
    try {
        $resp = $req.GetResponse()
    } catch [System.Net.WebException] {
        $resp = $_.Exception.Response
    }
    if (-not $resp) {
        Fail ("could not resolve the latest release for {0}; pass an explicit -Release <tag>" -f $RepoSlug)
    }
    $location = $null
    try {
        $location = $resp.Headers['Location']
    } finally {
        $resp.Close()
    }
    if ($location -and ($location -match '/releases/tag/([^/]+)$')) {
        return $Matches[1]
    }
    Fail ("could not resolve the latest release for {0}; pass an explicit -Release <tag>" -f $RepoSlug)
}

# Get-YabReleaseFromPublic downloads the release assets from the plain public
# browser_download_url (https://<host>/<repo>/releases/download/<tag>/...): no
# token, no API call, no Accept header.
function Get-YabReleaseFromPublic([string]$RepoSlug, [string]$Tag, [string]$DestDir) {
    $base = ("{0}/{1}/releases/download/{2}" -f $githubWeb, $RepoSlug, $Tag)
    Get-YabFile -Uri ("{0}/yab-windows-amd64.exe" -f $base)  -Dest (Join-Path $DestDir 'yab.exe')  -Token '' -RequirePe $true -Accept ''
    Get-YabFile -Uri ("{0}/yabw-windows-amd64.exe" -f $base) -Dest (Join-Path $DestDir 'yabw.exe') -Token '' -RequirePe $true -Accept ''
    Get-YabFile -Uri ("{0}/client.example.json" -f $base)    -Dest (Join-Path $DestDir 'client.example.json') -Token '' -RequirePe $false -Accept ''
}

# Get-YabReleaseFromApi is the PRIVATE FORK path, used only when -BundleToken is
# supplied: it lists the release through the authenticated API and downloads
# each asset by its API URL with Accept: application/octet-stream (the
# browser_download_url redirect rejects a bearer token).
function Get-YabReleaseFromApi([string]$RepoSlug, [string]$Tag, [string]$Token, [string]$DestDir) {
    if ($Tag -and $Tag -ne 'latest') {
        $apiUrl = ("{0}/repos/{1}/releases/tags/{2}" -f $githubApi, $RepoSlug, $Tag)
    } else {
        $apiUrl = ("{0}/repos/{1}/releases/latest" -f $githubApi, $RepoSlug)
    }
    $headers = @{
        'User-Agent'    = 'you-as-bee-install'
        'Accept'        = 'application/vnd.github+json'
        'Authorization' = ("Bearer {0}" -f $Token)
    }
    try {
        $release = Invoke-RestMethod -Uri $apiUrl -Headers $headers -ErrorAction Stop
    } catch {
        Fail ("release lookup failed: {0}: {1}" -f $apiUrl, $_.Exception.Message)
    }
    $assets = @{}
    foreach ($a in @($release.assets)) { $assets[$a.name] = $a.url }
    $map = @(
        @{ Asset = 'yab-windows-amd64.exe';  Dest = 'yab.exe';           RequirePe = $true },
        @{ Asset = 'yabw-windows-amd64.exe'; Dest = 'yabw.exe';          RequirePe = $true },
        @{ Asset = 'client.example.json';    Dest = 'client.example.json'; RequirePe = $false }
    )
    foreach ($m in $map) {
        if (-not $assets.ContainsKey($m.Asset)) {
            Fail ("release does not contain asset: {0}" -f $m.Asset)
        }
        Get-YabFile -Uri $assets[$m.Asset] -Dest (Join-Path $DestDir $m.Dest) `
            -Token $Token -RequirePe $m.RequirePe -Accept 'application/octet-stream'
    }
}

# Stop the logon task and the tray processes so a locked binary can be replaced.
# Both process names are handled: older installs run yab.exe, current ones yabw.exe.
function Stop-YabClient {
    if (Test-YabTaskExists 'you-as-bee-client') {
        [void](Stop-YabTask 'you-as-bee-client')
    }
    Get-Process -Name @('yab', 'yabw') -ErrorAction SilentlyContinue | ForEach-Object {
        Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
    }
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

$bundleTemp = $null
try {
    # --- bundle resolution ---------------------------------------------------

    Write-Step 'bundle'

    $explicitSource = $PSBoundParameters.ContainsKey('SourcePath')
    $explicitConfig = $PSBoundParameters.ContainsKey('ConfigExample')
    $explicitBundleDir = $PSBoundParameters.ContainsKey('BundleDir')

    if ($BundleUrl -and $Release) {
        Fail '-BundleUrl and -Release are mutually exclusive.'
    }

    $SourceW = $null

    if ($BundleUrl) {
        # Reject a plaintext bundle URL: the downloaded yab.exe/yabw.exe are
        # run, so https is required. http is allowed only for 127.0.0.1/localhost
        # testing.
        $bundleUri = $null
        if (-not [System.Uri]::TryCreate($BundleUrl, [System.UriKind]::Absolute, [ref]$bundleUri)) {
            Fail ("-BundleUrl is not an absolute URL: {0}" -f $BundleUrl)
        }
        if ($bundleUri.Scheme -ne 'https') {
            $loopback = ($bundleUri.Host -eq '127.0.0.1') -or ($bundleUri.Host -eq 'localhost')
            if (-not ($bundleUri.Scheme -eq 'http' -and $loopback)) {
                Fail '-BundleUrl must use https:// (http:// is allowed only for 127.0.0.1 or localhost testing).'
            }
        }

        # Create the temporary directory with .NET, not New-Item: the latter
        # honours -WhatIfPreference and would skip the download directory in a
        # dry run (a -WhatIf run still downloads so the URL and the file headers
        # can be validated).
        $bundleTemp = Join-Path ([System.IO.Path]::GetTempPath()) ("you-as-bee-bundle-" + [Guid]::NewGuid().ToString('N'))
        [void][System.IO.Directory]::CreateDirectory($bundleTemp)
        Write-Info ("downloading bundle from {0}" -f $BundleUrl)
        Get-YabBundleFromUrl -Url $BundleUrl -Token $BundleToken -DestDir $bundleTemp
        $SourcePath = Join-Path $bundleTemp 'yab.exe'
        $SourceW = Join-Path $bundleTemp 'yabw.exe'
        if (-not $explicitConfig) { $ConfigExample = Join-Path $bundleTemp 'client.example.json' }
    } elseif ($Release) {
        $bundleTemp = Join-Path ([System.IO.Path]::GetTempPath()) ("you-as-bee-bundle-" + [Guid]::NewGuid().ToString('N'))
        [void][System.IO.Directory]::CreateDirectory($bundleTemp)
        if ($BundleToken) {
            Write-Info ("downloading release {0} for {1} (authenticated API; private fork)" -f $Release, $Repo)
            Get-YabReleaseFromApi -RepoSlug $Repo -Tag $Release -Token $BundleToken -DestDir $bundleTemp
        } else {
            $tag = $Release
            if (-not $tag -or $tag -eq 'latest') { $tag = Resolve-YabLatestTag $Repo }
            Write-Info ("downloading release {0} for {1} (public download, no token)" -f $tag, $Repo)
            Get-YabReleaseFromPublic -RepoSlug $Repo -Tag $tag -DestDir $bundleTemp
        }
        $SourcePath = Join-Path $bundleTemp 'yab.exe'
        $SourceW = Join-Path $bundleTemp 'yabw.exe'
        if (-not $explicitConfig) { $ConfigExample = Join-Path $bundleTemp 'client.example.json' }
    } else {
        if ($explicitBundleDir) { $bundleDir = $BundleDir } else { $bundleDir = $PSScriptRoot }
        $bundleYab = Join-Path $bundleDir 'yab.exe'
        $bundleYabw = Join-Path $bundleDir 'yabw.exe'
        $hasBundle = (Test-Path -LiteralPath $bundleYab) -and (Test-Path -LiteralPath $bundleYabw)

        if ($explicitBundleDir -and -not $hasBundle) {
            Fail ("-BundleDir must contain yab.exe and yabw.exe: {0}" -f $bundleDir)
        }

        if (-not $explicitSource -and $hasBundle) {
            Write-Info ("using local bundle in {0}" -f $bundleDir)
            $SourcePath = $bundleYab
            $SourceW = $bundleYabw
            if (-not $explicitConfig) { $ConfigExample = Join-Path $bundleDir 'client.example.json' }
        } else {
            if (-not $SourcePath) {
                $SourcePath = Join-Path $PSScriptRoot '..\..\dist\yab-windows-amd64.exe'
            }
            $SourceW = Join-Path (Split-Path -Parent $SourcePath) 'yabw-windows-amd64.exe'
        }
    }

    if (-not (Test-Path -LiteralPath $SourcePath)) {
        Fail ("yab.exe not found: {0} (build it with scripts\build.ps1, or use -BundleDir/-BundleUrl/-Release)" -f $SourcePath)
    }
    $SourcePath = (Resolve-Path -LiteralPath $SourcePath).Path
    if (-not (Test-Path -LiteralPath $SourceW)) {
        Fail ("yabw.exe not found: {0} (build it with scripts\build.ps1, or use -BundleDir/-BundleUrl/-Release)" -f $SourceW)
    }
    $SourceW = (Resolve-Path -LiteralPath $SourceW).Path

    if (-not $ConfigExample) {
        $ConfigExample = Join-Path $PSScriptRoot 'client.example.json'
    }
    if (-not (Test-Path -LiteralPath $ConfigExample)) {
        Fail ("config example not found: {0}" -f $ConfigExample)
    }
    $ConfigExample = (Resolve-Path -LiteralPath $ConfigExample).Path

    if ($Token -and ($Token -notmatch '^[0-9a-f]{64}$')) {
        Fail 'Token must be exactly 64 lower-case hex characters.'
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

    # --- usbip-win2 ----------------------------------------------------------

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

    # --- binaries ------------------------------------------------------------

    Write-Step 'binaries'

    $installDir = Join-Path $env:ProgramFiles 'you-as-bee'
    $installYab = Join-Path $installDir 'yab.exe'
    $installYabw = Join-Path $installDir 'yabw.exe'

    $binaries = @(
        [pscustomobject]@{ Source = $SourcePath; Target = $installYab },
        [pscustomobject]@{ Source = $SourceW;    Target = $installYabw }
    )

    $needCopy = $false
    foreach ($b in $binaries) {
        if (Test-PathExists $b.Target) {
            if ((Get-Sha256 $b.Target) -ne (Get-Sha256 $b.Source)) { $needCopy = $true }
        } else {
            $needCopy = $true
        }
    }

    if ($needCopy) {
        if (-not (Test-PathExists $installDir)) {
            if ($PSCmdlet.ShouldProcess($installDir, 'Create directory')) {
                New-Item -ItemType Directory -Path $installDir -Force | Out-Null
            }
        }
        if (-not $DryRun) { Stop-YabClient }
        foreach ($b in $binaries) {
            if ((Test-PathExists $b.Target) -and
                ((Get-Sha256 $b.Target) -eq (Get-Sha256 $b.Source))) {
                continue
            }
            $leaf = Split-Path -Leaf $b.Target
            if ($PSCmdlet.ShouldProcess($b.Target, ("Copy " + $leaf))) {
                Copy-Item -LiteralPath $b.Source -Destination $b.Target -Force
                $script:Changed = $true
                Write-Ok ("installed {0}" -f $b.Target)
            } else {
                Write-Info ("would install {0}" -f $b.Target)
            }
        }
    } else {
        Write-Ok 'yab.exe and yabw.exe already up to date'
    }

    # --- config --------------------------------------------------------------

    Write-Step 'client config'

    $dataDir = Join-Path $env:ProgramData 'you-as-bee'
    $configPath = Join-Path $dataDir 'client.json'

    if (-not (Test-PathExists $dataDir)) {
        if ($PSCmdlet.ShouldProcess($dataDir, 'Create directory')) {
            New-Item -ItemType Directory -Path $dataDir -Force | Out-Null
        }
    }

    # Restrict the directory BEFORE writing client.json: the file holds the bearer
    # token, and the inherited ACL leaves a window where Users could read it.
    if ($PSCmdlet.ShouldProcess($dataDir, 'Restrict ACL to Administrators and SYSTEM')) {
        & icacls $dataDir /inheritance:r /grant:r '*S-1-5-32-544:(OI)(CI)F' /grant:r '*S-1-5-18:(OI)(CI)F' | Out-Null
        if ($LASTEXITCODE -ne 0) { Fail ("icacls failed (exit {0})" -f $LASTEXITCODE) }
        Write-Ok 'ACL restricted to Administrators and SYSTEM'
    }

    $writeConfig = (-not (Test-PathExists $configPath)) -or $Force
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
            $script:Changed = $true
            Write-Ok ("wrote {0}" -f $configPath)
        } else {
            Write-Info ("would write {0}" -f $configPath)
        }
        if (-not $PiHost -or -not $Token) {
            Write-Warn 'edit host and token in client.json before use (see docs/configuration.md).'
        }
    } else {
        Write-Ok 'client.json already present (use -Force to overwrite)'
    }

    # --- scheduled task ------------------------------------------------------

    Write-Step 'logon task'

    $taskName = 'you-as-bee-client'
    $register = $true
    if ((Test-YabTaskExists $taskName) -and -not $Force) {
        $register = $false
        Write-Ok 'task already registered (use -Force to re-register)'
    }
    if ($register) {
        # Register-ScheduledTask (not `schtasks /TR`) for two reasons:
        #   1. schtasks /TR cannot express the settings below, and its argument
        #      quoting fails on PowerShell 5.1 for a path containing spaces;
        #   2. the settings stop Windows killing the task after 72 hours or
        #      refusing to start it on battery.
        #
        # The action runs yabw.exe, the GUI-subsystem build: the tray starts with
        # no console window at logon.
        if ($PSCmdlet.ShouldProcess($taskName, 'Register logon Scheduled Task')) {
            $action = New-ScheduledTaskAction -Execute $installYabw -Argument 'tray'
            $trigger = New-ScheduledTaskTrigger -AtLogOn
            $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit 0 `
                -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
            Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger `
                -Settings $settings -RunLevel Highest -Force | Out-Null
            $script:Changed = $true
        } else {
            Write-Info ("would register '{0}' as: {1} tray" -f $taskName, $installYabw)
        }
        if (-not $DryRun) {
            Write-Ok ("registered task '{0}'" -f $taskName)
        }
    }

    Write-Host ''
    if ($DryRun) {
        Write-Host 'Dry run complete: no changes were made.' -ForegroundColor Cyan
    } elseif (-not $script:Changed) {
        Write-Host 'Install complete: no changes were needed.' -ForegroundColor Green
    } else {
        Write-Host 'Install complete.' -ForegroundColor Green
        Write-Host 'Next: run "yab doctor" and attach a device, or log off and on again for the windowless tray task.'
    }
}
finally {
    # Remove the download directory with .NET, not Remove-Item: the latter
    # honours -WhatIfPreference and would leave the temporary directory behind
    # after a dry run.
    if ($bundleTemp) {
        try {
            [System.IO.Directory]::Delete($bundleTemp, $true)
        } catch {
            Write-Verbose ("could not remove temporary bundle directory: {0}" -f $_.Exception.Message)
        }
    }
}
