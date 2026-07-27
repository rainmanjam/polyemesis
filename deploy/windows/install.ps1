<#
.SYNOPSIS
    Installs polyemesis as a Windows service.

.DESCRIPTION
    Copies the binary into Program Files, creates the data directory under
    ProgramData, registers the service and its Event Log source, and optionally
    opens the firewall for the web UI and the ingest port.

    Run from an elevated PowerShell prompt:

        .\install.ps1 -BinaryPath .\polyemesis.exe

    The service detects that the SCM started it and reports its state back, so
    a slow FFmpeg probe on first start is not mistaken for a hung service. On
    Stop it drains the HTTP listener and then tears the FFmpeg children down in
    order, which is what finalises an in-progress recording rather than
    truncating it. See "Shutdown timeout" in README.md — Windows caps that drain
    at five seconds during a machine shutdown unless you raise a registry value.

.PARAMETER BinaryPath
    Path to the polyemesis.exe you want installed.

.PARAMETER InstallDir
    Where the binary is copied to. Defaults to Program Files.

.PARAMETER DataDir
    Recordings, the database, secrets and generated TLS material live here. It
    belongs under ProgramData, not Program Files: Program Files is read-only for
    services by design and the data directory is written to constantly.

.PARAMETER ConfigPath
    config.yaml. Defaults to <DataDir>\config.yaml, and a missing file is left
    alone — polyemesis creates a default on first start.

.PARAMETER ServiceAccount
    Defaults to LocalSystem, which needs no ACL work and can bind any port.
    See README.md before switching to a less privileged account: it will need
    the "Log on as a service" right, Modify on DataDir, and Read on InstallDir.

.PARAMETER ServiceAccountPassword
    Required only for a domain or local user account. Leave unset for
    LocalSystem, LocalService, NetworkService and virtual accounts.

.PARAMETER WebPort
    Port the web UI listens on, used for the -addr flag and the firewall rule.

.PARAMETER IngestPort
    RTMP (or SRT) ingest port to open in the firewall. RTMP is TCP; SRT is UDP.

.PARAMETER IngestProtocol
    TCP for RTMP, UDP for SRT.

.PARAMETER SkipFirewall
    Do not create firewall rules.

.PARAMETER StartupType
    Automatic, AutomaticDelayedStart or Manual.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BinaryPath,

    [string]$InstallDir = (Join-Path $env:ProgramFiles 'polyemesis'),
    [string]$DataDir = (Join-Path $env:ProgramData 'polyemesis'),
    [string]$ConfigPath,

    [string]$ServiceAccount = 'LocalSystem',
    [string]$ServiceAccountPassword,

    [int]$WebPort = 8080,
    [int]$IngestPort = 1935,
    [ValidateSet('TCP', 'UDP')]
    [string]$IngestProtocol = 'TCP',
    [switch]$SkipFirewall,

    [ValidateSet('Automatic', 'AutomaticDelayedStart', 'Manual')]
    [string]$StartupType = 'AutomaticDelayedStart',

    [switch]$NoStart
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$ServiceName = 'polyemesis'
$DisplayName = 'polyemesis restreaming server'
$Description = 'Self-hosted restreaming server. Supervises FFmpeg and serves the web UI.'

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this from an elevated PowerShell prompt (Run as Administrator).'
    }
}

# sc.exe reports failure through $LASTEXITCODE, not by throwing, so every call
# has to be checked or a half-installed service looks like a success.
function Invoke-Sc {
    param([string[]]$Arguments, [string]$What)
    $output = & sc.exe @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "$What failed (sc.exe exit $LASTEXITCODE): $output"
    }
}

Assert-Administrator

$BinaryPath = (Resolve-Path -LiteralPath $BinaryPath).Path
if (-not $ConfigPath) { $ConfigPath = Join-Path $DataDir 'config.yaml' }

$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing) {
    throw "A service named '$ServiceName' already exists. Run uninstall.ps1 first, or use it to upgrade in place."
}

Write-Host "Installing $DisplayName"
Write-Host "  binary   $BinaryPath"
Write-Host "  install  $InstallDir"
Write-Host "  data     $DataDir"
Write-Host "  config   $ConfigPath"
Write-Host "  account  $ServiceAccount"

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
Copy-Item -LiteralPath $BinaryPath -Destination (Join-Path $InstallDir 'polyemesis.exe') -Force
$exe = Join-Path $InstallDir 'polyemesis.exe'

# A non-system account cannot write the database, recordings or TLS material
# without this. LocalSystem already has it, so granting is harmless there and
# means switching accounts later is a one-line change.
if ($ServiceAccount -notin @('LocalSystem')) {
    Write-Host "  granting Modify on $DataDir to $ServiceAccount"
    & icacls.exe $DataDir /grant "${ServiceAccount}:(OI)(CI)M" /T | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Warning "icacls could not grant $ServiceAccount write access to $DataDir. The service will fail to start until it can write there."
    }
}

# Registering the source keeps Event Viewer from prefixing every entry with a
# complaint about a missing message file. polyemesis ships no compiled .mc
# resource, so entries still render the message body verbatim.
if (-not [System.Diagnostics.EventLog]::SourceExists($ServiceName)) {
    New-EventLog -LogName 'Application' -Source $ServiceName
}

# The quoting matters: InstallDir contains a space under the default, and the
# SCM splits the image path on whitespace unless the executable is quoted.
$binaryPathName = '"{0}" --config "{1}" --data "{2}" --addr ":{3}"' -f $exe, $ConfigPath, $DataDir, $WebPort

$newServiceStartup = if ($StartupType -eq 'AutomaticDelayedStart') { 'Automatic' } else { $StartupType }
$params = @{
    Name           = $ServiceName
    BinaryPathName = $binaryPathName
    DisplayName    = $DisplayName
    Description    = $Description
    StartupType    = $newServiceStartup
}
if ($ServiceAccount -and $ServiceAccount -ne 'LocalSystem') {
    if ($ServiceAccountPassword) {
        $secure = ConvertTo-SecureString $ServiceAccountPassword -AsPlainText -Force
        $params['Credential'] = New-Object System.Management.Automation.PSCredential($ServiceAccount, $secure)
    } else {
        # Virtual and built-in accounts have no password; New-Service cannot
        # express that, so sc.exe sets the account after creation.
        $params['StartupType'] = 'Manual'
    }
}
New-Service @params | Out-Null

if ($ServiceAccount -ne 'LocalSystem' -and -not $ServiceAccountPassword) {
    Invoke-Sc -Arguments @('config', $ServiceName, "obj=$ServiceAccount") -What 'setting the service account'
    Invoke-Sc -Arguments @('config', $ServiceName, "start=$(if ($StartupType -eq 'Manual') { 'demand' } else { 'auto' })") -What 'setting the startup type'
}

# Delayed start keeps polyemesis out of the boot stampede. It talks to the
# network and shells out to FFmpeg; neither is ready at the very start of boot.
if ($StartupType -eq 'AutomaticDelayedStart') {
    Invoke-Sc -Arguments @('config', $ServiceName, 'start=delayed-auto') -What 'setting delayed autostart'
}

# Restart on crash, but back off: an FFmpeg binary that has gone missing will
# fail identically every time and a tight restart loop only fills the event log.
Invoke-Sc -Arguments @(
    'failure', $ServiceName,
    'reset=86400',
    'actions=restart/5000/restart/30000/restart/60000'
) -What 'setting recovery actions'

if (-not $SkipFirewall) {
    # Inbound rules only. The service also makes outbound connections to every
    # destination it pushes to; Windows allows outbound by default.
    $rules = @(
        @{ Name = 'polyemesis-web'; Display = 'polyemesis web UI'; Port = $WebPort; Protocol = 'TCP' },
        @{ Name = 'polyemesis-ingest'; Display = "polyemesis ingest ($IngestProtocol)"; Port = $IngestPort; Protocol = $IngestProtocol }
    )
    foreach ($rule in $rules) {
        if (Get-NetFirewallRule -Name $rule.Name -ErrorAction SilentlyContinue) {
            Remove-NetFirewallRule -Name $rule.Name
        }
        New-NetFirewallRule -Name $rule.Name -DisplayName $rule.Display `
            -Direction Inbound -Action Allow -Protocol $rule.Protocol `
            -LocalPort $rule.Port -Program $exe -Profile Any | Out-Null
        Write-Host ("  firewall {0}/{1} allowed" -f $rule.Protocol, $rule.Port)
    }
}

if (-not $NoStart) {
    Write-Host 'Starting the service'
    Start-Service -Name $ServiceName
    (Get-Service -Name $ServiceName).WaitForStatus('Running', [TimeSpan]::FromSeconds(60))
}

Write-Host ''
Write-Host "Installed. Web UI: http://localhost:$WebPort"
Write-Host "Logs:  Get-EventLog -LogName Application -Source $ServiceName -Newest 40"
Write-Host "State: Get-Service $ServiceName"
Write-Host ''
Write-Host 'If the service fails to start, the reason is in the Application event log.'
Write-Host 'The usual cause is FFmpeg not being on the service account PATH — set'
Write-Host 'ffmpeg.binary to an absolute path in config.yaml. A service does not'
Write-Host 'inherit the PATH from your interactive shell.'
