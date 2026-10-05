#Requires -Version 5.1
<#
.SYNOPSIS
    Tests for ScheduledTask.ps1.

.DESCRIPTION
    Plain Windows PowerShell 5.1 script, no Pester dependency. Covers B8: when
    Get-ScheduledTask throws (the "task XML contains a value which is incorrectly
    formatted or out of range" bug observed on Windows 11), Test-YabTaskExists
    must fall back to schtasks /query /TN and report existence correctly. It
    also covers m4/m5: a schtasks failure that is not the "no such task" message
    must be surfaced rather than read as absence, and Remove-YabTask must not
    report success on a genuine delete failure.

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

# A non-zero exit is only "absent" when schtasks says so.
function schtasks { $global:LASTEXITCODE = 1; Write-Output 'ERROR: The specified task name "stub" does not exist in the system.' }
Assert-True (-not (Test-YabTaskExists 'stub')) 'fallback reports a missing task when schtasks says it does not exist'

# Any other failure must not be read as absence.
function schtasks { $global:LASTEXITCODE = 1; Write-Output 'ERROR: Access is denied.' }
$threw = $false
try { [void](Test-YabTaskExists 'stub') } catch { $threw = $true }
Assert-True $threw 'a genuine schtasks failure is surfaced, not read as absence'

# 3. Remove-YabTask: delete succeeded.
function schtasks { $global:LASTEXITCODE = 0; return }
Assert-True (Remove-YabTask 'stub') 'Remove-YabTask succeeds when the delete exits zero'

# 4. Remove-YabTask: already absent is success.
function schtasks { $global:LASTEXITCODE = 1; Write-Output 'ERROR: The system cannot find the file specified.' }
Assert-True (Remove-YabTask 'stub') 'Remove-YabTask treats an already-absent task as success'

# 5. Remove-YabTask: a genuine failure is not reported as success.
function schtasks { $global:LASTEXITCODE = 1; Write-Output 'ERROR: Access is denied.' }
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
