#Requires -Version 5.1
<#
.SYNOPSIS
    Shared Scheduled Task helpers for the you-as-bee Windows scripts.

.DESCRIPTION
    On some Windows builds Get-ScheduledTask throws

        The task XML contains a value which is incorrectly formatted or out of
        range.

    when reading a task by name, even for a perfectly valid task (Microsoft's
    own tasks included). Tests on Windows 11 reproduced this for every task in
    the root folder while schtasks.exe read the same tasks fine, so
    Get-ScheduledTask is not a reliable existence check. These helpers fall back
    to schtasks.exe, which keeps install.ps1 and uninstall.ps1 idempotent.

    Windows PowerShell 5.1 compatible; ASCII only.
#>

# Invoke-SchTasks runs schtasks with the caller's error preference relaxed: a
# native command that writes to stderr becomes a terminating NativeCommandError
# when ErrorActionPreference is Stop, which would break the "task does not
# exist" probe. It returns an object carrying both the exit code and the merged
# stdout+stderr text, because the exit code alone cannot distinguish "task not
# found" from a genuine failure such as an access-denied error.
function Invoke-SchTasks {
    [CmdletBinding()]
    [OutputType([pscustomobject])]
    param(
        [Parameter(Mandatory = $true, ValueFromRemainingArguments = $true)]
        [string[]]$Arguments
    )

    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = (& schtasks @Arguments 2>&1 | Out-String)
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }

    if ($null -eq $output) { $output = '' }
    return [pscustomobject]@{
        ExitCode = $code
        Output   = $output
    }
}

# Test-SchTasksNotFound reports whether schtasks output is the "no such task"
# message rather than a real error. schtasks prints (English, case-insensitive)
# either "ERROR: The system cannot find the file specified." or "ERROR: The
# specified task name ... does not exist in the system.". Any other non-zero
# exit is a genuine failure and must not be read as absence.
function Test-SchTasksNotFound {
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory = $false)]
        [AllowEmptyString()]
        [string]$Output
    )

    if ([string]::IsNullOrEmpty($Output)) { return $false }
    $text = $Output.ToLowerInvariant()
    return ($text -match 'does not exist') -or ($text -match 'cannot find')
}

# Test-YabTaskExists reports whether a Scheduled Task named TaskName exists. It
# tries the ScheduledTasks module first and falls back to `schtasks /query /TN`
# when the module throws or reports nothing. A schtasks failure that is not the
# "no such task" message throws, so a genuine error is never silently read as
# "task absent".
function Test-YabTaskExists {
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory = $true)]
        [string]$TaskName
    )

    try {
        if (Get-ScheduledTask -TaskName $TaskName -ErrorAction Stop) {
            return $true
        }
    } catch {
        # The module can fail on a valid task; fall through to schtasks.
    }

    $result = Invoke-SchTasks /query /TN $TaskName
    if ($result.ExitCode -eq 0) { return $true }
    if (Test-SchTasksNotFound $result.Output) { return $false }
    throw ("schtasks /query /TN '{0}' failed (exit {1}): {2}" -f $TaskName, $result.ExitCode, $result.Output.Trim())
}

# Stop-YabTask ends a running Scheduled Task. Best effort; the ScheduledTasks
# cmdlets fail on the same tasks the query does. Returns $true when schtasks
# reports the task was ended, and $false otherwise - including when the task
# exists but is not running, which schtasks reports with a non-zero exit.
function Stop-YabTask {
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory = $true)]
        [string]$TaskName
    )

    return ((Invoke-SchTasks /End /TN $TaskName).ExitCode -eq 0)
}

# Remove-YabTask deletes a Scheduled Task. Returns $true when it was deleted or
# was already absent, and $false on a genuine delete failure. schtasks reports a
# missing task with a non-zero exit and a "cannot find"/"does not exist"
# message, so a non-zero exit is classified from that text rather than assumed
# to be either success or failure.
function Remove-YabTask {
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory = $true)]
        [string]$TaskName
    )

    $delete = Invoke-SchTasks /Delete /TN $TaskName /F
    if ($delete.ExitCode -eq 0) { return $true }
    if (Test-SchTasksNotFound $delete.Output) { return $true }

    # Delete failed for some other reason. Confirm with a query: if the task is
    # really gone now, treat it as a success; otherwise report failure to the
    # caller rather than claiming it was removed.
    $query = Invoke-SchTasks /query /TN $TaskName
    if ($query.ExitCode -ne 0) {
        if (Test-SchTasksNotFound $query.Output) { return $true }
    }
    return $false
}
