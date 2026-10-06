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
#   5. the four cross-builds (windows/amd64 console yab, windows/amd64 GUI
#      yabw, linux/arm64, linux/arm GOARM=7), asserting the PE subsystem of
#      each Windows binary (3 = console, 2 = GUI/windowless).
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
        [pscustomobject]@{ Name = 'yab-windows-amd64.exe';  Package = './cmd/yab';  TargetGOOS = 'windows'; TargetGOARCH = 'amd64'; TargetGOARM = ''; Ldflags = '';              PeSubsystem = 3 },
        [pscustomobject]@{ Name = 'yabw-windows-amd64.exe'; Package = './cmd/yab';  TargetGOOS = 'windows'; TargetGOARCH = 'amd64'; TargetGOARM = ''; Ldflags = '-H windowsgui'; PeSubsystem = 2 },
        [pscustomobject]@{ Name = 'yabd-linux-arm64';       Package = './cmd/yabd'; TargetGOOS = 'linux';   TargetGOARCH = 'arm64'; TargetGOARM = ''; Ldflags = '';              PeSubsystem = 0 },
        [pscustomobject]@{ Name = 'yabd-linux-armv7';       Package = './cmd/yabd'; TargetGOOS = 'linux';   TargetGOARCH = 'arm';   TargetGOARM = '7'; Ldflags = '';             PeSubsystem = 0 }
    )

    # Get-PeSubsystem reads the COFF optional-header Subsystem word (2 = GUI,
    # 3 = console) from a PE file. It is how check.ps1 guards that yabw.exe is
    # still built with -H windowsgui.
    function Get-PeSubsystem([string]$Path) {
        $fs = [System.IO.File]::OpenRead($Path)
        try {
            $br = New-Object System.IO.BinaryReader($fs)
            try {
                $fs.Position = 0x3C
                $lfanew = $br.ReadInt32()
                # Subsystem is at optional-header offset 68, after the 4-byte PE
                # signature and the 20-byte IMAGE_FILE_HEADER.
                $fs.Position = $lfanew + 4 + 20 + 68
                return [int]$br.ReadUInt16()
            } finally {
                $br.Dispose()
            }
        } finally {
            $fs.Dispose()
        }
    }

    foreach ($t in $targets) {
        $env:GOOS = $t.TargetGOOS
        $env:GOARCH = $t.TargetGOARCH
        if ([string]::IsNullOrEmpty($t.TargetGOARM)) {
            Remove-Item Env:\GOARM -ErrorAction SilentlyContinue
        } else {
            $env:GOARM = $t.TargetGOARM
        }

        $out = Join-Path $dist $t.Name
        $buildArgs = @('-o', $out)
        if (-not [string]::IsNullOrEmpty($t.Ldflags)) {
            $buildArgs += @('-ldflags', $t.Ldflags)
        }
        $buildArgs += $t.Package
        & go build @buildArgs
        if ($LASTEXITCODE -ne 0) { throw ("cross-build failed: {0}" -f $t.Name) }
        if ($t.PeSubsystem -gt 0) {
            $sub = Get-PeSubsystem $out
            if ($sub -ne $t.PeSubsystem) {
                throw ("{0}: PE subsystem {1}, want {2}" -f $t.Name, $sub, $t.PeSubsystem)
            }
            Write-Host ("cross-build OK  {0} (PE subsystem {1})" -f $t.Name, $sub)
        } else {
            Write-Host ("cross-build OK  {0}" -f $t.Name)
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

Write-Host 'CHECK OK' -ForegroundColor Green
exit 0
