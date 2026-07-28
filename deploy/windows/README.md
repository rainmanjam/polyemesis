# Running polyemesis as a Windows service

> **None of this has ever been run on Windows.**
>
> The service wrapper, the process-group teardown and these scripts were all
> written and cross-compiled on macOS. No PowerShell in this directory has been
> executed, no `polyemesis.exe` has been started, and no service has been
> registered with a real Service Control Manager. Treat every command below as
> a first draft that needs a throwaway VM, not as a runbook.
>
> Two failure modes to expect first, both described in
> [`docs/INSTALL.md`](../../docs/INSTALL.md#windows): FFmpeg not being on the
> service account's `PATH`, and recordings being truncated on service stop (a
> service has no console, so the graceful-stop signal that makes FFmpeg
> finalise its file cannot be delivered — see *Stopping the service* below).
>
> If you need this working today, use Linux or Docker.

A console window you have to stay logged in to is not a deployment. These
scripts register polyemesis with the Service Control Manager so it starts at
boot, restarts on failure, and logs somewhere you can read after the fact.

```powershell
# from an elevated PowerShell prompt
.\install.ps1 -BinaryPath .\polyemesis.exe
```

Defaults:

| | |
|---|---|
| Binary | `C:\Program Files\polyemesis\polyemesis.exe` |
| Data | `C:\ProgramData\polyemesis` |
| Config | `C:\ProgramData\polyemesis\config.yaml` |
| Service account | `LocalSystem` |
| Startup | Automatic (delayed) |
| Web UI | `http://localhost:8080` |

The data directory belongs under `ProgramData`, not under `Program Files`.
`Program Files` is read-only for services by design, and this directory is
written to constantly — the SQLite database, recordings, the secret box and any
generated TLS material all live there.

## The service account

`LocalSystem` is the default because it needs no setup: it can write anywhere
and bind any port. It is also far more privileged than polyemesis needs.

To run as something smaller, pass `-ServiceAccount`. Whatever you choose has to
have all three of these, or the service will fail to start — or worse, start and
then fail the first time it tries to write a recording:

1. **The "Log on as a service" right.** Grant it under
   `secpol.msc` → Local Policies → User Rights Assignment → *Log on as a
   service*. Built-in accounts (`LocalService`, `NetworkService`) and virtual
   service accounts (`NT SERVICE\polyemesis`) already have it.
2. **Modify on the data directory.** `install.ps1` runs `icacls` to grant this
   for any non-`LocalSystem` account, and warns if it could not. Read access
   alone is not enough: the database, recordings and TLS material are all
   written under it.
3. **The right to bind the listening ports.** Any service account can bind a
   port above 1024. Ports below 1024 are *not* privileged on Windows the way
   they are on Unix, but they are frequently already claimed — port 80 in
   particular is often held by `http.sys` (IIS, WinRM, or anything else using
   the HTTP Server API). If polyemesis terminates TLS itself it also tries to
   bind `:80` for the HTTP→HTTPS redirect and, in `acme` mode, for the
   Let's Encrypt HTTP-01 challenge. That bind failing is logged as a warning and
   HTTPS keeps serving, but ACME issuance will never complete until port 80
   actually reaches this host.

Outbound connections to your streaming destinations need nothing special;
Windows permits outbound by default.

## Firewall

`install.ps1` adds two inbound rules scoped to the polyemesis executable:

- TCP `-WebPort` (default 8080) for the web UI and API.
- `-IngestProtocol` `-IngestPort` (default TCP 1935) for ingest.

RTMP is TCP; **SRT is UDP**. If you switch the ingest mode in the UI, the
firewall rule does not follow — re-run with `-IngestProtocol UDP -IngestPort
<port>`, or pass `-SkipFirewall` and manage the rules yourself.

## FFmpeg and PATH

A service does not inherit the `PATH` from your interactive shell. If FFmpeg is
only on your user `PATH`, the service will fail to start with a detection error
even though `ffmpeg -version` works fine in your terminal. Either put FFmpeg on
the *system* `PATH` or, better, set an absolute path in `config.yaml`:

```yaml
ffmpeg:
  binary: C:\ffmpeg\bin\ffmpeg.exe
  probe:  C:\ffmpeg\bin\ffprobe.exe
```

## Logs

A service has no stderr, so polyemesis writes to the Windows Event Log instead:

```powershell
Get-EventLog -LogName Application -Source polyemesis -Newest 40
```

Entries render with an "the description for Event ID … cannot be found"
preamble. That is expected — polyemesis ships no compiled message resource, and
the message text itself appears verbatim underneath.

## Stopping the service

On Stop, polyemesis drains the HTTP listener and *then* tears the FFmpeg
children down in order. The service reports `STOP_PENDING` with a wait hint
while it does that, so a `Stop-Service` or a Services-console stop will wait
for it.

**Known limitation: a service stop truncates in-progress recordings.** The
graceful stop is a `CTRL_BREAK_EVENT`, which is what makes FFmpeg flush and
write out its container index — and Windows delivers that only through a
console. A service has none, so the signal fails and the supervisor escalates
straight to terminating the process. An in-progress recording is left
unfinalised, and destinations drop rather than disconnecting politely.

Until that is fixed, if you record on Windows, stop the recording from the web
UI and let it finalise *before* stopping the service. Running interactively
from a console (see below) is not affected.

The fix is to allocate a console for the service process at startup; it is not
implemented because it could not be tested. Tracked in the report accompanying
this work.

**A machine shutdown or reboot will not.** Windows caps every service's
shutdown at `WaitToKillServiceTimeout` (5 seconds by default), regardless of
the wait hint. If you record and you reboot the host while recording, raise it:

```powershell
Set-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control' `
    -Name 'WaitToKillServiceTimeout' -Value '45000'
```

The value is a *string* of milliseconds, and it is machine-wide — it delays
every service on the box, so do not set it to something absurd. A reboot is
required for it to take effect.

## Slow first start

FFmpeg detection shells out to the binary and parses its banner. On a cold
filesystem, or with a virus scanner in the way, that can take several seconds.
The service reports `START_PENDING` with a rising checkpoint throughout, so the
SCM waits rather than declaring the start hung. You should not need to touch
`ServicesPipeTimeout`.

## Recovery

Installed recovery actions restart the service after 5 s, then 30 s, then 60 s,
with the failure count resetting daily. The backoff is deliberate: a missing
FFmpeg binary fails identically every time, and a tight restart loop only fills
the event log.

## Upgrading

```powershell
.\uninstall.ps1                       # keeps C:\ProgramData\polyemesis
.\install.ps1 -BinaryPath .\polyemesis.exe
```

`uninstall.ps1` never touches the data directory unless you pass `-RemoveData`.

## Running interactively

Nothing here changes the console experience. Launched from a terminal,
polyemesis detects it is not running under the SCM and behaves exactly as
before, logging to stderr and stopping on Ctrl-C.
