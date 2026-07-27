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
- **Single static binary.** UI embedded, pure-Go SQLite, no cgo, no runtime
  dependencies except FFmpeg itself.

---

## Install

### Requirements

- **FFmpeg 6.0 or newer**, with SRT support for multitrack ingest.
  - macOS: `brew install ffmpeg` — note that the current Homebrew bottle has
    **no SRT**; see [FFmpeg without SRT](#ffmpeg-without-srt).
  - Debian/Ubuntu: `apt install ffmpeg` (24.04+ includes libsrt).
  - Check: `ffmpeg -protocols | tr ' ' '\n' | grep -x srt` must print `srt`.
- Go 1.22+ and Node 20+ only if you are building from source.

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

The **data directory** holds `polyemesis.db`, `secret.key`, `recordings/` and
`hls/`. Back it up: `secret.key` is what decrypts your stored OAuth tokens.

### Docker (optional)

Docker is never required. If you prefer it:

```bash
docker compose up -d
```

The image bundles FFmpeg with SRT. Note the compose file exposes `6000/udp` —
SRT is UDP, and omitting `/udp` is the classic reason an ingest silently
receives nothing.

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

## Reverse proxy

The common deployment terminates TLS at a reverse proxy and lets polyemesis
listen on plain HTTP behind it. A complete nginx example is in
[`deploy/nginx.conf.example`](deploy/nginx.conf.example).

Three things matter:

1. **Set `trustProxyHeaders: true`** in `config.yaml`. polyemesis then honours
   `X-Forwarded-Proto` and `X-Forwarded-Host` when marking session cookies
   `Secure` and when building OAuth redirect URIs. Leave it `false` when there
   is no proxy — otherwise a client could forge those headers.
2. **Proxy the WebSocket.** Live status, meters and logs all arrive over
   `/api/v1/ws`. You need `proxy_set_header Upgrade`/`Connection "upgrade"`,
   `proxy_http_version 1.1`, and a long `proxy_read_timeout`.
3. **Do not proxy the ingest.** SRT is UDP and RTMP is not HTTP; neither can
   travel through an HTTP reverse proxy. Open those ports directly on the
   firewall.

Also turn buffering off (`proxy_buffering off`) or the HLS preview will lag,
and set `client_max_body_size 0` so multi-gigabyte recording downloads work.

Built-in TLS is available if you prefer no proxy:

```yaml
tls:
  enabled: true
  certFile: /etc/polyemesis/cert.pem
  keyFile:  /etc/polyemesis/key.pem
```

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

polyemesis has no multi-user model and no per-destination permissions. Treat
access to the UI as full control of the server's streaming. Do not expose it to
the internet without TLS.

---

## FFmpeg without SRT

If `ffmpeg -protocols | tr ' ' '\n' | grep -x srt` prints nothing, your build
has no SRT and multitrack ingest will fail with `Protocol not found`.

> Careful: `ffmpeg -protocols | grep srt` is **misleading** — every build lists
> `srtp` (Secure RTP), which contains the substring but is a different
> protocol. Use the exact match above.

polyemesis starts anyway and shows a warning, so you can reach Settings and
switch the ingest to RTMP. To get multitrack:

- Install an FFmpeg configured with `--enable-libsrt`.
- Or run the Docker image, which bundles one.

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
