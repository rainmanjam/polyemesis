# polyemesis

A self-hosted restreaming server with **per-destination multichannel audio
routing**.

You stream once. polyemesis fans it out to as many destinations as you like —
and each destination gets its *own* audio mix, chosen from the audio tracks your
encoder sent. Video is copied through untouched by default. When a platform will
not accept your source video, a shared [**rendition**](#video-renditions)
re-encodes the video *once* for every destination that needs it — and leaves the
audio alone.

```
                                    ┌── YouTube    tracks 1+2   (no music)
OBS ──SRT, 6 audio tracks──► polyemesis ──┼── Twitch     tracks 1+2+3
                                    ├── Kick       tracks 1+2+3+4
                                    └── local file all tracks, unencoded
```

## The problem it solves

You run OBS with several audio tracks — say track 1 is the full mix including
copyrighted music, track 2 is the clean mix without it, track 3 is your mic.
Every live platform accepts exactly **one stereo audio stream**, so normally you
must choose one mix for everyone, or run several encoders and several uploads.

polyemesis ingests once and lets you pick, per destination, which tracks get
summed into the stereo feed that destination receives. YouTube gets the clean
mix; your local archive keeps everything; your Discord restream keeps the mic
hot. One upload, one encode, different audio per platform.

## Features

- **Per-destination audio routing** — the differentiator. Checkbox-per-track
  simple mode, or a full channel-to-output mix matrix with a gain per cell.
- **Video is copied, not re-encoded.** `-c:v copy` on every destination, so CPU
  cost is nearly independent of resolution. Only audio is decoded, mixed and
  re-encoded to AAC.
- **Shared video renditions** for platforms that will not take your source: one
  encode feeds every destination that selects it, ref-counted so an unused tier
  costs nothing. A rendition re-encodes **video only** — audio still routes per
  destination.
- **SRT multitrack ingest** (up to 6 AAC tracks), with RTMP as a fallback.
- **Live audio meters** for every channel of every track — how you verify the
  clean track really is clean, *before* going live.
- **Unlimited destinations**: RTMP(S), SRT, or local file, each independently
  startable with auto-reconnect and exponential backoff.
- **Platform sign-in** for YouTube and Twitch — polyemesis fetches your ingest
  URL and stream key so you never copy-paste one. Multiple accounts per
  platform supported.
- **Full multitrack recording** to segmented MKV, preserving every audio track,
  with size and age retention.
- **Low-latency HLS preview**, live process logs, CPU/RAM and bitrate graphs.
- **Prometheus metrics** at `/api/v1/metrics`, covering ingest, every
  destination, the relay, recording disk and host resources.
- **TLS that configures itself.** `tls.mode: auto` fetches a Let's Encrypt
  certificate when the box has a public name, mints a local CA when it does not,
  and stands aside when a reverse proxy is already terminating TLS. Manual
  certificates stay first-class. See [TLS and certificates](#tls-and-certificates).
- **Single static binary.** UI embedded, pure-Go SQLite, no cgo, no runtime
  dependencies except FFmpeg itself.

---

## Install

Step-by-step instructions for each platform — Docker, Linux, macOS, Windows —
are in [`docs/INSTALL.md`](docs/INSTALL.md). What follows is the short version.

### Platform support, honestly

These are not four equal targets:

| Platform | Status |
|---|---|
| **Linux (server)** | Primary target. Developed against, deployed, exercised. |
| **Docker** | Primary target. Image built from this repo, FFmpeg pinned and bundled. |
| **macOS** | Developed on daily. Good workstation and test rig. Homebrew's FFmpeg has no SRT. |
| **Windows** | Implemented but **not executed on Windows**. It compiles, the service wrapper and process-group teardown are written, the installer scripts exist — but nobody has run the binary on a Windows host. Untested. |

### Requirements

**FFmpeg 6.0 or newer**, with SRT for multitrack ingest. Two separate things,
and they fail differently: polyemesis **refuses to start** on FFmpeg older than
6.0, and starts-with-a-warning on a build that has no SRT (leaving you RTMP,
which is one stereo pair and therefore nothing to route).

```bash
ffmpeg -version | head -1
ffmpeg -protocols | tr ' ' '\n' | grep -x srt      # must print: srt
```

Several current server distributions ship an FFmpeg that is too old, so
`apt install ffmpeg` is not a universal answer:

| Distribution | Stock FFmpeg | polyemesis |
|---|---|---|
| Ubuntu 22.04 LTS (jammy) | **4.4.2** | **refuses to start** |
| Debian 12 (bookworm) | **5.1.9** | **refuses to start** |
| Ubuntu 24.04 LTS (noble) | 6.1.1 | clears the floor |
| Debian 13 (trixie) | 7.1.5 | clears the floor |
| Alpine 3.20 / 3.21 / 3.22 | 6.1.1 / 6.1.2 / 6.1.2 | clears the floor; 3.22 is what the Docker image runs |
| RHEL / Rocky / AlmaLinux | not in the base repositories | see INSTALL.md |

Version numbers checked against the distro package indexes on 2026-07-26.
"Clears the floor" means the version passes the 6.0 startup check — only the
Alpine 3.22 row has actually been run, via the Docker image. If your release is
not listed, run `apt-cache policy ffmpeg` and read the number rather than
assuming.

On a distro that is too old you have three real options — **a newer distro**, **a
static FFmpeg build** with libsrt, or **Docker**. All three, with commands, are
in [`docs/INSTALL.md`](docs/INSTALL.md#if-your-distro-is-too-old-pick-one-of-three).

macOS deserves its own warning: `brew install ffmpeg` is new enough but is
**built without libsrt**, so a stock Homebrew install cannot do multitrack
ingest. See [FFmpeg without SRT](#ffmpeg-without-srt).

To build from source you also need **Go 1.26.5+** (the floor in `go.mod`) and
**Node 20.19+ or 22.12+** (Vite 8's requirement). Neither is needed to run the
resulting binary.

### From source

```bash
git clone https://github.com/rainmanjam/polyemesis
cd polyemesis
make build        # builds the UI, embeds it, produces ./polyemesis
./polyemesis -data ./data
```

Open <http://localhost:8080> and set an admin password on the first-run screen.

`make build` produces one self-contained binary. Copy it anywhere; it needs
only FFmpeg on the host and a writable data directory.

```
Usage of ./polyemesis:
  -addr string      HTTP listen address (default from config, ":8080")
  -config string    path to config.yaml (default "config.yaml")
  -data string      data directory (default "./data")
  -ffmpeg string    path to the ffmpeg binary (default: search $PATH)
  -ffprobe string   path to the ffprobe binary
  -log string       log level: debug, info, warn, error (default "info")
  -version          print the version and exit
```

### Configuration

Copy `config.example.yaml` to `config.yaml`. It holds only deployment-time
settings — listen address, data directory, TLS, FFmpeg paths. Everything else
(ingest ports, recording retention, platform credentials) is configured in the
web UI and stored in SQLite.

The **data directory** holds `polyemesis.db`, `secret.key`, `recordings/`,
`hls/` and — once polyemesis is terminating TLS itself — `tls/`. Back it up:
`secret.key` is what decrypts your stored OAuth tokens, and `tls/` holds the
local CA your browsers have been told to trust and the ACME cache that keeps a
redeploy from re-issuing against Let's Encrypt's rate limits.

`config.yaml` is read-only to polyemesis. The server parses it at startup and
never writes it back, so it is safe to keep in configuration management.

The TLS block is the part most worth reading before you deploy:
[TLS and certificates](#tls-and-certificates).

### Docker (optional)

Docker is never required. If you prefer it:

```bash
docker compose up -d
```

The image bundles FFmpeg with SRT, at a **pinned** package version rather than
whatever the Alpine branch holds on the day you build — otherwise an image
rebuilt months later is a different transcoder under the same tag, and that kind
of drift shows up as a broken stream rather than a build error. The `Dockerfile`
documents how to bump it deliberately and fails the build if the resulting
FFmpeg has no SRT.

Note the compose file publishes `6000/udp` — SRT is UDP, and omitting `/udp` is
the classic reason an ingest silently receives nothing.

**Port 80 is commented out in `docker-compose.yml`**, and needs uncommenting for
exactly one thing: `tls.mode: acme`. Let's Encrypt validates over HTTP-01, so it
has to reach `http://<hostname>/.well-known/acme-challenge/…` on port 80 from
the public internet, which means publishing the port on the host and not merely
`EXPOSE`ing it in the image. It ships off because publishing `:80`
unconditionally breaks `docker compose up` on any host already running a web
server, and the default container configuration is plain HTTP with no
certificate at all. See [ACME needs port 80](#acme-needs-port-80).

### systemd

A hardened unit file is in [`deploy/polyemesis.service`](deploy/polyemesis.service):

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

The unit uses `KillMode=mixed` with a 45 s stop timeout on purpose: polyemesis
tears its FFmpeg children down in order so recordings are finalised rather than
truncated. Letting systemd kill the whole cgroup immediately would cut a
recording off mid-write.

`config.example.yaml` ships `tls.mode: auto`, so a fresh install started this
way gets a self-signed certificate on `:8080` and logs that it could not bind
`:80` for the HTTP→HTTPS redirect — the unit runs unprivileged and ports below
1024 are privileged. That warning is harmless. If you want `mode: acme` it is
not: see the commented `AmbientCapabilities` block in the unit file and
[ACME needs port 80](#acme-needs-port-80).

### Other platforms

- **macOS** — a launchd plist, and the Homebrew SRT problem:
  [`docs/INSTALL.md`](docs/INSTALL.md#macos).
- **Windows** — the Service Control Manager installer lives in
  [`deploy/windows/`](deploy/windows/). Read
  [`deploy/windows/README.md`](deploy/windows/README.md) first, and note the
  maturity caveat above: this path has not been executed on Windows.

---

## OBS setup

### Multitrack over SRT (recommended)

This is the configuration that unlocks per-destination routing. OBS sends
several audio tracks in one MPEG-TS stream over SRT.

**Step 1 — enable the audio tracks.**
`Settings → Output → Audio` (Advanced mode). Set a bitrate for each track you
intend to use, e.g. 160 for tracks 1–3.

**Step 2 — assign sources to tracks.**
In the Audio Mixer, click the gear icon → **Advanced Audio Properties**. Each
source has six **Tracks** checkboxes. A typical layout:

| Source | 1 | 2 | 3 | Meaning |
|---|---|---|---|---|
| Desktop audio (music) | ✓ | | | full mix only |
| Game audio | ✓ | ✓ | | full + clean |
| Microphone | ✓ | ✓ | ✓ | everywhere |

Track 1 = everything, track 2 = clean (no music), track 3 = mic only.

**Step 3 — configure the Custom FFmpeg output.**
`Settings → Output → Output Mode: Advanced → Recording tab`, set
**Type: Custom Output (FFmpeg)**, then:

| Field | Value |
|---|---|
| FFmpeg Output Type | **Output to URL** |
| File path or URL | `srt://YOUR_SERVER:6000?mode=caller&transtype=live&latency=200000` |
| Container Format | `mpegts` |
| Muxer Settings | *(leave blank)* |
| Video Bitrate | e.g. `6000 Kbps` |
| Keyframe interval (frames) | `60` (2 s at 30 fps) |
| Video Encoder | `libx264` (or `h264_nvenc`, `h264_videotoolbox`) |
| Video Encoder Settings | `preset=veryfast tune=zerolatency` |
| Audio Bitrate | `160 Kbps` |
| Audio Encoder | `aac` |
| **Audio Track** | tick **1, 2, 3** (up to 6) |

> The `latency` value is in **microseconds**. `200000` is 200 ms and must match
> the latency set in polyemesis → Settings → Ingest. Writing `200` there gives
> a 0.2 ms buffer and a stream that falls apart on the first jitter.

Copy the exact URL from the polyemesis dashboard — it renders your server's
hostname and current settings, including the passphrase if you set one.

**Step 4 — start it.** Press **Start Recording** (not Start Streaming). With
`Output to URL`, OBS's "recording" is the SRT push.

**Step 5 — verify.** The polyemesis dashboard should show your track count and
video format. Open **Audio meters**, play music, and confirm track 1 moves
while track 2 stays flat. That is the whole product working.

### Standard RTMP (single track)

For encoders that cannot do SRT. RTMP carries one audio track by protocol, so
per-destination *track* routing does not apply — though gain, matrix panning
and 5.1 downmix still do.

In polyemesis: `Settings → Ingest → Mode: RTMP`. Note the app name and stream
key. In OBS: `Settings → Stream`, Service **Custom...**:

| Field | Value |
|---|---|
| Server | `rtmp://YOUR_SERVER:1935/live` |
| Stream Key | the key from polyemesis Settings |

Then press **Start Streaming**.

### Enhanced RTMP multitrack (not implemented)

OBS 30.2+ can send multiple audio tracks over Enhanced RTMP/FLV. **polyemesis
does not support this.** RTMP ingest is single-track.

`config.yaml` accepts an `enhancedRtmp` key, but it is an inert placeholder for
that unbuilt feature: **setting it to `true` does nothing.** No code path reads
it and no endpoint reports it — RTMP ingest behaves identically either way. It
exists only so config files that already carry the key keep parsing. Do not set
it expecting multitrack RTMP.

For multiple audio tracks, use SRT ingest.

---

## Audio routing

Every destination has its own routing profile.

### Simple mode

A row per ingest track with a checkbox and a gain slider. Ticked tracks are
summed into that destination's stereo output. Tracks with more than two
channels are stereo-downmixed first, using FFmpeg's normalized ITU
coefficients (a 5.1 track folds down with LFE dropped, scaled so a fully
correlated source cannot clip).

### Mix matrix

A grid mapping every channel of every track onto L and R, with a gain per cell
from 0.0 to 2.0. This subsumes simple mode and additionally lets you do things
like take only the rear channels of a 5.1 track, pan a mono mic hard left, or
swap channels.

### Clip protection

Summing tracks can exceed full scale. By default (`auto`) a limiter is inserted
whenever two or more tracks are combined and omitted for a single track. You
can force it off, force the limiter, or use EBU R128 loudness normalization
(`-16 LUFS`) instead.

> Note: polyemesis sets `amix=normalize=0` deliberately. FFmpeg's default
> divides the sum by the number of inputs, which would quietly drop a 3-track
> mix by ~9.5 dB. Levels are controlled by per-track gain instead, and the
> resulting clip risk is handled by the limiter.

### Presets

**Everything** · **No music** (all tracks except a nominated one) ·
**Mic only** · **5.1 → stereo** (emitted as an editable matrix so you can see
and tweak the coefficients).

### Transparency

The routing editor shows the **exact `-filter_complex` string** that will be
passed to FFmpeg, recompiled live as you edit, by the same code that runs it.
Copy it and reproduce any destination by hand.

Changing a routing profile restarts **only that destination**. The ingest, the
recorder, and every other destination are untouched.

---

## Video renditions

### The problem

You ingest 4K60. YouTube will take it. Twitch, Kick and X cap well below 4K, so
they reject it — or accept it and quietly transcode it into something worse.

Without renditions the only way out is to drop **your whole ingest** to the
lowest common denominator: YouTube gets 1080p because Kick cannot do 4K. And
running one polyemesis destination per resolution does not help either, because
each destination copies video — none of them can change it.

A **rendition** is a named video output profile. Destinations *select* a
rendition rather than owning one, so three platforms that all want 1080p60 cost
**one** encode, not three.

```
relay ─────────────────────────► dest:youtube   -c:v copy + audio graph A
  │                               (rendition = passthrough, zero cost)
  └──► rendition "1080p60 6M"     ONE encode
         (video encoded, ALL audio tracks copied through untouched)
         └──► rendition's own relay hub
                ├─► dest:twitch   -c:v copy + audio graph B
                ├─► dest:kick     -c:v copy + audio graph C
                └─► dest:x        -c:v copy + audio graph D
```

### A rendition re-encodes video only

This is the load-bearing rule, and it is worth stating plainly: a rendition
encodes **video**, and passes **every audio track through with `-c:a copy`**. It
never mixes, never downmixes, never re-encodes audio. There is no audio setting
on a rendition and there never will be one.

That is what keeps the differentiator intact on top of shared video. The
destinations downstream of a rendition still receive the full multitrack stream,
still compile their own `-filter_complex` from their own routing profile, and
still do `-c:v copy` — exactly as a passthrough destination does. Audio is
encoded once, at the destination, and never twice.

Consequently, changing which rendition a destination is on does not change its
audio, and changing its audio routing does not restart the rendition or disturb
the other destinations sharing it.

### Passthrough is a rendition

**Passthrough** is the zero-cost default: no process, no encode. The destination
subscribes straight to the ingest relay and copies the source video, which is
precisely what every destination has always done.

Every existing destination stays on passthrough with no action from you, and
behaves exactly as it did before. This feature is strictly additive: if you
never create a rendition, nothing about your install changes.

### A rendition only runs when something needs it

The encode starts when the **first enabled** destination selects the rendition,
and stops when the last one releases it. A rendition that nothing enabled points
at has no process and burns no CPU — creating a tier you are not using yet is
free, and stopping the last destination on a tier stops its encode too.

- Editing a rendition restarts that encode and exactly the destinations reading
  it. Nothing else moves.
- Renaming a rendition, or editing its note, restarts nothing.
- Deleting a rendition does **not** delete its destinations. They fall back to
  passthrough and keep running — which means they are suddenly being handed your
  source video, so the delete tells you how many destinations that just happened
  to. Check the source still fits what each of those platforms accepts.

Each destination's card on the dashboard shows the rendition it is on directly
above the audio tracks it receives, so "what video and what audio does this
platform get" is one glance, not two.

### Presets are starting points, not limits

The rendition editor offers these as editable starting points:

| Preset | Size | Rate | Video bitrate | Encoder |
|---|---|---|---|---|
| Source passthrough | source | source | — (no encode) | — |
| 1080p60 | 1920×1080 | 60 fps | 6000 kbps | `libx264`, `veryfast`, 2 s GOP |
| 1080p30 | 1920×1080 | 30 fps | 4500 kbps | `libx264`, `veryfast`, 2 s GOP |
| 720p60 | 1280×720 | 60 fps | 4500 kbps | `libx264`, `veryfast`, 2 s GOP |
| 720p30 | 1280×720 | 30 fps | 3000 kbps | `libx264`, `veryfast`, 2 s GOP |

> **Starting point — verify current limits with the platform.**
>
> These are not authoritative ceilings and are not presented as any platform's
> policy. Published limits change without notice, and they differ by partner,
> affiliate and beta status on the same platform for two different accounts.
> Being confidently wrong about one of these numbers breaks a live stream, so
> where we were unsure we picked the *lower* value: an under-spec stream is
> watchable, an over-spec one is rejected at the ingest.
>
> Check your own account's current limits on the platform, then edit the
> rendition. Every field is yours to change.

Keyframe interval is set in **seconds**, not frames, so it stays correct when
you change the frame rate. Two seconds suits every live platform we know of.

### Hardware encoders

polyemesis offers whichever encoders your FFmpeg build actually registers —
probed from `ffmpeg -encoders`, not guessed — and greys out the rest with the
reason, so you cannot select NVENC on a machine with no NVIDIA card and discover
it only when a stream goes live.

| Family | Encoders | Notes |
|---|---|---|
| Software | `libx264`, `libx265` | Always available. Identical rate control everywhere. |
| NVIDIA | `h264_nvenc`, `hevc_nvenc` | Presets are `p1`–`p7`; `p4` is the honest middle. |
| Intel Quick Sync | `h264_qsv`, `hevc_qsv` | Needs a working VA-API/QSV runtime, not just the CPU. |
| Apple | `h264_videotoolbox`, `hevc_videotoolbox` | No preset knob; `-realtime` is the lever. |
| VA-API (Linux) | `h264_vaapi`, `hevc_vaapi` | Needs a render node, `/dev/dri/renderD128` by default. |
| AMD | `h264_amf`, `hevc_amf` | Windows and Linux with the AMF runtime installed. |

`libx264` is the default even on a machine with a GPU, because its behaviour is
the same on every host while the hardware wrappers vary by driver version.
Hardware is an opt-in you make deliberately.

**Be realistic about software 4K60.** `libx264` at 4K60 is not a workload a
normal streaming box handles: even at `veryfast` it needs a very high core count
to hold realtime, and this machine is *already* running your ingest, your
recorder, the preview and one FFmpeg per destination. If it cannot keep up, the
encode falls behind realtime and every destination on that rendition suffers.
If you are ingesting 4K60 and need it re-encoded, use a hardware encoder. If you
have no hardware encoder, rendition down from 4K rather than at 4K — that is
what renditions are for.

Note also that most RTMP ingests accept **H.264 only**. The HEVC encoders are
listed because they are real and occasionally useful (SRT, a file destination,
an ingest you control), not because a live platform is likely to take one.

---

## Platform accounts

polyemesis can fetch your stream key automatically so you never copy-paste one.
This requires **your own** OAuth developer app: polyemesis cannot ship client
secrets, because anyone with the binary would have them and the platforms would
revoke them.

`Settings → Platform credentials` has step-by-step instructions and renders the
exact redirect URI to whitelist. In summary:

### YouTube (Google)

1. <https://console.cloud.google.com/apis/credentials> — create or pick a project.
2. **APIs & Services → Library** → enable **YouTube Data API v3**.
3. **OAuth consent screen** → External; add your own Google account under
   *Test users*. You do not need to publish the app.
4. **Credentials → Create Credentials → OAuth client ID → Web application.**
5. Add the redirect URI shown on the credentials page, exactly:
   `https://YOUR_HOST/api/v1/oauth/youtube/callback`
6. Paste the client ID and secret into polyemesis, then **Connect account**.

Scope requested: `https://www.googleapis.com/auth/youtube`. Write access is
needed because polyemesis creates a reusable ingest stream if your channel has
none.

### Twitch

1. <https://dev.twitch.tv/console/apps> → **Register Your Application**.
2. OAuth Redirect URL: `https://YOUR_HOST/api/v1/oauth/twitch/callback`
3. Category: *Broadcasting Suite*. Client Type: **Confidential**.
4. **Manage → New Secret**, then paste both values into polyemesis.

Scope requested: `channel:read:stream_key`.

### Kick

Kick's public API does not expose stream keys, so there is nothing to connect.
Add a Kick destination and paste the RTMPS URL and key from your Kick creator
dashboard (**Settings → Stream**). Everything else — routing, meters, reconnect,
recording — works identically.

### Multiple accounts

Connect the same platform more than once to stream to several channels. Each
connected account becomes its own destination with its own routing profile.

Tokens and client secrets are encrypted at rest with NaCl secretbox, keyed by
`secret.key` in the data directory, and refreshed automatically.

---

## TLS and certificates

polyemesis can terminate TLS itself. `tls.mode` chooses how, and
`config.example.yaml` ships `auto`, which decides at startup.

| mode | certificate from | browser warning | needs |
|---|---|---|---|
| `auto` | resolves to one of the four below | depends | nothing |
| `acme` | Let's Encrypt, issued on demand | none | public DNS name, `acmeEmail`, inbound port 80 |
| `selfsigned` | a CA generated on this box | yes, until you trust that CA | nothing |
| `manual` | `certFile` / `keyFile` you supply | none, if their issuer is trusted | those two files |
| `off` | nothing — plain HTTP | n/a | something else terminating TLS |

Whenever polyemesis is terminating TLS, the listener pins **TLS 1.2 as the
floor** and prefers X25519, then P-256 and P-384. Go's server default already
floors at 1.2; pinning it means a future toolchain default cannot quietly change
what this server accepts.

### How `auto` decides

At startup, in this order:

1. `trustProxyHeaders: true` → **off**. You have told polyemesis a reverse proxy
   sits in front of it, so the proxy owns TLS.
2. `hostname` is a public FQDN **and** `acmeEmail` is set → **acme**.
3. anything else → **selfsigned**.

A *public FQDN* contains a dot, is not an IP literal, and does not end in
`.local`, `.internal`, `.lan`, `.home`, `.arpa` or `localhost` — a name Let's
Encrypt could plausibly validate. `stream.example.com` qualifies;
`polyemesis.lan`, `nas.local` and `192.168.1.10` do not.

Rule 2 needs *both* a public name and a contact address, so a box with real DNS
but no `acmeEmail` falls to self-signed rather than repeatedly failing issuance.

If `hostname` is empty and the resolved mode is `selfsigned`, the system
hostname is used, so the certificate always has a name in it.

### Upgrading from `tls.enabled`

`tls.enabled` is still parsed, and is consulted **only when `tls.mode` is
absent**:

| existing config | behaves as |
|---|---|
| `enabled: true` with `certFile`/`keyFile` | `mode: manual` — your certificate keeps being served |
| `enabled: false`, or no `tls:` block at all | `mode: off` — plain HTTP, exactly as before |

An explicit `tls.mode` always wins, so you can migrate without deleting the old
key, and an upgrade never swaps a real certificate for a self-signed one or
silently stops serving HTTPS. Note that this also means **an existing install
does not get `auto` for free** — it keeps doing what it did yesterday until you
write `mode: auto` yourself.

### Worked configurations

**1. Public server with a DNS name — the recommended deployment.**

```yaml
addr: ":443"
tls:
  mode: "auto"                    # resolves to acme
  hostname: "stream.example.com"
  acmeEmail: "ops@example.com"
  hsts: true                      # safe here: publicly trusted certificate
```

Point an A/AAAA record at the box and open **80 and 443**. The certificate is
issued lazily, on the first HTTPS handshake for that name, and renewed
automatically. Tradeoff: you depend on Let's Encrypt being reachable and on
your DNS being correct, and issuance is pinned to that one hostname — a request
arriving with any other SNI is refused rather than triggering a new order,
which is what stops a public port being used to burn your rate limit.

**2. Homelab box with no public DNS.**

```yaml
tls:
  mode: "auto"                    # resolves to selfsigned
  hostname: "polyemesis.lan"
```

polyemesis mints a local CA and a leaf for that name, plus `localhost`,
`127.0.0.1` and `::1` so the first login over an SSH tunnel or by loopback does
not warn either. Tradeoff: every browser warns until you install the CA (below),
and mobile clients are genuinely annoying to convince. In exchange the traffic
is encrypted, which is the part that matters on a shared LAN.

If you reach the box by LAN address rather than by name, put the address in
`hostname` — an IP literal is accepted and becomes a SAN. A certificate naming
only `polyemesis.lan` will still warn when you browse to `https://192.168.1.10`,
because the name you typed is not in it. Changing `hostname` reissues the leaf
on the next start; the CA, and everything that already trusts it, is untouched.

**3. Behind nginx / Caddy / Traefik.**

```yaml
addr: "127.0.0.1:8080"
trustProxyHeaders: true
tls:
  mode: "auto"                    # resolves to off
```

The proxy owns TLS, HSTS and the redirect. See
[Behind a reverse proxy](#behind-a-reverse-proxy).

**4. Your own certificate — corporate CA, wildcard, cert-manager, an existing
certbot.**

```yaml
tls:
  mode: "manual"
  hostname: "stream.example.com"  # used for the HTTP→HTTPS redirect target
  certFile: "/etc/ssl/polyemesis/fullchain.pem"
  keyFile:  "/etc/ssl/polyemesis/privkey.pem"
  hsts: true                      # only if the issuer is publicly trusted
```

`certFile` should be the full chain, leaf first. Both files are read at startup
and never rewritten. Tradeoff: **renewal is yours.** polyemesis loads the pair
once, so a certbot renewal needs a `systemctl restart polyemesis` (or a
`--deploy-hook`) before the new certificate is served. Missing or mismatched
files are a hard startup error, naming both paths.

**5. Plain HTTP on purpose.**

```yaml
addr: "127.0.0.1:8080"
tls:
  mode: "off"
```

Fine on loopback. On any other address it is the worst thing in this document —
see [Binding, and the SSH tunnel](#binding-and-the-ssh-tunnel).

### Trusting the self-signed CA

The generated material lives in `<dataDir>/tls/` (directory `0700`, private keys
`0600`, never logged and never returned by any API):

```
<dataDir>/tls/ca.crt        the local CA — this is the file you install
<dataDir>/tls/ca.key        its private key. Never leaves the box.
<dataDir>/tls/server.crt    the leaf, followed by the CA, as a chain
<dataDir>/tls/server.key    the leaf's private key
```

The CA is valid for ten years; the leaf for one and is regenerated
automatically within 30 days of expiry, or if you change `tls.hostname`. That
split is on purpose: installing a CA into a browser, a phone and a keychain is
the most tedious step of a homelab setup, and making you redo it annually would
be a reason to give up on HTTPS entirely.

Copy the CA to the machine you browse from and **check the fingerprint** against
the `ca sha-256` line polyemesis prints at startup before you trust it:

```bash
scp user@host:/var/lib/polyemesis/tls/ca.crt ./polyemesis-ca.crt
openssl x509 -in polyemesis-ca.crt -noout -fingerprint -sha256
```

The server also offers it at `GET /api/v1/tls/ca`, which needs no session — on a
fresh box the browser will not let you reach the login form until the CA is
installed, so gating the download behind a sign-in would deadlock the only way
out of that. It is the public half of the CA, which every client already
receives during the handshake; the private key has no route. The Settings page
links to it, or:

```bash
curl -k https://polyemesis.lan:8443/api/v1/tls/ca -o polyemesis-ca.crt
```

Check the fingerprint either way — `-k` means you have not yet verified who
answered.

**macOS** (Keychain Access will also do this by drag-and-drop):

```bash
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain polyemesis-ca.crt
```

**Linux**, Debian/Ubuntu:

```bash
sudo cp polyemesis-ca.crt /usr/local/share/ca-certificates/polyemesis.crt
sudo update-ca-certificates
```

Fedora/RHEL: drop it in `/etc/pki/ca-trust/source/anchors/` and run
`sudo update-ca-trust`.

> Firefox — and Chrome on Linux — do **not** use the system store. Firefox:
> *Settings → Privacy & Security → Certificates → View Certificates →
> Authorities → Import*, and tick "identify websites". Chrome on Linux reads
> NSS: `certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n polyemesis -i polyemesis-ca.crt`.

**Windows**, PowerShell as Administrator:

```powershell
Import-Certificate -FilePath .\polyemesis-ca.crt `
  -CertStoreLocation Cert:\LocalMachine\Root
```

**iOS/Android** both need two steps — install the profile *and* then explicitly
enable it as a trusted root (iOS: *Settings → General → About → Certificate
Trust Settings*). If that is more than you want to do, use mode `acme`, or reach
the UI over the SSH tunnel below.

### ACME needs port 80

Let's Encrypt validates over **HTTP-01**, which means it must reach
`http://<hostname>/.well-known/acme-challenge/…` on **port 80** from the public
internet. Open it on the firewall and in any NAT/port-forward. A CNAME to a CDN,
a captive portal, or an ISP that blocks 80 will all break issuance.

polyemesis also advertises the **TLS-ALPN-01** protocol on its HTTPS listener,
so on a box where 443 is reachable but 80 is not, issuance can still succeed
that way. Treat that as a fallback, not a plan.

If port 80 cannot be bound — already taken, or the process is unprivileged —
polyemesis **logs a warning and carries on serving**. It does not refuse to
start. That is deliberate: a server that dies over a certificate problem leaves
you with no UI in which to fix the setting that killed it. The log carries a
warning reading

```text
cannot bind :80 for the acme http-01 challenge; certificate issuance will keep
failing until port 80 reaches this host (free the port, grant
CAP_NET_BIND_SERVICE, or forward it). Serving https meanwhile
```

and the startup banner says the certificate has not been issued yet.

Under systemd, the usual fix is `AmbientCapabilities=CAP_NET_BIND_SERVICE` —
see the commented block in
[`deploy/polyemesis.service`](deploy/polyemesis.service).

Back up `<dataDir>/tls/acme/`. It holds your ACME account key and every issued
certificate; a redeploy that loses it re-orders from scratch, and Let's Encrypt's
duplicate-certificate limit is not generous.

### HSTS is opt-in, and here is why

`tls.hsts` defaults to `false`. Turn it on only when you have a certificate a
browser will validate without help.

`Strict-Transport-Security` tells a browser "never speak plain HTTP to this
hostname again, and never let the user click through a certificate warning for
it". The browser remembers that **for the whole max-age, on the client**, and
there is no way for the server to take it back — clearing it means clearing site
data in every browser on every device that saw the header.

Now picture that on a homelab box with a self-signed certificate. The browser
has been told to refuse plain HTTP to `polyemesis.lan`, and HSTS also removes
the "Advanced → Proceed anyway" escape hatch for the untrusted certificate. Both
doors are shut, from one header, and rebuilding the server does not reopen them.
That is why HSTS here is opt-in rather than on by default.

So:

- **Never sent unless the connection really is HTTPS.** The check is Go's
  `r.TLS`, not a forwarded header — a header can be forged, and behind a trusted
  proxy the policy for the connection the browser actually made belongs to
  whoever terminated it.
- **Never sent in `selfsigned` mode, even with `hsts: true`.** polyemesis logs a
  warning at startup explaining that it is being suppressed, rather than
  quietly obeying you into a lockout.
- **Never sent when the resolved mode is `off`** — same warning.
- When it *is* sent it is `max-age=86400`, one day. **No `includeSubDomains`, no
  `preload`.** Both widen the blast radius past this one host, and preload in
  particular is close to irreversible. A day is long enough to be a real
  downgrade defence and short enough that a mistake ages out.

If you want a long max-age and preloading, set it on the reverse proxy, where
you already own the whole origin and can undo it.

### Binding, and the SSH tunnel

The default `addr` is `":8080"` — **every interface**. Plain HTTP on every
interface is the single biggest practical exposure this product has: the login
form and the session cookie cross the network in clear text, and anyone on the
path can read or replay them. polyemesis prints a loud warning at startup when
it detects exactly that combination (binds publicly, not terminating TLS, no
trusted proxy).

Fix it by enabling TLS — or, if you just want it private with no certificates at
all, bind to loopback and tunnel:

```yaml
addr: "127.0.0.1:8080"
tls:
  mode: "off"
```

```bash
ssh -N -L 8080:127.0.0.1:8080 user@host
```

Then open <http://localhost:8080>. SSH carries the encryption and the
authentication, nothing is exposed on the box, and there is no certificate to
install anywhere. For a single admin this is the lowest-effort secure setup
there is.

Two caveats, both real:

- **The tunnel only covers the web UI.** The ingest listener binds `0.0.0.0` on
  its own port regardless of `addr`, because your encoder has to reach it. Guard
  that with the SRT passphrase and a firewall rule — see
  [Transport security beyond the UI](#transport-security-beyond-the-ui).
- The HLS preview and the WebSocket both travel inside the tunnel and work
  normally; nothing in the UI needs a second port.

### Behind a reverse proxy

The other common deployment terminates TLS at nginx (or Caddy, or Traefik) and
lets polyemesis listen on plain HTTP behind it. A complete example is in
[`deploy/nginx.conf.example`](deploy/nginx.conf.example).

With `trustProxyHeaders: true`, `mode: auto` resolves to **off** — polyemesis
does not try to obtain or serve a certificate, does not bind port 80, and sends
no HSTS. The proxy owns all three. That is the intended interaction, not a
limitation: two things fighting over port 80 for ACME is a much worse day than
one.

Four things matter:

1. **Set `trustProxyHeaders: true`.** polyemesis then honours
   `X-Forwarded-Proto` and `X-Forwarded-Host` when marking session cookies
   `Secure` and when building OAuth redirect URIs. Leave it `false` when there
   is no proxy — otherwise a client can forge those headers.
2. **Bind polyemesis to loopback** (`addr: "127.0.0.1:8080"`). With a proxy in
   front there is no reason for the plaintext port to be reachable from
   anywhere else, and `trustProxyHeaders` suppresses the exposure warning that
   would otherwise have told you about it.
3. **Proxy the WebSocket.** Live status, meters and logs all arrive over
   `/api/v1/ws`: `proxy_http_version 1.1`, `Upgrade`/`Connection "upgrade"`, and
   a long `proxy_read_timeout`.
4. **Do not proxy the ingest.** SRT is UDP and RTMP is not HTTP; neither travels
   through an HTTP reverse proxy. Open those ports directly on the firewall.

Also turn buffering off (`proxy_buffering off`) or the HLS preview will lag, and
set `client_max_body_size 0` so multi-gigabyte recording downloads work.

If you want HSTS in this deployment, set it on the proxy. polyemesis will not
send it with `mode: off` however `tls.hsts` is set, and says so at startup.

### The plain-HTTP companion on :80

Whenever polyemesis is terminating TLS itself, it also tries to bind `:80` for a
small helper that does two jobs: answers ACME HTTP-01 challenges (acme mode
only) and permanently redirects everything else to HTTPS. Redirects are `301`
for `GET`/`HEAD` and `308` for everything else, so an API client's method and
body survive the hop.

It is skipped when `addr` is already port 80, and a failure to bind is a warning
rather than a fatal error — you keep your HTTPS listener and your UI either way.

### Security headers

Every response carries:

| header | value |
|---|---|
| `Content-Security-Policy` | `default-src 'self'` plus the relaxations below |
| `X-Frame-Options` | `DENY` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `no-referrer` |
| `Permissions-Policy` | `camera=(), microphone=(), geolocation=()` |
| `Strict-Transport-Security` | only under the conditions above |

The CSP relaxations exist for specific features and each one is load-bearing:
`media-src 'self' blob:` and `worker-src 'self' blob:` because hls.js hands
`<video>` a blob URL and compiles its demuxer worker from generated source;
`connect-src 'self' ws: wss:` for the telemetry WebSocket, with `ws:` because a
LAN box may legitimately be on plain HTTP; `img-src 'self' data:` for inline
icons; `style-src 'self' 'unsafe-inline'` because the bundle injects `<style>`
at runtime. Notably **absent** is `'unsafe-inline'` for scripts — the UI is a
Vite bundle of hashed module files with no inline `<script>`, and that is the
one relaxation that would turn an injected string into executable code.

### Transport security beyond the UI

TLS on the web UI is not the whole story:

- **SRT ingest has its own encryption.** Set a passphrase under *Settings →
  Ingest* (SRT requires 10–79 characters, which polyemesis enforces in the form)
  and SRT encrypts the stream with AES. The dashboard renders the exact
  `srt://…?passphrase=…` URL to paste into OBS. Without one, your stream —
  including anything on screen — crosses the network unencrypted.
- **RTMP ingest has no equivalent.** RTMP is authenticated by the stream key in
  the URL and is otherwise in the clear. It is the fallback for encoders that
  cannot do SRT; prefer SRT where you have the choice.
- **Destinations can be `rtmps://`.** RTMP destination URLs accept both
  `rtmp://` and `rtmps://` and the URL is handed to FFmpeg verbatim, so where a
  platform publishes an RTMPS ingest address, paste that one. SRT destinations
  are passed through unchanged too — append your own `?passphrase=…` if the
  receiving end expects one.

---

## Monitoring

`GET /api/v1/metrics` returns Prometheus text exposition. Everything the
dashboard draws is there: ingest state and bitrate, per-destination state,
bitrate, restarts and dropped frames, relay throughput and drops, recording
disk usage, and the process's own CPU and memory.

**The endpoint requires authentication.** It accepts an API token, which is
what a scraper should use — create one under Settings → API tokens and point
Prometheus at it:

```yaml
scrape_configs:
  - job_name: polyemesis
    metrics_path: /api/v1/metrics
    static_configs:
      - targets: ['stream.example.com']
    authorization:
      credentials_file: /etc/prometheus/polyemesis.token
```

A session cookie works too, so you can just open the URL in a signed-in
browser tab.

Why not leave it open to loopback, as many projects do? Because loopback is
both too strict and too lax here. Prometheus normally runs in a neighbouring
container, so its scrape arrives from a bridge address and would be refused;
and once `trustProxyHeaders` is on, *every* request arrives from a proxy on
127.0.0.1, so the check would let the whole internet in. A revocable token is
correct in both deployments.

Metric names carry the `polyemesis_` prefix, counters end in `_total`, and
values are in base units — bytes, seconds, bits per second. Destinations are
labelled `id` and `name`; `polyemesis_destination_info` carries `kind` and
`platform` for joining.

A few queries to start from:

```promql
polyemesis_ingest_up == 0                                   # nobody is streaming
polyemesis_destination_up == 0 and polyemesis_destination_enabled == 1
rate(polyemesis_destination_restarts_total[15m]) > 0        # a flapping output
polyemesis_recording_free_bytes < 20e9                      # disk filling up
```

---

## Automation with API tokens

Everything the UI does is a REST call, and a script can make the same calls
with an API token instead of a session. Create one under **Settings →
Security → API tokens**; the secret is shown once, because polyemesis stores
only its hash.

```sh
curl -H "Authorization: Bearer pmk_..." https://stream.example.com/api/v1/status
curl -H "Authorization: Bearer pmk_..." \
     -X POST https://stream.example.com/api/v1/destinations/3/stop
```

A token acts as the admin, with one exception: it cannot create or revoke
tokens. That is deliberate. If a leaked token could mint more, revoking the one
you know about would mean nothing — the holder has quietly issued three others.
Minting stays behind the password, so revocation is final.

Token requests need no CSRF header: the header exists to prove a request was
not made by a browser carrying your cookie automatically, and a `Bearer` header
is never sent automatically.

---

## Security

- Single admin user; password hashed with bcrypt.
- Session is a JWT in an `HttpOnly`, `SameSite=Lax` cookie, signed with a key
  derived from the server secret.
- Failed logins are throttled per client address: five free attempts, then a
  delay that doubles from 2s and is capped at 5 minutes, with `Retry-After` on
  the 429. The cap and a one-hour idle reset are what stop an attacker turning
  the lockout into a denial of service against the admin. Counters live in
  memory only — a restart must never strand you outside your own server.
- API tokens are stored as hashes, prefixed `pmk_`, revocable individually, and
  cannot manage other tokens.
- CSRF: double-submit token required on every state-changing request.
- OAuth `state` is server-stored, single-use and expiring.
- OAuth tokens and client secrets encrypted at rest (NaCl secretbox).
- File destinations and recording downloads are confined to the recordings
  directory; paths from the database are never trusted.
- Every response carries a CSP, `X-Frame-Options: DENY`, `nosniff`,
  `Referrer-Policy: no-referrer` and a `Permissions-Policy` that switches off
  camera, microphone and geolocation. HSTS is opt-in and never sent over a
  self-signed certificate — see [HSTS is opt-in, and here is
  why](#hsts-is-opt-in-and-here-is-why).
- Generated TLS material lives in `<dataDir>/tls/` with the directory `0700` and
  private keys `0600`. No key is ever logged, and no API returns one.

polyemesis has no multi-user model and no per-destination permissions. Treat
access to the UI as full control of the server's streaming.

**Do not expose it to the internet — or to a LAN you do not control — without
TLS.** `tls.mode: auto` is the one-line fix; binding to `127.0.0.1` and using an
SSH tunnel is the zero-configuration alternative. Both are in
[TLS and certificates](#tls-and-certificates), along with the SRT passphrase and
RTMPS notes for the parts of the product that are not the web UI.

---

## FFmpeg without SRT

If `ffmpeg -protocols | tr ' ' '\n' | grep -x srt` prints nothing, your build
has no SRT and multitrack ingest will fail with `Protocol not found`.

> Careful: `ffmpeg -protocols | grep srt` is **misleading** — every build lists
> `srtp` (Secure RTP), which contains the substring but is a different
> protocol. Use the exact match above.

polyemesis starts anyway and shows a warning, so you can reach Settings and
switch the ingest to RTMP. But RTMP carries a single stereo pair, so
per-destination audio routing has nothing to route from — SRT is what makes this
product work.

Where builds stand, checked 2026-07-26. "Ran the check" means the `-protocols`
command above was executed against that build; everything else is read off a
package index or a published feature list, so run the check yourself:

| Source | libsrt |
|---|---|
| Docker image in this repo | yes — the build asserts it, and it was run |
| [BtbN](https://github.com/BtbN/FFmpeg-Builds/releases) static builds, **Linux** | yes — ran the check |
| Ubuntu 24.04 / Debian 13 `apt install ffmpeg` | advertised in the package build flags; not run |
| [BtbN](https://github.com/BtbN/FFmpeg-Builds/releases) static builds, **Windows** | same recipe as their Linux asset, but no Windows asset was downloaded or run |
| [gyan.dev](https://www.gyan.dev/ffmpeg/builds/) Windows builds | listed among the *essentials* build's externals; not downloaded or run |
| **Homebrew `ffmpeg` on macOS** | **no** — `srt` is not among the formula's dependencies, and the check was run |
| [johnvansickle.com](https://johnvansickle.com/ffmpeg/) static builds | not advertised; run the check before relying on it |

On macOS, the way out is the `homebrew-ffmpeg` tap, which exposes the option:

```bash
brew tap homebrew-ffmpeg/ffmpeg
brew install homebrew-ffmpeg/ffmpeg/ffmpeg --with-srt   # builds from source
```

Otherwise: install an FFmpeg configured with `--enable-libsrt`, or run the
Docker image, which bundles one. Full per-platform detail in
[`docs/INSTALL.md`](docs/INSTALL.md).

---

## Architecture

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the process graph, the
relay design and the tradeoffs behind it. In brief:

- One supervised FFmpeg process listens for the incoming stream and republishes
  it, untouched, as MPEG-TS to an in-process Go **relay hub** on loopback UDP.
- The hub replicates every datagram to each subscriber, so destinations, the
  recorder, the HLS preview and the metering sidecar each read the full stream
  independently and can restart without disturbing the ingest.
- A rendition is that same shape one level up: a supervised FFmpeg reading the
  ingest hub, encoding video only, publishing to a **hub of its own** that its
  destinations subscribe to instead. Ref-counted by its enabled destinations, so
  a tier nobody selects has no process.
- Each destination is its own supervised FFmpeg process: video copied — from the
  ingest hub, or from its rendition's — audio compiled from its routing profile.
- Every child runs in its own process group and dies with the parent.

---

## Development

```bash
make check        # go vet + go test + tsc --noEmit
make test         # Go tests only
make dev          # backend only, on :8080
make ui-dev       # Vite dev server on :5173, proxying to :8080
make release      # cross-compile into dist/
make help         # all targets
```

The routing engine (`internal/routing`) and the FFmpeg command builders
(`internal/ffmpeg`) are pure functions with no I/O, which is what makes them
exhaustively unit-testable without spawning a process. That is where the tests
are concentrated, because those are the parts whose bugs are only audible.

Testing without OBS: see [`docs/TESTING.md`](docs/TESTING.md).

## Licence

MIT.
