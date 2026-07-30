# Quickstart

From nothing to a stream going out of polyemesis, in about five minutes.

This is the impatient path. [INSTALL.md](INSTALL.md) has the careful version for
each platform, and the [README](../README.md) explains what everything means.

## 0. Check FFmpeg first

Do this before anything else — it is the single most common reason a first run
goes wrong.

```sh
ffmpeg -version | head -1                          # must be 6.0 or newer
ffmpeg -protocols | tr ' ' '\n' | grep -x srt      # must print: srt
```

- **Older than 6.0** → polyemesis refuses to start. Ubuntu 22.04 (4.4) and
  Debian 12 (5.1) both ship too old.
- **No `srt`** → it starts with a warning, but you are limited to RTMP, which
  carries one stereo pair. Multitrack routing — the reason to use this — needs
  SRT. Homebrew's FFmpeg on macOS has no SRT.

If either check fails, use Docker. It bundles a known-good FFmpeg and sidesteps
the whole question.

## 1. Run it

**Docker** (recommended for a first try):

```sh
docker run -d --name polyemesis \
  -p 8080:8080 -p 6000:6000/udp \
  -v polyemesis-data:/data \
  rainmanjam/polyemesis:latest
```

**Binary:**

```sh
./polyemesis                      # serves on :8080, data in ./data
```

Open <http://localhost:8080>.

## 2. Set an admin password

The first page asks you to create one. There is one user — see
[SECURITY.md](../SECURITY.md) for what that does and does not protect.

If you are reaching this over anything other than localhost, the server will
have warned you at startup that the password crosses the network in clear text.
It is right. Set `tls.mode: auto` or use an SSH tunnel.

## 3. Point OBS at it

The **Sources** page shows the publish URL for your source. Copy it — it looks
like this, and the `streamid` is what tells polyemesis which source you are:

```
srt://your-host:6000?streamid=<token>
```

Every source shares that one port. The token is the address, so adding a second
programme later needs no new port and no container restart.

In OBS, multitrack SRT does **not** go through the Stream tab. Use
**Settings → Output → Output Mode: Advanced → Recording**, set
**Type: Custom Output (FFmpeg)** and **FFmpeg Output Type: Output to URL**, then
paste the URL as the path with **Container Format: `mpegts`**.

That sounds wrong and is not: with *Output to URL*, OBS's "recording" **is** the
SRT push. The Stream tab can speak SRT, but it sends one audio track, which
loses the only thing polyemesis is for.

Tick the tracks you want under **Audio Track** in that same panel — the sources
are assigned to tracks 1–6 in the Audio Mixer's **Advanced Audio Properties**.

Set the encoder you normally use. Do *not* set a low keyframe interval on
account of polyemesis — video is passed through untouched, so your encoder
settings are what every destination receives.

Press **Start Recording**, not Start Streaming. [OBS.md](OBS.md) has the full
field-by-field table, including the `latency` unit that catches everybody.

Press **Start Streaming**. The polyemesis dashboard should show the ingest live
within a couple of seconds, with a meter per incoming track.

## 4. Tell it what each track is

On **Sources → annotations**, label the incoming tracks: *mic*, *music*,
*commentary*, and a language where it matters.

This is optional but it is the step that makes everything after it obvious. The
labels belong to the feed, so every destination sees the same set.

## 5. Add a destination

**Destinations → Add.**

- **Platform** — pick one, and the form takes on that platform's limits.
- **URL and stream key** — from the platform's own dashboard. The key is stored
  encrypted and is never shown again or returned by the API.
- **Tracks** — tick the ones this destination should carry. This is the whole
  point: your main channel gets mic + music, the second-language stream gets
  mic + commentary, the podcast feed gets mic only.

Enable it. Video is copied, not re-encoded, so this costs almost nothing — add
as many as you have upstream bandwidth for.

## 6. Watch that it is actually right

The **Meters** page shows loudness measured *after* routing, which is what the
platform on the other end receives. That is the number worth trusting: it
accounts for the mix you just built, not the levels arriving from OBS.

If a destination is silent or quiet, that page will tell you before your viewers
do.

## What to do next

| You want to | Go to |
|---|---|
| Two programmes at once (e.g. horizontal + vertical) | **Sources** — add a second one |
| Different resolutions per destination | **Renditions** |
| Keep a recording | **Settings → Recording** |
| Stay on air when the encoder drops | **Settings → Failover** — off by default |
| Serve a player from polyemesis itself | **Playout** |
| Something is wrong | [TROUBLESHOOTING.md](TROUBLESHOOTING.md) |

## Common first-run problems

**The ingest never goes live.** The port has to be reachable *and* published. In
Docker, `-p 6000:6000/udp` — SRT is UDP, and forgetting `/udp` is the usual
cause. Check the ingest mode matches what OBS is sending.

**A destination shows an error immediately.** Open its process log on the
**Monitoring** page. The platform's own rejection message is almost always in
there, and it is usually a stream key or a bitrate the platform refuses.

**Audio is quiet or missing on one destination.** Check its track selection —
selecting a track the source is not sending gives you silence. The Meters page
measures what is actually going out.

**A video-only source.** Every major platform refuses video with no audio.
Turn on the silence tier (**Settings → Synthetic**) and polyemesis will
synthesise a silent stereo track so your destinations work.
