# polyemesis

[![ci](https://github.com/rainmanjam/polyemesis/actions/workflows/ci.yml/badge.svg)](https://github.com/rainmanjam/polyemesis/actions/workflows/ci.yml)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Docker](https://img.shields.io/badge/docker-rainmanjam%2Fpolyemesis-2496ED?logo=docker&logoColor=white)](https://hub.docker.com/r/rainmanjam/polyemesis)
[![Licence: MIT](https://img.shields.io/badge/licence-MIT-green.svg)](LICENSE)

A self-hosted restreaming server with **per-destination multichannel audio
routing**.

You stream once. polyemesis fans it out to as many destinations as you like —
and each destination gets its *own* audio mix, chosen from the audio tracks your
encoder sent. Video is copied through untouched.

```
                                          ┌── YouTube    tracks 1+2   (no music)
OBS ──SRT, 6 audio tracks──► polyemesis ──┼── Twitch     tracks 1+2+3
                                          ├── Kick       tracks 1+2+3+4
                                          └── local file all tracks, unencoded
```

## The problem it solves

You run OBS with several audio tracks — track 1 is the full mix including
copyrighted music, track 2 is the clean mix without it, track 3 is your mic.
Every live platform accepts exactly **one stereo audio stream**, so normally you
must choose one mix for everyone, or run several encoders and several uploads.

polyemesis ingests once and lets you pick, per destination, which tracks get
summed into the stereo feed that destination receives. YouTube gets the clean
mix; your local archive keeps everything; your Discord restream keeps the mic
hot. One upload, one video encode, different audio per platform.

## What it does

- **Per-destination audio routing** — the differentiator. Checkbox-per-track
  simple mode, or a full channel-to-output mix matrix with a gain per cell.
  [→ AUDIO-ROUTING.md](docs/AUDIO-ROUTING.md)
- **Video is copied, not re-encoded.** `-c:v copy` on every destination, so CPU
  cost is nearly independent of resolution. Only audio is decoded, mixed and
  re-encoded to AAC.
- **Shared video renditions** for platforms that will not take your source: one
  encode feeds every destination that selects it, ref-counted so an unused tier
  costs nothing — and it re-encodes **video only**, so audio still routes per
  destination. [→ RENDITIONS.md](docs/RENDITIONS.md)
- **SRT multitrack ingest** (up to 6 AAC tracks), addressed by token on a single
  port, with RTMP as a single-track fallback. [→ OBS.md](docs/OBS.md)
- **Live audio meters** for every channel of every track — how you verify the
  clean track really is clean, *before* going live.
- **Unlimited destinations**: RTMP(S), SRT, or local file, each independently
  startable with auto-reconnect and exponential backoff.
- **Platform sign-in** for YouTube, Twitch, Facebook and Kick, with chat and
  metadata push where the API allows it. [→ PLATFORMS.md](docs/PLATFORMS.md)
- **Failover** with a standby ingest, a generated slate, and a source selector
  that switches without restarting a single destination.
- **Post-production**: segmented multitrack recording with retention, a job
  queue, Whisper transcription with full-text search, and a clipper.
- **Prometheus metrics** and in-process alert rules with webhook delivery.
  [→ MONITORING.md](docs/MONITORING.md)
- **TLS that configures itself** — Let's Encrypt when the box has a public name,
  a local CA when it does not, and out of the way when a proxy already
  terminates it. [→ TLS.md](docs/TLS.md)
- **Single static binary.** UI embedded, pure-Go SQLite, no cgo, no runtime
  dependency except FFmpeg itself.

## What it does not do

Read this before you invest an evening. These are design decisions, not a
roadmap.

| | |
|---|---|
| **No multitrack over RTMP.** | RTMP carries one stereo pair. Enhanced RTMP multitrack from OBS 30.2+ is **not** supported, and the `enhancedRtmp` config key is an inert placeholder. Multitrack means SRT. |
| **One RTMP source, maximum.** | polyemesis has no RTMP server of its own — it uses `ffmpeg -listen 1`, which cannot demultiplex by path. Any number of sources can share the SRT port; only one can use RTMP. [Why](docs/DESIGN-ONE-PORT-ONLY.md#rtmp) |
| **No per-source ports.** | Every push source is addressed by its token on one SRT port. Giving one programme its own port for firewall, NIC or QoS purposes is not something polyemesis does. [Why](docs/DESIGN-ONE-PORT-ONLY.md#what-it-costs) |
| **One user, no roles.** | No multi-user model, no per-destination permissions. Access to the UI is full control of the server's streaming — and, through file destinations and expert mode, meaningful control of the machine. |
| **Instagram Live cannot work.** | There is no Live broadcast API, and Live Producer's RTMP path was removed for most accounts. It is listed and marked unsupported rather than shipped as a preset that quietly never connects. |
| **Your source video is your problem.** | Renditions can re-encode down, but polyemesis will not rescue a 4K60 ingest on a box that cannot software-encode it in realtime. |

### Platform maturity

Four targets, not four equal targets:

| Platform | Status |
|---|---|
| **Linux (server)** | Primary target. Developed against, deployed, exercised. |
| **Docker** | Primary target. Image built from this repo, FFmpeg pinned and bundled. |
| **macOS** | Developed on daily. Good workstation and test rig. Homebrew's FFmpeg has no SRT — [see INSTALL.md](docs/INSTALL.md#ffmpeg-on-macos-the-version-is-fine-srt-is-not). |
| **Windows** | **Implemented but never executed on Windows.** It compiles, the service wrapper and process-group teardown are written, the installer scripts exist — but nobody has run the binary on a Windows host. Treat it as untested. |

### Project status

**Pre-release.** No version has been tagged. The test suites are extensive and
green, and the software has been run in earnest — but it has one maintainer, no
production installs beyond that, and no compatibility promises yet. The
[changelog](CHANGELOG.md) records what a first release will contain.

## How it compares

| | polyemesis | datarhei/Restreamer | restream.io |
|---|---|---|---|
| Per-destination audio mix | **Yes** — the point of it | No | No |
| Video re-encoded per destination | No (`-c:v copy`) | Optional | Yes, server-side |
| Self-hosted | Yes | Yes | No |
| Cost | Your hardware | Your hardware | Subscription |
| Maturity | Pre-release | Established | Commercial product |

The honest summary: if you do not need different audio per platform, Restreamer
is a more mature product and you should probably use it. polyemesis exists for
the case where one stereo pair for everybody is the thing that does not work.

More detail in
[docs/RESEARCH-COMPETITIVE.md](docs/RESEARCH-COMPETITIVE.md).

## Try it

The fastest path, with FFmpeg and SRT already bundled:

```bash
docker run -d --name polyemesis \
  -p 8080:8080 -p 6000:6000/udp -p 1935:1935 \
  -v polyemesis-data:/data \
  rainmanjam/polyemesis:latest
```

Open <http://localhost:8080> and set an admin password on the first-run screen.

> Note the `/udp` on port 6000. SRT is UDP, and omitting the suffix is the
> classic reason an ingest silently receives nothing.

Or build from a clone — you need Go 1.26.5+, Node 20.19+/22.12+ and FFmpeg:

```bash
git clone https://github.com/rainmanjam/polyemesis
cd polyemesis
make build            # builds the UI, embeds it, produces ./polyemesis
./polyemesis -data ./data
```

Then follow [docs/QUICKSTART.md](docs/QUICKSTART.md) — first stream in about
five minutes. Per-platform installation, service files and the GPU images are in
[docs/INSTALL.md](docs/INSTALL.md).

### Requirements

**FFmpeg 6.0 or newer, with libsrt.** Two separate things, and they fail
differently: polyemesis **refuses to start** on FFmpeg older than 6.0, and
starts-with-a-warning on a build that has no SRT — leaving you RTMP, which is
one stereo pair and therefore nothing to route.

```bash
ffmpeg -version | head -1
ffmpeg -protocols | tr ' ' '\n' | grep -x srt      # must print: srt
```

Use the exact match. `grep srt` also matches `srtp`, a different protocol that
every build lists, so the loose check passes everywhere and tells you nothing.

Several current server distributions ship an FFmpeg that is too old — Ubuntu
22.04 and Debian 12 both fail the floor — so `apt install ffmpeg` is not a
universal answer, and Homebrew's macOS build has no SRT at all.
[docs/INSTALL.md](docs/INSTALL.md#the-ffmpeg-requirement) has the
per-distribution table and the ways out.

## How it works

- One supervised FFmpeg process listens for the incoming stream and republishes
  it, untouched, as MPEG-TS to an in-process Go **relay hub** on loopback UDP.
- The hub replicates every datagram to each subscriber, so destinations, the
  recorder, the HLS preview and the metering sidecar each read the full stream
  independently and can restart without disturbing the ingest.
- A rendition is that same shape one level up: a supervised FFmpeg reading the
  ingest hub, encoding video only, publishing to a **hub of its own** that its
  destinations subscribe to instead.
- Each destination is its own supervised FFmpeg process: video copied — from the
  ingest hub, or from its rendition's — audio compiled from its routing profile.
- Every child runs in its own process group and dies with the parent.

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) has the process graph, the relay
design, and the tradeoffs behind each of those choices.

## Documentation

[`docs/`](docs/) has a full [index](docs/README.md). The ones people reach for
most:

| | |
|---|---|
| [Quickstart](docs/QUICKSTART.md) | First stream in about five minutes |
| [Install](docs/INSTALL.md) | Per-platform, including a too-old FFmpeg |
| [OBS setup](docs/OBS.md) | Multitrack over SRT, step by step |
| [Audio routing](docs/AUDIO-ROUTING.md) | The mix matrix, clip protection, loudness |
| [Renditions](docs/RENDITIONS.md) | Shared video encodes and hardware encoders |
| [Platforms](docs/PLATFORMS.md) | What each platform's API allows, and OAuth setup |
| [TLS](docs/TLS.md) | Certificates, reverse proxies, and the SSH tunnel |
| [Configuration](docs/CONFIGURATION.md) | `config.yaml` vs the UI, and which is which |
| [API](docs/API.md) | Every route, with authentication and a worked example |
| [Monitoring](docs/MONITORING.md) | Prometheus metrics and alerts |
| [MQTT](docs/MQTT.md) | Retained telemetry and Home Assistant discovery |
| [Troubleshooting](docs/TROUBLESHOOTING.md) | Organised by what you observe |
| [FAQ](docs/FAQ.md) | The questions that come up first |

## Security

polyemesis has no multi-user model and no per-destination permissions. Treat
access to the UI as full control of the server's streaming.

**Do not expose it to the internet — or to a LAN you do not control — without
TLS.** `tls.mode: auto` is the one-line fix; binding to `127.0.0.1` and using an
SSH tunnel is the zero-configuration alternative. Both are in
[docs/TLS.md](docs/TLS.md), along with the SRT passphrase and RTMPS notes for the
parts of the product that are not the web UI.

[SECURITY.md](SECURITY.md) states the threat model plainly, including what
polyemesis deliberately does *not* defend, and is where to report a
vulnerability — privately, not in a public issue.

## Contributing

Pull requests are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) covers setup, the
conventions, and — importantly — the handful of constraints that are not
negotiable, so you find out before writing rather than after.

```bash
make check        # go vet + go test + the UI typecheck
make dev          # backend only, on :8080
make ui-dev       # Vite dev server on :5173, proxying to :8080
make help         # all targets
```

See also [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) and
[CHANGELOG.md](CHANGELOG.md).

## Licence

MIT. See [LICENSE](LICENSE).
