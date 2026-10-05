#Requires -Version 5.1
<#
.SYNOPSIS
    Tests for ScheduledTask.ps1.

.DESCRIPTION
    Plain Windows PowerShell 5.1 script, no Pester dependency. Covers B8: when
    Get-ScheduledTask throws (the "task XML contains a value which is incorrectly
    formatted or out of range" bug observed on Windows 11), Test-YabTaskExists
    must fall back to schtasks /query /TN and report existence correctly.

    Run:  powershell -NoProfile -ExecutionPolicy Bypass -File .\ScheduledTask.Tests.ps1
    Exit code is 0 when every check passes, 1 otherwise.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'ScheduledTask.ps1')

$script:Failures = 0

function Assert-True([bool]$Condition, [string]$Message) {
    if ($Condition) {
        Write-Host ("  PASS: {0}" -f $Message) -ForegroundColor Green
    } else {
        Write-Host ("  FAIL: {0}" -f $Message) -ForegroundColor Red
        $script:Failures++
    }
}

Write-Host 'ScheduledTask.ps1 tests'

# 1. A module result is trusted without consulting schtasks.
function Get-ScheduledTask { [pscustomobject]@{ TaskName = 'stub' } }
Assert-True (Test-YabTaskExists 'stub') 'existing task is detected via Get-ScheduledTask'

# 2. The module throws (the observed bug): the schtasks fallback must decide.
function Get-ScheduledTask { throw 'The task XML contains a value which is incorrectly formatted or out of range.' }
function schtasks { $global:LASTEXITCODE = 0; return }
Assert-True (Test-YabTaskExists 'stub') 'fallback reports an existing task when the module throws'

function schtasks { $global:LASTEXITCODE = 1; return }
Assert-True (-not (Test-YabTaskExists 'stub')) 'fallback reports a missing task when the module throws'

# 3. Real schtasks, module still throwing: a task that does not exist is absent.
Remove-Item function:\schtasks
Assert-True (-not (Test-YabTaskExists 'you-as-bee-definitely-missing-xyz')) 'real schtasks reports a missing task absent'

if ($script:Failures -gt 0) {
    Write-Host ("{0} test(s) failed" -f $script:Failures) -ForegroundColor Red
    exit 1
}
Write-Host 'all ScheduledTask.ps1 tests passed' -ForegroundColor Green
exit 0
