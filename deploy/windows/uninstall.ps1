<#
.SYNOPSIS
    Removes the polyemesis Windows service.

.DESCRIPTION
    Stops the service, waits for it to drain, deletes it, and removes the Event
    Log source, firewall rules and the installed binary.

    The data directory is KEPT unless you pass -RemoveData. It holds the
    database, your recordings, secrets and generated TLS material, and there is
    no undo.

    Run from an elevated PowerShell prompt:

        .\uninstall.ps1

.PARAMETER RemoveData
    Also delete the data directory. Destructive.

.PARAMETER DataDir
    Only consulted when -RemoveData is given.

.PARAMETER InstallDir
    Directory the binary was installed into.

.PARAMETER StopTimeoutSeconds
    How long to wait for the service to stop. polyemesis drains its HTTP
    listener and then finalises in-progress recordings; cutting that short is
    how you get a truncated file.
#>
[CmdletBinding()]
param(
    [switch]$Force,
    [switch]$RemoveData,
    [string]$DataDir = (Join-Path $env:ProgramData 'polyemesis'),
    [string]$InstallDir = (Join-Path $env:ProgramFiles 'polyemesis'),
    [int]$StopTimeoutSeconds = 90
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$ServiceName = 'polyemesis'

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this from an elevated PowerShell prompt (Run as Administrator).'
    }
}

Assert-Administrator

# IS ANYTHING ON AIR? Stopping the service ends every live broadcast this host
# is carrying, and a completed broadcast cannot be returned to. The Linux
# uninstaller refuses the same way; this is the Windows half of that device.
function Get-PublishingFfmpeg {
    try {
        # @() then the unary comma, so this ALWAYS returns an array -- empty for
        # "nothing is publishing", never $null.
        #
        # Written as a bare pipeline first, and that was a bug (#509): a pipeline
        # matching nothing emits nothing, so PowerShell collapsed it to $null --
        # the same value the catch below returns for "could not check". The
        # caller tests `$null -eq $live` to mean "cannot determine", so on an
        # IDLE host, which is the ordinary case, the uninstaller threw "Cannot
        # determine whether a broadcast is live" and the confirmation below was
        # unreachable. Fail-closed, so nothing was destroyed, but every ordinary
        # uninstall refused with a message about the wrong thing.
        $found = @(Get-CimInstance Win32_Process -Filter "Name='ffmpeg.exe'" -ErrorAction Stop |
            Where-Object { $_.CommandLine -and ($_.CommandLine -match 'rtmp:|srt:|-f\s+flv') })
        return ,$found
    } catch {
        # If the process table cannot be read we cannot prove the host is idle.
        # Say so rather than returning "nothing found", which would read as safe.
        Write-Warning "Could not inspect running processes: $($_.Exception.Message)"
        return $null
    }
}

if (-not $Force) {
    $live = Get-PublishingFfmpeg
    if ($null -eq $live) {
        throw 'Cannot determine whether a broadcast is live. Re-run with -Force if you mean to stop the service anyway.'
    }
    if ($live.Count -gt 0) {
        foreach ($p in $live) { Write-Host ("    pid {0}: {1}" -f $p.ProcessId, ($p.CommandLine -replace '^.*?((rtmp|srt)://\S{0,40}).*$','$1')) }
        throw "REFUSING: $ServiceName is publishing right now (listed above). Uninstalling stops it, and a live broadcast that ends cannot be resumed. Stop the destinations first, or pass -Force if you mean to end them."
    }

    $target = if ($RemoveData) { "$InstallDir and $DataDir" } else { $InstallDir }
    $answer = Read-Host "This removes the $ServiceName service and $target. Type the service name ($ServiceName) to confirm"
    if ($answer -ne $ServiceName) {
        throw 'Not confirmed; nothing was changed.'
    }
}

$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($service) {
    if ($service.Status -ne 'Stopped') {
        Write-Host "Stopping $ServiceName (waiting up to $StopTimeoutSeconds s for recordings to finalise)"
        Stop-Service -Name $ServiceName -ErrorAction SilentlyContinue
        try {
            $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds($StopTimeoutSeconds))
        } catch {
            Write-Warning "$ServiceName did not stop within $StopTimeoutSeconds s. Deleting it anyway; check the Application event log."
        }
    }

    & sc.exe delete $ServiceName | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "sc.exe delete $ServiceName failed with exit $LASTEXITCODE"
    }
    Write-Host "  service removed"
} else {
    Write-Host "  no '$ServiceName' service registered"
}

foreach ($rule in @('polyemesis-web', 'polyemesis-ingest')) {
    if (Get-NetFirewallRule -Name $rule -ErrorAction SilentlyContinue) {
        Remove-NetFirewallRule -Name $rule
        Write-Host "  firewall rule $rule removed"
    }
}

if ([System.Diagnostics.EventLog]::SourceExists($ServiceName)) {
    Remove-EventLog -Source $ServiceName
    Write-Host "  event log source removed"
}

if (Test-Path -LiteralPath $InstallDir) {
    # The SCM releases the image only once the process has exited; a delete
    # immediately after a stop can still hit a sharing violation.
    for ($i = 0; $i -lt 10; $i++) {
        try {
            Remove-Item -LiteralPath $InstallDir -Recurse -Force
            break
        } catch {
            Start-Sleep -Seconds 1
        }
    }
    if (Test-Path -LiteralPath $InstallDir) {
        Write-Warning "Could not remove $InstallDir; the file is still locked. Delete it manually after a reboot."
    } else {
        Write-Host "  $InstallDir removed"
    }
}

if ($RemoveData) {
    if (Test-Path -LiteralPath $DataDir) {
        Write-Warning "Deleting $DataDir — database, recordings, secrets and TLS material."
        Remove-Item -LiteralPath $DataDir -Recurse -Force
        Write-Host "  $DataDir removed"
    }
} else {
    Write-Host ''
    Write-Host "Data kept at $DataDir. Pass -RemoveData to delete it."
}
