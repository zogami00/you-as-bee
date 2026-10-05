#Requires -Version 5.1
<#
.SYNOPSIS
    Tests for ScheduledTask.ps1.

.DESCRIPTION
    Plain Windows PowerShell 5.1 script, no Pester dependency. Covers B8: when
    Get-ScheduledTask throws (the "task XML contains a value which is incorrectly
    formatted or out of range" bug observed on Windows 11), Test-YabTaskExists
    must fall back to schtasks and report existence from the locale-independent
    CSV task listing. It also covers a genuine schtasks query failure being
    surfaced rather than read as absence - which would otherwise abort a first
    install - and Remove-YabTask not reporting success on a genuine delete
    failure.

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

# From here on the module throws (the observed bug): schtasks must decide.
function Get-ScheduledTask { throw 'The task XML contains a value which is incorrectly formatted or out of range.' }

# 2. Task present: the CSV listing contains the task name.
function schtasks {
    $global:LASTEXITCODE = 0
    Write-Output '"\stub","N/A","Ready"'
}
Assert-True (Test-YabTaskExists 'stub') 'fallback reports an existing task from the CSV listing'

# 2b. Task absent: the CSV listing does not contain the task name. The decision
# does not depend on the language of any error text.
function schtasks {
    $global:LASTEXITCODE = 0
    Write-Output '"\someone-else","N/A","Ready"'
}
Assert-True (-not (Test-YabTaskExists 'stub')) 'fallback reports a task absent from the CSV listing as missing'

# 2c. An empty task list still reports the task absent.
function schtasks {
    $global:LASTEXITCODE = 0
    Write-Output 'INFO: There are no scheduled tasks present in the system.'
}
Assert-True (-not (Test-YabTaskExists 'stub')) 'fallback reports absent when the system has no tasks'

# 2d. A genuine query failure (unrecognised, localised text) must be surfaced,
# not read as absence - otherwise a first install would abort even with -Force.
function schtasks { $global:LASTEXITCODE = 1; Write-Output 'ERROR: Zugriff verweigert.' }
$threw = $false
try { [void](Test-YabTaskExists 'stub') } catch { $threw = $true }
Assert-True $threw 'a genuine schtasks query failure is surfaced, not read as absence'

# 3. Remove-YabTask: delete succeeded.
function schtasks { $global:LASTEXITCODE = 0; return }
Assert-True (Remove-YabTask 'stub') 'Remove-YabTask succeeds when the delete exits zero'

# 4. Remove-YabTask: the delete failed but the task is gone from the listing.
function schtasks {
    if ($args -contains '/Delete') { $global:LASTEXITCODE = 1; Write-Output 'ERROR: ...'; return }
    $global:LASTEXITCODE = 0
    Write-Output '"\someone-else","N/A","Ready"'
}
Assert-True (Remove-YabTask 'stub') 'Remove-YabTask treats an already-absent task as success'

# 5. Remove-YabTask: the delete failed and the task is still listed.
function schtasks {
    if ($args -contains '/Delete') { $global:LASTEXITCODE = 1; Write-Output 'ERROR: ...'; return }
    $global:LASTEXITCODE = 0
    Write-Output '"\stub","N/A","Ready"'
}
Assert-True (-not (Remove-YabTask 'stub')) 'Remove-YabTask reports failure when the delete genuinely fails'

# 6. Real schtasks, module still throwing: a task that does not exist is absent.
Remove-Item function:\schtasks
Assert-True (-not (Test-YabTaskExists 'you-as-bee-definitely-missing-xyz')) 'real schtasks reports a missing task absent'

if ($script:Failures -gt 0) {
    Write-Host ("{0} test(s) failed" -f $script:Failures) -ForegroundColor Red
    exit 1
}
Write-Host 'all ScheduledTask.ps1 tests passed' -ForegroundColor Green
exit 0
