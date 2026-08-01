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
5. **Are you on macOS with a bare `:port`?** See directly below — this one looks
   exactly like a firewall and is not one.

### macOS: an IPv4 publisher times out and nothing is logged

The distinguishing symptom is the **silence**. Every refusal polyemesis makes is
typed and logged, so a publisher that fails with an `I/O error` while the server
log says nothing at all did not reach the handshake — no refusal was made,
because no connection was ever offered.

On macOS a bare `:6000` accepts **IPv6 publishers only**. The datagrams from an
IPv4 caller do arrive — a plain listener on the same address receives them — but
the SRT handshake never completes. Measured against gosrt v0.11.0 on GitHub's
hosted runners, so this is not one machine's firewall:

| Listen address | Caller | Linux | macOS |
|---|---|---|---|
| `:6000` | IPv4 | ok | **times out** |
| `:6000` | IPv6 | ok | ok |
| `0.0.0.0:6000` | IPv4 | ok | ok |
| `127.0.0.1:6000` | IPv4 | ok | ok |

**The fix is to bind `0.0.0.0:6000`**, or to publish over IPv6. The server prints
this warning at startup on macOS when the address is a bare `:port`, so it is
worth scrolling back to the first few lines of the log.

**Linux is unaffected in every case**, which is what the container images and
every documented deployment run. The default is deliberately not changed to
`0.0.0.0`: that binds IPv4 only, which would silently drop IPv6 publishers on the
platform that actually ships, to fix one where the problem is a development
inconvenience. Tracked in [issue #28](https://github.com/rainmanjam/polyemesis/issues/28);
the underlying behaviour is upstream in `datarhei/gosrt`, not in polyemesis.

### The publisher is refused

With one-port ingest the refusal is typed and says which:

| Reason | What it means |
|---|---|
| `REJ_BADSECRET` | Wrong passphrase, or a token matching no source |
| `REJ_CLOSE` | The source exists but is disabled |
| `REJ_RESOURCE` | Something is already publishing to that source, or no pipeline is running for it |
| `REJ_ROGUE` | The `streamid` is empty or over the length limit |
| `REJ_UNSECURE` | The source requires a passphrase and none was offered — or the publisher encrypted and the source has no passphrase set |

A token that does not exist and a token for a source that does not exist give
the same answer deliberately, so a caller cannot use the refusal to enumerate
sources. Neither is ever logged.

The two `REJ_RESOURCE` cases are worth telling apart, and the server log does:
*already publishing* names the incumbent peer, *no pipeline for source* means
the source is enabled but nothing is running to receive it.

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

## Recordings and jobs

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

### Recording stopped on its own

The free-space guard halts recording rather than filling the disk. The
**Recordings** page reports the state and the reason. It resumes when space is
available.

### Windows: the last part of a recording is missing after a service stop

Known defect, and the loss is bounded rather than total.

The graceful stop is a `CTRL_BREAK` console event. A Windows **service** has no
console for it to travel through, so the supervisor terminates the recorder
instead of asking it to finish, and FFmpeg never writes the container index for
the segment it was filling. The service logs this warning at every start, under
event ID 3 in the Event Viewer.

**Only the in-progress segment is affected.** The recorder writes segmented MKV,
so every segment that had already rolled over is finalised and plays normally. A
shorter `recording.segmentSeconds` puts less at risk each time.

**To avoid it:** stop recording from the UI and wait for it to appear in the
**Recordings** list before stopping the service. Running polyemesis from a
console rather than as a service is unaffected — there the `CTRL_BREAK` is
delivered and the recording is finalised.

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
