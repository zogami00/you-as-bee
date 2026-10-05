#Requires -Version 5.1
# check.ps1 - full local validation gate for you-as-bee.
#
# Windows PowerShell 5.1 compatible; ASCII only.
#
# Runs, in order, stopping at the first failure:
#   1. gofmt -l .        (fail if it prints any file)
#   2. go vet ./...
#   3. go test ./...
#   4. go build ./...
#   5. the three cross-builds (windows/amd64, linux/arm64, linux/arm GOARM=7)
#
# NOTE: `go test -race` is intentionally excluded. The race detector requires
# cgo/gcc, and this project builds with CGO_ENABLED=0 and has no gcc dependency
# (including on the Windows development machines). CI runs -race on Ubuntu
# only, where gcc is available.

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot

$savedGOOS = $env:GOOS
$savedGOARCH = $env:GOARCH
$savedGOARM = $env:GOARM
$savedCGO = $env:CGO_ENABLED

try {
    Write-Host '== 1/5 gofmt -l . =='
    $unformatted = & gofmt -l .
    if ($LASTEXITCODE -ne 0) { throw ("gofmt exited {0}" -f $LASTEXITCODE) }
    if ($unformatted) {
        Write-Host $unformatted
        throw 'gofmt found unformatted files'
    }
    Write-Host 'gofmt OK'

    Write-Host '== 2/5 go vet ./... =='
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw ("go vet failed (exit {0})" -f $LASTEXITCODE) }
    Write-Host 'go vet OK'

    Write-Host '== 3/5 go test ./... =='
    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw ("go test failed (exit {0})" -f $LASTEXITCODE) }
    Write-Host 'go test OK'

    Write-Host '== 4/5 go build ./... =='
    & go build ./...
    if ($LASTEXITCODE -ne 0) { throw ("go build failed (exit {0})" -f $LASTEXITCODE) }
    Write-Host 'go build OK'

    Write-Host '== 5/5 cross-builds =='
    $env:CGO_ENABLED = '0'
    $dist = Join-Path $repoRoot 'dist'
    if (-not (Test-Path $dist)) {
        New-Item -ItemType Directory -Path $dist | Out-Null
    }

    $targets = @(
        [pscustomobject]@{ Name = 'yab-windows-amd64.exe'; Package = './cmd/yab';  TargetGOOS = 'windows'; TargetGOARCH = 'amd64'; TargetGOARM = '' },
        [pscustomobject]@{ Name = 'yabd-linux-arm64';       Package = './cmd/yabd'; TargetGOOS = 'linux';   TargetGOARCH = 'arm64'; TargetGOARM = '' },
        [pscustomobject]@{ Name = 'yabd-linux-armv7';       Package = './cmd/yabd'; TargetGOOS = 'linux';   TargetGOARCH = 'arm';   TargetGOARM = '7' }
    )

    foreach ($t in $targets) {
        $env:GOOS = $t.TargetGOOS
        $env:GOARCH = $t.TargetGOARCH
        if ([string]::IsNullOrEmpty($t.TargetGOARM)) {
            Remove-Item Env:\GOARM -ErrorAction SilentlyContinue
        } else {
            $env:GOARM = $t.TargetGOARM
        }

        $out = Join-Path $dist $t.Name
        & go build -o $out $t.Package
        if ($LASTEXITCODE -ne 0) { throw ("cross-build failed: {0}" -f $t.Name) }
        Write-Host ("cross-build OK  {0}" -f $t.Name)
    }
}
finally {
    if ([string]::IsNullOrEmpty($savedGOOS)) { Remove-Item Env:\GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $savedGOOS }
    if ([string]::IsNullOrEmpty($savedGOARCH)) { Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $savedGOARCH }
    if ([string]::IsNullOrEmpty($savedGOARM)) { Remove-Item Env:\GOARM -ErrorAction SilentlyContinue } else { $env:GOARM = $savedGOARM }
    if ([string]::IsNullOrEmpty($savedCGO)) { Remove-Item Env:\CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $savedCGO }
    Pop-Location
}

Write-Host 'CHECK OK' -ForegroundColor Green
exit 0
