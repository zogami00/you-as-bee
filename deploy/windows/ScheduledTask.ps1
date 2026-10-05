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
# exist" probe. Returns the process exit code.
function Invoke-SchTasks {
    [CmdletBinding()]
    [OutputType([int])]
    param(
        [Parameter(Mandatory = $true, ValueFromRemainingArguments = $true)]
        [string[]]$Arguments
    )

    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        & schtasks @Arguments *> $null
        return $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
}

# Test-YabTaskExists reports whether a Scheduled Task named TaskName exists. It
# tries the ScheduledTasks module first and falls back to `schtasks /query /TN`
# when the module throws or reports nothing.
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

    return ((Invoke-SchTasks /query /TN $TaskName) -eq 0)
}

# Stop-YabTask ends a running Scheduled Task. Best effort; the ScheduledTasks
# cmdlets fail on the same tasks the query does. Returns $true when the task was
# ended or was not running.
function Stop-YabTask {
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory = $true)]
        [string]$TaskName
    )

    return ((Invoke-SchTasks /End /TN $TaskName) -eq 0)
}

# Remove-YabTask deletes a Scheduled Task. Returns $true when it was deleted or
# was already absent. schtasks reports a missing task with a non-zero exit and a
# "cannot find" message, so a non-zero exit is confirmed with a query rather
# than assumed to be a real failure.
function Remove-YabTask {
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory = $true)]
        [string]$TaskName
    )

    if ((Invoke-SchTasks /Delete /TN $TaskName /F) -eq 0) {
        return $true
    }
    # Not deleted: absent (fine) or a real failure (let the caller see it exit
    # non-zero from the query and decide).
    return ((Invoke-SchTasks /query /TN $TaskName) -ne 0)
}
