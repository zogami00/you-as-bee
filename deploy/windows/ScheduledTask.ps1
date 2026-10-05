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

# Get-SchTasksListing runs `schtasks /query /FO CSV /NH` and returns the raw
# result object, so callers can decide presence from the task-name column of
# the CSV rather than from schtasks' human-readable "task not found" message,
# which is localised and therefore unreliable on non-English Windows.
function Get-SchTasksListing {
    [CmdletBinding()]
    [OutputType([pscustomobject])]
    param()

    return Invoke-SchTasks /query /FO CSV /NH
}

# Test-SchTasksTaskListed reports whether TaskName appears in a `schtasks
# /query /FO CSV /NH` listing. Each CSV row's first field is the task name,
# quoted and prefixed with a backslash (for example `"\you-as-bee","N/A",
# "Ready"`); the parser reads that field directly and compares it after
# stripping the quoting and the leading backslash. Because the decision is made
# from the listing - not from a localised error string - a missing task is
# reported the same way on any system language.
#
# A non-zero exit from the query itself is a genuine failure (access denied, a
# broken task database, ...) and throws: it must never be read as "task absent",
# or a first install on a machine where the task does not exist yet would abort.
function Test-SchTasksTaskListed {
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory = $true)]
        [string]$TaskName
    )

    $result = Get-SchTasksListing
    if ($result.ExitCode -ne 0) {
        throw ("schtasks /query /FO CSV /NH failed (exit {0}): {1}" -f $result.ExitCode, $result.Output.Trim())
    }

    $want = $TaskName.TrimStart('\')
    foreach ($line in ($result.Output -split '\r?\n')) {
        if (-not $line.StartsWith('"')) { continue }
        $close = $line.IndexOf('"', 1)
        if ($close -lt 1) { continue }
        $name = $line.Substring(1, $close - 1).TrimStart('\')
        if ($name -ieq $want) { return $true }
    }
    return $false
}

# Test-YabTaskExists reports whether a Scheduled Task named TaskName exists. It
# tries the ScheduledTasks module first and falls back to a `schtasks` CSV
# listing when the module throws or reports nothing. A genuine schtasks query
# failure throws, so an error is never silently read as "task absent".
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

    return (Test-SchTasksTaskListed -TaskName $TaskName)
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
# was already absent, and $false on a genuine delete failure. A non-zero delete
# exit is not itself "absent": presence is confirmed from the CSV task listing,
# because schtasks' "task not found" text is localised and cannot be matched on
# a non-English Windows. A listing query that genuinely fails throws.
function Remove-YabTask {
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory = $true)]
        [string]$TaskName
    )

    $delete = Invoke-SchTasks /Delete /TN $TaskName /F
    if ($delete.ExitCode -eq 0) { return $true }

    # Delete failed. If the task is really gone now, treat that as success;
    # otherwise the delete genuinely failed and the caller must hear about it.
    if (-not (Test-SchTasksTaskListed -TaskName $TaskName)) { return $true }
    return $false
}
