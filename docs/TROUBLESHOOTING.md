# Troubleshooting

Organised by what you observe, because that is what you have when something is
wrong.

## Start here

Two commands answer a surprising share of problems:

```sh
polyemesis -log debug          # logs every child's full command line as it spawns
ffmpeg -version | head -1      # 6.0+ required
```

And one page: **Monitoring** shows each running process with its own FFmpeg
output. When a destination or an ingest misbehaves, the explanation is usually
sitting in that process's log rather than in the server log — FFmpeg's own words
go to the event bus, not to the log file.

---

## It will not start

### "ffmpeg 5.x is too old"

polyemesis refuses to start below 6.0 rather than failing later in a way that
looks like a bug. Ubuntu 22.04 ships 4.4 and Debian 12 ships 5.1, so
`apt install ffmpeg` is not a universal answer. Your options are a newer distro,
a static FFmpeg build with libsrt, or Docker —
[INSTALL.md](INSTALL.md#if-your-distro-is-too-old-pick-one-of-three) has all
three with commands.

### "listening without TLS" warning

Not fatal — a warning, and a correct one. The login form and session cookie
cross the network in clear text. Set `tls.mode: auto`, or bind to `127.0.0.1`
and use an SSH tunnel.

### Port already in use

Something else has `:8080` or the ingest port. On Linux:

```sh
ss -lntup | grep -E ':8080|:6000'
```

A previous polyemesis that did not shut down cleanly is a common cause. Note
that its **FFmpeg children are in their own process groups**, so killing the
server does not take them with it — check for strays:

```sh
pgrep -af ffmpeg
```

---

## The ingest never goes live

### Nothing arrives at all

1. **Is the port published?** In Docker, SRT is UDP: `-p 6000:6000/udp`. Missing
   `/udp` is the single most common cause.
2. **Does the mode match?** An SRT listener will not accept an RTMP publisher.
3. **Is a firewall in the way?** SRT is UDP and is often dropped by default.
4. **Does your FFmpeg have SRT?** `ffmpeg -protocols | grep -x srt`. Homebrew's
   does not. Without it, the ingest cannot listen and the log says
   `Protocol not found`.

### The publisher is refused

With one-port ingest the refusal is typed and says which:

| Reason | What it means |
|---|---|
| `REJ_BADSECRET` | Wrong passphrase, or a token that does not match any source |
| `REJ_RESOURCE` | Something is already publishing to that source |
| `REJ_UNSECURE` | A passphrase is required and none was offered |

A source that is **disabled** also refuses. That used to fail with nothing on
screen explaining it; if you are on an older build and a publisher is rejected
for no visible reason, check the source is enabled.

### It connects and then drops every few seconds

Usually the encoder and the server disagreeing about latency, or genuine packet
loss. The source's link telemetry (RTT, loss, retransmits) is on the **Sources**
page. Raise the SRT latency on both ends if loss is real.

---

## A destination produces nothing

**Open its process log on the Monitoring page first.** The platform's own
rejection is almost always there.

### "Error binding filtergraph inputs/outputs"

The routing graph references a track the incoming stream does not have. Either
the source stopped sending that track, or the destination selects a track that
was never there. Check the source's probed layout on the **Sources** page against
the destination's selection.

For a **video-only source**, this is expected until you turn on the silence tier
(**Settings → Synthetic**), which synthesises a silent stereo track so
destinations have something to select.

### It writes a file once and then never again

Fixed. Older builds passed a fixed output path to FFmpeg, which refuses an
existing file — so the first restart died with "already exists" and every one
after it did too. Current builds pick a fresh path per spawn and never overwrite
existing footage.

### The platform accepts it and then disconnects

- **Bitrate above what the platform allows.** The platform presets set limits
  the platform will actually take; a manual configuration can exceed them.
- **Keyframe interval.** Video is passed through untouched, so this is your
  *encoder's* setting, not a polyemesis one. Most platforms want 2 seconds.
- **A backwards timestamp.** A platform drops the connection on one. If this
  happens at a failover switch, that is a bug worth reporting — the failover
  suite measures exactly this and expects zero.

---

## The audio is wrong

This is what the product is for, so it is worth measuring rather than guessing.
The **Meters** page shows loudness *after* routing — what the platform actually
receives.

### One destination is silent

Its track selection includes only tracks the source is not sending. Selecting a
track that is not there gives you silence, not an error.

### The mix is quieter than the individual tracks

If you are on an old build, this was a real bug: FFmpeg's `amix` divides by the
input count by default, which cost about 9.5 dB on a three-track mix. Current
builds set `normalize=0`. If you still see it, check for a normalisation or
limiter setting on the destination profile.

### Audio and video are out of sync

Set the per-destination audio delay. A *negative* delay pulls audio ahead of
picture, which is done by shifting the video instead — no audio filter can move
sound earlier than it arrived.

### Loudness is not hitting the target

The measurement is post-routing and needs a minimum integration time before it
means anything; a reading taken in the first few seconds is not yet meaningful.
Check the destination's target matches the platform you are sending to.

---

### A clip is up to a second longer than I asked for

Check your FFmpeg major version. This changed between the versions polyemesis
supports, and the difference is a whole GOP rather than rounding — measured on a
one-second-GOP source, asking for a 4.4s clip:

| FFmpeg | Result |
|---|---|
| 6.1.2 | **5.402s** — keeps the packets through the end of the GOP containing the out point |
| 8.1.2 | 4.423s — stops at the out point |

Neither is wrong: a stream copy can only cut on packet boundaries, and which
side of the boundary to land on is a choice. But on **FFmpeg 6.x a copied clip
can run up to one GOP long**, and that applies to precise mode too — precise
re-encodes only the *head* and copies the tail.

It only shows up when the out point falls mid-GOP. An out point that lands on a
keyframe is exact on both.

If you need exact out-points, use FFmpeg 8.x.


## Recordings and jobs

### Recording stopped on its own

The free-space guard halts recording rather than filling the disk. The
**Recordings** page reports the state and the reason. It resumes when space is
available.

### A job never runs

The governor defers work under load — this is deliberate, so post-production
does not compete with a live broadcast. **Jobs → overview** shows what is
queued and why. Check the queue is not paused.

### Downloads 404

The file was swept by retention between the page listing it and you clicking it.
Expected, and a 404 rather than an error for that reason.

---

## Docker

### The container is healthy but nothing reaches it

Publish the ingest port with the right protocol. SRT is UDP, RTMP is TCP:

```sh
-p 8080:8080 -p 6000:6000/udp -p 1935:1935
```

### Recordings vanish on restart

`/data` is not on a volume. One mount covers everything — the database, the key,
recordings and TLS material all live under it.

### Shutdown takes a while

Up to about 30 seconds, and that is on purpose: recordings are finalised on the
way down. `stop_grace_period: 30s` is set in the compose file. Forcing a shorter
timeout truncates the recording you were making.

---

## Still stuck

Open an issue with the bug template. The most useful report has
`polyemesis -version`, `ffmpeg -version`, the ingest mode, the relevant process
log from the Monitoring page — and for anything about audio or timing, what you
*measured* rather than what it sounded like.

Check for stream keys and passphrases before pasting logs.
