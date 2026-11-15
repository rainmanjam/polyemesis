# Quickstart

From nothing to a stream going out of polyemesis: install, add a source and a
destination, then stream.

This is the impatient path. [INSTALL.md](INSTALL.md) has the careful version for
each platform, and the [README](../README.md) explains what everything means.

## 0. Check FFmpeg first

Do this before anything else — it is the single most common reason a first run
goes wrong.

```sh
ffmpeg -version | head -1                          # must be 8.0 or later
ffmpeg -protocols | tr ' ' '\n' | grep -x srt      # must print: srt
```

- **Older than 8.0** → polyemesis refuses to start. Ubuntu 22.04 (4.4),
  Debian 12 (5.1), Ubuntu 24.04 (6.1.1) and Debian 13 (7.1.5) all ship too old.
- **No `srt`** → it starts with a warning, but you are limited to RTMP, which
  carries one stereo pair, unless the encoder speaks Enhanced RTMP. Multitrack
  routing — the reason to use this — needs
  SRT. Homebrew's FFmpeg on macOS has no SRT.

If either check fails, use Docker. It bundles a known-good FFmpeg and sidesteps
the whole question.

## 1. Run it

**Docker** (recommended for a first try):

```sh
docker run -d --name polyemesis \
  -p 8080:8080 -p 6000:6000/udp -p 1935:1935 \
  -v polyemesis-data:/data \
  rainmanjam/polyemesis:latest
```

> Note the `/udp` on port 6000. SRT is UDP, and omitting the suffix is the
> classic reason an ingest silently receives nothing.

> **Publish 1935 now even if you plan to use SRT.** Step 3 lets you choose RTMP
> per source, and a container's port map is fixed when it is created — adding it
> later means `docker rm` and re-creating the container. Left out, an RTMP
> source fails silently: the card shows a publish URL, the container is healthy,
> the UI reports no error, and the encoder simply never connects.

**Binary:**

```sh
./polyemesis                      # serves on :8080, data in ./data
```

Open <http://localhost:8080>.

## 2. Set an admin password

The first page asks you to create one. There is one user — see
[SECURITY.md](../SECURITY.md) for what that does and does not protect.

It also asks for a **setup code**. The server printed it in the terminal when
it started, and saved it in `setup-code` in the data directory (`./data`
here). It works once. See
[INSTALL.md](INSTALL.md#the-first-run-setup-code) for where it is on other
installs.

If you are reaching this over anything other than localhost, the server will
have warned you at startup that the password crosses the network in clear text.
It is right. Set `tls.mode: auto` or use an SSH tunnel.

## 3. Create a source

A **source** is one ingested programme — one feed from one encoder. A fresh
install has none, so this is the step that turns a running server into one
anything can publish to.

Open the **Sources** page and add one. The dialog asks for a name and nothing
else.

A new source starts on **SRT**, the protocol for multitrack. Its card shows the
SRT publish URL and its publish token straight away, and the server accepts an
SRT publish for it before you press **Apply**. Check the **protocol** on the
card. If your encoder can only send RTMP, which carries a single audio track,
pick **RTMP** and press **Apply**. The URL changes with the protocol.
**Apply** appears once you change any of the source's ingest settings, not only
the protocol, and applying restarts that source's ingest.

Until you do this, the dashboard says "no programme yet" rather than showing an
empty pipeline, and anything that acts on a programme — starting a destination,
capturing a clip — answers "this install has no source yet". That is the server
telling you which step you are on, not an error.

## 4. Point OBS at it

The **Sources** page shows the publish URL for the source you just created. Copy
it — it looks like this, and the `streamid` is that
source's publish token, which is what tells polyemesis which source you are:

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

Press **Start Recording**, not Start Streaming — with *Output to URL* that
button is the SRT push. [OBS.md](OBS.md) has the full field-by-field table,
including the `latency` unit that catches everybody.

The polyemesis dashboard should show the ingest live within a couple of
seconds, with a meter per incoming track.

## 5. Tell it what each track is

On **Sources → annotations**, label the incoming tracks: *mic*, *music*,
*commentary*, and a language where it matters.

This is optional but it is the step that makes everything after it obvious. The
labels belong to the feed, so every destination sees the same set.

## 6. Add a destination

**Destinations → Add.**

- **Platform** — pick one, and the form takes on that platform's limits.
- **URL and stream key** — from the platform's own dashboard, or leave them
  empty and pick a **Connected account** to have polyemesis fetch them; see
  [CONNECTING-PLATFORMS.md](CONNECTING-PLATFORMS.md). The key is stored
  encrypted and is never shown again or returned by the API.
- **Tracks** — tick the ones this destination should carry. This is the whole
  point: your main channel gets mic + music, the second-language stream gets
  mic + commentary, the podcast feed gets mic only.

Start it with the **Start** button on its card. That is what enabling a
destination means: the server stores that it should be running and acts on it.
There is no separate enable switch on the form. A destination you have started
before any stream arrives stays on and waits; it starts sending once the stream
is arriving and its tracks are known. Video is copied, not re-encoded, so this
costs almost nothing — add as many as you have upstream bandwidth for.

### Start, and Go live

The **Start** button on a card starts that one destination. **Go live** on the
dashboard opens a sheet that does the same for several at once. The sheet says
"Review each destination, then start the ones that are ready." Each row has two
marks. Under **Readiness**, a destination whose programme is on this install
shows **Ready**, and one without a source shows "No source for this
destination". A destination that is already **Live** shows nothing there. Under
**Progress**, a row shows **Not started**, **Starting**, **Live** or **Failed**,
or **Blocked** when it has no source. The button reads **Go live · N ready**. Pressing it starts, one after another,
each ready destination that is not already started, exactly as **Start** on that card
would. A destination that is already started is left alone.

## 7. Watch that it is actually right

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
| Keep a recording | **Recordings** — the *Recording* card on that page, not Settings |
| Stay on air when the encoder drops | **Settings → Pipeline → Failover** — off by default |
| Serve a player from polyemesis itself | **Playout** |
| Sign in to X, Twitch, Kick, YouTube or Facebook, for stream keys, go-live and chat | [CONNECTING-PLATFORMS.md](CONNECTING-PLATFORMS.md) |
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
Turn on the silence tier (**Settings → Pipeline → Synthetic audio → Add silence when the source has no audio**) and polyemesis will
synthesise a silent stereo track so your destinations work.
