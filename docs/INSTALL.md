# Installing polyemesis

One binary, one dependency. The binary is self-contained — the React UI is
embedded, SQLite is pure Go, there is no cgo and no runtime library to install.
The dependency is **FFmpeg 6.0 or newer**, and it is where essentially every
installation problem comes from. Read the FFmpeg part of your platform's
section even if you skip the rest.

- [Platform maturity](#platform-maturity)
- [The FFmpeg requirement](#the-ffmpeg-requirement)
- [Docker](#docker)
- [Linux (bare metal or VM)](#linux-bare-metal-or-vm)
- [macOS](#macos)
- [Windows](#windows)
- [Verifying the install](#verifying-the-install)
- [Ports](#ports)

---

## Platform maturity

These are not four equal targets, and pretending otherwise would cost you a
weekend.

| Platform | Status |
|---|---|
| **Linux (server)** | Primary target. Where it is developed against, deployed and exercised. |
| **Docker** | Primary target. The image is built from this repo and bundles a pinned FFmpeg. |
| **macOS** | Developed on daily. Fine as a workstation and test rig. Homebrew's FFmpeg has no SRT — see below. |
| **Windows** | **Implemented but not executed on Windows.** It compiles, the service wrapper and process-group teardown are written, and the installer scripts exist — but no one has run the resulting binary on a Windows host. Treat it as untested. |

If you are choosing where to run this, run it on Linux.

---

## The FFmpeg requirement

Two separate checks, and they fail differently:

**Version — 6.0 or newer, enforced.** polyemesis probes `ffmpeg -version` before
it opens the database and **refuses to start** on anything older, with a message
naming the binary and the version it found. The floor is 6.0 because multi-track
MPEG-TS mapping, the channel-layout API behind the audio mix matrix, and the
`-progress` fields the supervisor parses are only reliable from 6.x onwards.

**SRT — needed for multitrack ingest, not enforced.** A build without libsrt
starts fine and warns. You can reach the UI and switch ingest to RTMP, but RTMP
carries a single stereo pair, so per-destination audio routing has nothing to
route from. SRT is what makes this product work.

Check both, on every platform:

```bash
ffmpeg -version | head -1
ffmpeg -protocols | tr ' ' '\n' | grep -x srt      # must print: srt
```

> Use the exact match. `ffmpeg -protocols | grep srt` is **misleading** —
> every build lists `srtp` (Secure RTP), a different protocol that happens to
> contain the substring.

---

## Docker

The least that can go wrong: the image bundles a pinned FFmpeg with SRT, so
nothing on the host matters except Docker itself.

**Prerequisites.** Docker Engine 20.10+ (or Docker Desktop). Nothing else — no
Go, no Node, no FFmpeg on the host.

**FFmpeg.** Pinned in the `Dockerfile` to a specific Alpine package version, not
floating. `apk add ffmpeg` would resolve to whatever the branch holds on the day
you build, which makes a rebuild months later a different product under the same
tag — and the thing that drifts is the transcoder, where a behaviour change
surfaces as a broken stream rather than a build error. The Dockerfile documents
how to bump it deliberately, and fails the build if the resulting FFmpeg has no
SRT.

**Install.**

```bash
git clone https://github.com/rainmanjam/polyemesis
cd polyemesis
docker compose up -d
docker compose logs -f
```

Open <http://localhost:8080> and set an admin password on the first-run screen.

**What the compose file publishes**, and why each one:

| Port | Why |
|---|---|
| `8080/tcp` | web UI and API |
| `6000/udp` | SRT ingest — **UDP**. Omitting `/udp` is the classic reason an SRT ingest silently receives nothing. |
| `1935/tcp` | RTMP ingest, the fallback for encoders that cannot do SRT |
| `80/tcp` | **ACME only**, and shipped commented out — see below |

**Port 80 and Let's Encrypt.** If you want polyemesis to obtain its own
certificate (`tls.mode: acme`, or `auto` resolving to it), HTTP-01 validation
must reach `http://<hostname>/.well-known/acme-challenge/…` on port 80 from the
public internet. That needs the port *published on the host*, not merely
`EXPOSE`d in the image. Uncomment both the `- "80:80"` line and the config mount
in `docker-compose.yml`, then put your TLS block in `config.yaml`.

It ships commented out because publishing `:80` unconditionally makes
`docker compose up` fail outright on any host that already runs a web server,
and the default configuration is plain HTTP with no certificate at all — so the
port would buy nothing and cost a bind error. No other TLS mode needs it.

The container runs as UID 10001, not root, which normally raises the question of
whether it can bind a port below 1024 at all. It can: Docker sets
`net.ipv4.ip_unprivileged_port_start=0` inside containers, so no added
capability and no `user: root` is required. (Checked against Docker Engine 29.)

**Data.** Everything that must survive a container replacement lives in the
`polyemesis-data` volume mounted at `/data`: the SQLite database, `secret.key`
(which decrypts your stored OAuth tokens), recordings, and `tls/` (the local CA
your browsers have been told to trust, plus the ACME cache that keeps a redeploy
from re-issuing against Let's Encrypt's rate limits). Back it up.

**As a service.** `restart: unless-stopped` is already in the compose file; the
Docker daemon's own startup handles boot.

---

## Linux (bare metal or VM)

### Read this table first

polyemesis needs FFmpeg 6.0+. Several current server distributions ship less
than that, and `apt install ffmpeg` on them produces a binary polyemesis will
refuse to start against.

| Distribution | Stock FFmpeg | polyemesis |
|---|---|---|
| Ubuntu 22.04 LTS (jammy) | **4.4.2** | **refuses to start** |
| Debian 12 (bookworm) | **5.1.9** | **refuses to start** |
| Ubuntu 24.04 LTS (noble) | 6.1.1 | works |
| Debian 13 (trixie) | 7.1.5 | works |
| Alpine 3.20 / 3.21 / 3.22 | 6.1.1 / 6.1.2 / 6.1.2 | works |
| Alpine 3.23 / 3.24 | 8.0.1 / 8.1.2 | meets the floor; FFmpeg 8.x is not yet exercised here |
| RHEL / Rocky / AlmaLinux | not in the base repositories at all | see below |
| Fedora, Arch, openSUSE | not checked | verify for your release |

Checked against the distro package indexes on 2026-07-26 (`packages.ubuntu.com`,
`packages.debian.org`, `pkgs.alpinelinux.org`). Versions move; if your release
is not in this table, or you are reading this much later, run
`apt-cache policy ffmpeg` (or your equivalent) and check the number yourself
rather than trusting the row.

On RHEL and its rebuilds FFmpeg is not shipped at all for licensing reasons —
RPM Fusion is the usual source. Verify the version it gives you before relying
on it.

### If your distro is too old, pick one of three

**1. A newer distro.** Ubuntu 24.04 LTS or Debian 13 both clear the floor from
the stock repositories, with libsrt included. This is the least ongoing work.

**2. A static FFmpeg build.** Does not touch your system packages and does not
need a distro upgrade. [BtbN's builds](https://github.com/BtbN/FFmpeg-Builds/releases)
include libsrt:

```bash
curl -fsSL -o ffmpeg.tar.xz \
  https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-n7.1-latest-linux64-gpl-7.1.tar.xz
tar xf ffmpeg.tar.xz
sudo install -m755 ffmpeg-*/bin/ffmpeg ffmpeg-*/bin/ffprobe /usr/local/bin/

/usr/local/bin/ffmpeg -protocols | tr ' ' '\n' | grep -x srt
```

Verified 2026-07-26: `ffmpeg-n7.1-latest-linux64-gpl-7.1` reports version
`n7.1.5` and lists `srt`. These are glibc, x86-64 builds — they will not run on
Alpine or musl, and `linux64` means amd64, not arm64.

If you would rather not put them on `PATH`, point the config at them instead:

```yaml
ffmpeg:
  binary: /opt/ffmpeg/bin/ffmpeg
  probe:  /opt/ffmpeg/bin/ffprobe
```

Note that [John Van Sickle's static builds](https://johnvansickle.com/ffmpeg/),
the other well-known set, do **not** advertise libsrt in their published
configuration. If you use them, run the protocol check before you rely on
multitrack ingest.

**3. Docker.** See the section above. On an old host this is often the shortest
path, since the FFmpeg problem stops being a host problem.

### Install the binary

Prerequisites for building from source: **Go 1.26.5+** (the floor in `go.mod`;
the official Go images set `GOTOOLCHAIN=local`, so an older toolchain fails
rather than silently upgrading itself) and **Node 20.19+ or 22.12+** (Vite 8's
requirement). Neither is needed to *run* the result.

```bash
git clone https://github.com/rainmanjam/polyemesis
cd polyemesis
make build                 # builds the UI, embeds it, produces ./polyemesis
./polyemesis -data ./data
```

Open <http://localhost:8080> and set an admin password.

### Run it as a service

A hardened systemd unit ships in
[`deploy/polyemesis.service`](../deploy/polyemesis.service):

```bash
sudo cp polyemesis /usr/local/bin/
sudo useradd --system --home /var/lib/polyemesis --shell /usr/sbin/nologin polyemesis
sudo mkdir -p /var/lib/polyemesis /etc/polyemesis
sudo chown polyemesis:polyemesis /var/lib/polyemesis
sudo cp config.example.yaml /etc/polyemesis/config.yaml
sudo cp deploy/polyemesis.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now polyemesis
journalctl -u polyemesis -f
```

Two details in that unit are load-bearing:

- **`KillMode=mixed` with a 45 s stop timeout.** polyemesis signals its FFmpeg
  children and gives them time to finish. On SIGTERM, FFmpeg flushes and
  finalises its output — that is the difference between a playable recording and
  a truncated one, and between a clean disconnect and a platform that thinks you
  dropped. Letting systemd kill the whole cgroup immediately cuts a recording off
  mid-write.
- **No `CAP_NET_BIND_SERVICE` by default.** The service runs unprivileged, so it
  cannot bind ports below 1024. With `tls.mode: auto` resolving to `selfsigned`
  that is harmless — you get a warning that `:80` could not be bound for the
  HTTP→HTTPS redirect and HTTPS serves normally. With `mode: acme` it is not
  harmless: issuance will never complete. Uncomment the `AmbientCapabilities`
  block in the unit file.

### Behind a reverse proxy

If nginx, Caddy or Traefik already terminates TLS, set `trustProxyHeaders: true`
and leave `tls.mode: auto` — it deliberately resolves to `off`, so polyemesis
does not bind `:80` and does not compete with the proxy for ACME challenges.
There is a worked config in
[`deploy/nginx.conf.example`](../deploy/nginx.conf.example).

SRT and RTMP ingest are **not HTTP** and cannot be proxied by nginx's HTTP
server. Open those ports on the firewall so encoders reach polyemesis directly.

---

## macOS

Developed on daily, so it works — but read the SRT paragraph, because the
default Homebrew install cannot do multitrack ingest.

**Prerequisites.** Homebrew. Go 1.26.5+ and Node 20.19+/22.12+ if building from
source. Apple Silicon and Intel both fine.

### FFmpeg on macOS: the version is fine, SRT is not

`brew install ffmpeg` gives you a current FFmpeg — 8.1.2 as of 2026-07-26, well
past the 6.0 floor. But the Homebrew bottle is **built without libsrt**. Its
formula lists `dav1d, lame, libvmaf, libvpx, openssl@3, opus, sdl2-compat,
svt-av1, x264, x265` as dependencies; `srt` is not among them, and the protocol
check above prints nothing on a stock Homebrew install. (Verified against a live
Homebrew install on 2026-07-26, not inferred from the formula alone.)

polyemesis will start and warn. Ingest will work over RTMP only, which means one
stereo pair and nothing for the mix matrix to route.

Three ways out:

**1. The homebrew-ffmpeg tap, with SRT.** Builds from source, so budget time:

```bash
brew uninstall ffmpeg                     # the core formula, if installed
brew tap homebrew-ffmpeg/ffmpeg
brew install homebrew-ffmpeg/ffmpeg/ffmpeg --with-srt
ffmpeg -protocols | tr ' ' '\n' | grep -x srt
```

The tap's formula exposes `option "with-srt"` and passes `--enable-libsrt`.

**2. Docker Desktop.** The image bundles an FFmpeg with SRT, so the host's
FFmpeg stops mattering. Note that SRT ingest arrives over UDP into a VM here —
fine for development, not what you would ship.

**3. Build FFmpeg yourself** with `--enable-libsrt`.

### Install

```bash
git clone https://github.com/rainmanjam/polyemesis
cd polyemesis
make build
./polyemesis -data ./data
```

### Run it at login, or at boot

launchd. For a per-user agent that starts at login, write
`~/Library/LaunchAgents/dev.polyemesis.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>            <string>dev.polyemesis</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/polyemesis</string>
    <string>-config</string>
    <string>/Users/YOU/Library/Application Support/polyemesis/config.yaml</string>
    <string>-data</string>
    <string>/Users/YOU/Library/Application Support/polyemesis</string>
  </array>
  <key>RunAtLoad</key>        <true/>
  <key>KeepAlive</key>        <true/>
  <!-- Homebrew is not on a launchd job's default PATH, and polyemesis looks
       for ffmpeg there. Without this it exits at startup saying ffmpeg is
       missing, on a machine where ffmpeg plainly works in your shell. -->
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
  </dict>
  <key>StandardOutPath</key>  <string>/tmp/polyemesis.log</string>
  <key>StandardErrorPath</key><string>/tmp/polyemesis.log</string>
</dict>
</plist>
```

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.polyemesis.plist
launchctl print gui/$(id -u)/dev.polyemesis
```

A LaunchAgent stops when you log out. For a machine that should stream whether
or not anyone is logged in, put the same plist in
`/Library/LaunchDaemons/`, own it `root:wheel` with mode `644`, and load it with
`sudo launchctl bootstrap system …` — and give the daemon a data directory it
can actually write, not one under a user's home.

macOS will ask for a firewall exception the first time polyemesis binds its
ingest ports. Approve it, or nothing external reaches the encoder.

---

## Windows

**Read this first.** Windows support is *implemented but has not been executed
on Windows*. The binary cross-compiles, the Service Control Manager wrapper is
written, process-group teardown and the disk-full guard have Windows
implementations, and the installer scripts exist — but nobody has run the result
on a Windows host. Nothing here is verified end to end. If you need this to work
today, use Linux or Docker.

**Prerequisites.** Windows 10 / Server 2019 or newer, x86-64. Go 1.26.5+ and
Node 20.19+/22.12+ if you are building the binary yourself.

### FFmpeg on Windows: this part is straightforward

Unlike macOS, the mainstream Windows builds **do** include libsrt:

- **[gyan.dev](https://www.gyan.dev/ffmpeg/builds/)** — libsrt is in the
  *essentials* build, so you do not need the much larger *full* one. Also
  available as `winget install Gyan.FFmpeg`.
- **[BtbN](https://github.com/BtbN/FFmpeg-Builds/releases)** — take a
  `win64-gpl` asset. libsrt is part of the build recipe.

Unzip somewhere stable, add the `bin` directory to `PATH` (or point
`ffmpeg.binary` and `ffmpeg.probe` at it in `config.yaml`), then verify in
PowerShell:

```powershell
ffmpeg -version | Select-Object -First 1
(ffmpeg -protocols) -split '\s+' -contains 'srt'    # must be True
```

Avoid the Microsoft Store and Chocolatey's `ffmpeg-shared` unless you have run
that protocol check against them — an FFmpeg without SRT is the single most
likely reason multitrack ingest fails on a fresh Windows box.

### Build the binary

`make release` does not currently emit a Windows target, so build it directly.
From a checkout, with the UI built first so `go:embed` picks up the real assets
rather than the placeholder:

```bash
make ui
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o polyemesis.exe ./cmd/polyemesis
```

That works from Linux or macOS just as well as from Windows — there is no cgo.

### Register it with the Service Control Manager

A console window you have to stay logged in to is not a deployment. The
installer scripts and the full account, firewall and troubleshooting notes live
in [`deploy/windows/`](../deploy/windows/):

```powershell
# elevated PowerShell
.\install.ps1 -BinaryPath .\polyemesis.exe
```

Read [`deploy/windows/README.md`](../deploy/windows/README.md) before running
it — particularly the service-account section, because an account that lacks
*Modify* on the data directory produces a service that starts cleanly and then
fails the first time it writes a recording.

Three Windows-specific things worth knowing up front:

- **A service stop truncates an in-progress recording.** The graceful stop is a
  `CTRL_BREAK_EVENT`, and Windows delivers those only through a console, which a
  service does not have. The supervisor therefore terminates FFmpeg outright and
  the container is never finalised. Stop the recording from the web UI and let
  it finalise before stopping the service. Running interactively from a console
  is not affected.
- **The data directory belongs under `C:\ProgramData`, not `Program Files`.**
  `Program Files` is read-only to services by design, and this directory is
  written to constantly.
- **Port 80 is often already taken by `http.sys`** (IIS, WinRM, or anything
  else using the HTTP Server API). Ports below 1024 are not privileged on
  Windows the way they are on Unix, but they are frequently claimed. If
  polyemesis terminates TLS it also tries to bind `:80`; that failing is a
  warning and HTTPS keeps serving, but ACME issuance will never complete.

---

## Verifying the install

Same on every platform.

```bash
curl -s http://localhost:8080/api/v1/health
```

The health endpoint is unauthenticated on purpose, so it works before you have
signed in and from a container healthcheck.

Then confirm in the UI what polyemesis actually detected. *Settings → Ingest*
carries three badges: the FFmpeg version, `srt yes/no`, and `x264 yes/no`. If
`srt` reads `no`, no amount of OBS configuration will fix it — go back to your
platform's FFmpeg section.

The startup log carries the same facts:

```text
ffmpeg detected version=… path=… srt=true
tls mode=… hostname=…
```

---

## Ports

| Port | Protocol | Needed when |
|---|---|---|
| 8080 | TCP | always — web UI and API. Configurable via `addr`. |
| 6000 | **UDP** | SRT ingest. The default; changeable in *Settings → Ingest*. |
| 1935 | TCP | RTMP ingest, only if you use the fallback |
| 80 | TCP | only for `tls.mode: acme` (HTTP-01 validation), plus the HTTP→HTTPS redirect whenever polyemesis terminates TLS |
| 443 | TCP | only if you set `addr` to `:443` rather than serving TLS on 8080 |

The SRT ingest listener binds `0.0.0.0` regardless of `addr`, so restricting
`addr` to loopback for a reverse-proxy deployment does not restrict ingest. Set
an SRT passphrase in *Settings → Ingest*: without one your stream crosses the
network in the clear.

---

Configuration reference: `config.example.yaml`.
TLS modes and what `auto` resolves to: the *TLS and certificates* section of
[`README.md`](../README.md).
Architecture: [`ARCHITECTURE.md`](ARCHITECTURE.md).
Testing without OBS: [`TESTING.md`](TESTING.md).
