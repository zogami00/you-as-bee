#Requires -Version 5.1
# build.ps1 - cross-build the you-as-bee binaries into dist/.
#
# Windows PowerShell 5.1 compatible; ASCII only.
# The USB/IP port (3240) is not configurable and is not a build input.
#
# Targets:
#   dist/yab-windows-amd64.exe   ./cmd/yab   GOOS=windows GOARCH=amd64
#   dist/yabw-windows-amd64.exe  ./cmd/yab   GOOS=windows GOARCH=amd64, -H windowsgui
#   dist/yabd-linux-arm64        ./cmd/yabd  GOOS=linux   GOARCH=arm64
#   dist/yabd-linux-armv7        ./cmd/yabd  GOOS=linux   GOARCH=arm   GOARM=7
#
# yabw is the same package built for the GUI subsystem: launching it opens no
# console window (see docs/setup-windows.md). yab.exe stays a console binary so
# the CLI (list, status, doctor, ...) keeps its output.

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot

$savedGOOS = $env:GOOS
$savedGOARCH = $env:GOARCH
$savedGOARM = $env:GOARM
$savedCGO = $env:CGO_ENABLED

$failures = 0

try {
    # Version metadata for -ldflags, taken from git when available.
    $version = 'dev'
    $commit = 'none'
    $date = 'unknown'

    if (Get-Command git -ErrorAction SilentlyContinue) {
        $v = (& git describe --tags --always --dirty 2>$null)
        if ($v) { $version = $v }
        $c = (& git rev-parse --short HEAD 2>$null)
        if ($c) { $commit = $c }
        $d = (& git show -s --format=%cI HEAD 2>$null)
        if ($d) { $date = $d }
    } else {
        Write-Warning 'git not found; using default version metadata'
    }

    $ldflags = "-X github.com/zogami00/you-as-bee/internal/version.Version=$version " +
               "-X github.com/zogami00/you-as-bee/internal/version.Commit=$commit " +
               "-X github.com/zogami00/you-as-bee/internal/version.Date=$date"

    $env:CGO_ENABLED = '0'

    $dist = Join-Path $repoRoot 'dist'
    if (-not (Test-Path $dist)) {
        New-Item -ItemType Directory -Path $dist | Out-Null
    }

    $targets = @(
        [pscustomobject]@{ Name = 'yab-windows-amd64.exe';  Package = './cmd/yab';  TargetGOOS = 'windows'; TargetGOARCH = 'amd64'; TargetGOARM = '';  ExtraLdflags = '' },
        [pscustomobject]@{ Name = 'yabw-windows-amd64.exe'; Package = './cmd/yab';  TargetGOOS = 'windows'; TargetGOARCH = 'amd64'; TargetGOARM = '';  ExtraLdflags = '-H windowsgui' },
        [pscustomobject]@{ Name = 'yabd-linux-arm64';       Package = './cmd/yabd'; TargetGOOS = 'linux';   TargetGOARCH = 'arm64'; TargetGOARM = '';  ExtraLdflags = '' },
        [pscustomobject]@{ Name = 'yabd-linux-armv7';       Package = './cmd/yabd'; TargetGOOS = 'linux';   TargetGOARCH = 'arm';   TargetGOARM = '7'; ExtraLdflags = '' }
    )

    foreach ($t in $targets) {
        $env:GOOS = $t.TargetGOOS
        $env:GOARCH = $t.TargetGOARCH
        if ([string]::IsNullOrEmpty($t.TargetGOARM)) {
            Remove-Item Env:\GOARM -ErrorAction SilentlyContinue
            $armLabel = ''
        } else {
            $env:GOARM = $t.TargetGOARM
            $armLabel = ' GOARM=' + $t.TargetGOARM
        }

        $out = Join-Path $dist $t.Name
        Write-Host ("building {0}/{1}{2} {3} -> {4}" -f $t.TargetGOOS, $t.TargetGOARCH, $armLabel, $t.Package, $out)

        $targetLdflags = $ldflags
        if (-not [string]::IsNullOrEmpty($t.ExtraLdflags)) {
            $targetLdflags = ("{0} {1}" -f $ldflags, $t.ExtraLdflags)
        }

        & go build -trimpath -ldflags $targetLdflags -o $out $t.Package
        if ($LASTEXITCODE -eq 0) {
            Write-Host ("  OK   {0}" -f $t.Name) -ForegroundColor Green
        } else {
            Write-Host ("  FAIL {0} (go build exit {1})" -f $t.Name, $LASTEXITCODE) -ForegroundColor Red
            $failures = $failures + 1
        }
    }
}
finally {
    if ([string]::IsNullOrEmpty($savedGOOS)) { Remove-Item Env:\GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $savedGOOS }
    if ([string]::IsNullOrEmpty($savedGOARCH)) { Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $savedGOARCH }
    if ([string]::IsNullOrEmpty($savedGOARM)) { Remove-Item Env:\GOARM -ErrorAction SilentlyContinue } else { $env:GOARM = $savedGOARM }
    if ([string]::IsNullOrEmpty($savedCGO)) { Remove-Item Env:\CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $savedCGO }
    Pop-Location
}

if ($failures -gt 0) {
    Write-Host ("BUILD FAILED: {0} target(s) failed" -f $failures) -ForegroundColor Red
    exit 1
}

Write-Host 'BUILD OK' -ForegroundColor Green
exit 0
