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
3. **Is the address right for *that* source?** Both listeners are shared, so the
   token (SRT) or the stream key (RTMP) is what picks the source out — a valid
   URL with the wrong key reaches the server and is refused, it does not fall
   through to whichever source happens to be there. On RTMP the log line is
   `rtmp publish refused` and it deliberately names **no source**: "no such
   key", "that source is disabled" and "that source has no engine" are three
   different facts, and telling them apart is what would let someone enumerate
   which keys exist. Copy the URL and key from **Sources** rather than retyping
   them.
4. **Is a firewall in the way?** SRT is UDP and is often dropped by default.
5. **Does your FFmpeg have SRT?** `ffmpeg -protocols | grep -x srt`. Homebrew's
   does not. Without it, the ingest cannot listen and the log says
   `Protocol not found`.
6. **Are you on macOS with a bare `:port`?** See directly below — this one looks
   exactly like a firewall and is not one.
7. **Is the link lossy?** A handshake can fail on loss that an established
   stream would shrug off, and it fails with a bare `I/O error` that names
   nothing. See [It connects on some attempts and not
   others](#it-connects-on-some-attempts-and-not-others).

### macOS: an IPv4 publisher times out and nothing is logged — FIXED

**This no longer happens.** It is kept here because the symptom was distinctive
and someone running an older build will still meet it.

The distinguishing symptom was the **silence**. Every refusal polyemesis makes is
typed and logged, so a publisher failing with an `I/O error` while the server log
said nothing at all had not reached the handshake — no refusal was made, because
no connection was ever offered.

On macOS a bare `:6000` accepted **IPv6 publishers only**. Datagrams from an IPv4
caller arrived — a plain listener on the same address received them — but the SRT
handshake never completed:

| Listen address | Caller | Linux | macOS (before the fix) |
|---|---|---|---|
| `:6000` | IPv4 | ok | **times out** |
| `:6000` | IPv6 | ok | ok |
| `0.0.0.0:6000` | IPv4 | ok | ok |
| `127.0.0.1:6000` | IPv4 | ok | ok |

The cause is upstream, in `datarhei/gosrt`: a reply to a v4-mapped peer goes out
through `golang.org/x/net`'s `ipv4.PacketConn` carrying an IPv4 control message
on an `AF_INET6` socket, Darwin rejects that combination with
`sendmsg: invalid argument`, and `packetConn.writeToFrom` has no error return —
so the failure is discarded. Reported as
[datarhei/gosrt#148](https://github.com/datarhei/gosrt/issues/148).

**polyemesis no longer takes that path.** gosrt chooses its network from the
address it is given — an empty host becomes a dual-stack `udp`, a v4 literal
becomes `udp4`, a v6 literal becomes `udp6` — and only the first is affected. So
a wildcard address now binds `0.0.0.0` and `::` as two separate listeners, which
between them accept both families on every platform. Two sockets can share the
port because Go sets `IPV6_V6ONLY` on the `udp6` network.

If one family cannot be bound — a host with IPv6 disabled, say — the other still
serves and the reason is logged. Only losing both is fatal.

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

### It connects on some attempts and not others

Retrying works, and that is the fix. The reason it works is worth knowing,
because the numbers say loss is hurting one specific moment rather than the link
as a whole.

**Once the connection is established, the data path is very robust.** Measured
against a live server over 40s runs — 1200 kbit/s video plus three audio tracks —
with sender-side loss applied 8s in, so every handshake completed on a clean
link:

| Sender-side condition | Publisher live | Packets lost | Delivered |
|---|---|---|---|
| clean | 100% of samples | 0 | 6.1 MB |
| 2% loss | 100% | 199 | 6.1 MB |
| 5% loss | 100% | 463 | 6.1 MB |
| 10% loss + 120ms±40ms jitter | 100% | 4766 | 5.9 MB |
| 20% loss | 100% | 1788 | 6.1 MB |

The publisher never left the live state in any condition, and all three audio
tracks kept metering throughout every one. At 20% loss the stream still
delivered the same 6.1 MB as a clean link. The jittered row is the only one that
lost ground — RTT rose to 89.8ms and delivered bytes fell about 3% — and it is
also the only row with jitter. Counts here are not monotonic in loss and should
not be read as if they were.

**The handshake is the weak point.** Same conditions, but with loss present from
the first packet so that it applies during connection setup — six attempts each:

| Condition | Connected |
|---|---|
| clean | 6/6 |
| 2% | 6/6 |
| 5% | 6/6 |
| 10% | 4/6 |
| 20% | 4/6 |

The asymmetry is structural rather than a defect. A data stream is thousands of
packets protected by retransmission that has already been negotiated, so losing
one costs a retransmit. A handshake is a handful of packets exchanged before any
of that machinery exists, so losing one loses the whole attempt.

A failed attempt surfaces at the publisher as nothing more than:

```
Error opening output files: I/O error
```

That is FFmpeg's message, not polyemesis's, and it names neither SRT, nor loss,
nor suggests retrying. The server side is what tells you which failure you have.
A handshake that never completed produces **no refusal at all** — nothing is
logged, because nothing was refused. If instead you get a typed `REJ_` reason,
loss is not your problem and [the refusal table](#the-publisher-is-refused) is.

**What to do:** retry, and turn on your encoder's auto-reconnect so it retries
for you. At 10% loss a second attempt is very likely to succeed where the first
did not, and once it does the connection will carry the stream.

Raising SRT latency does not help here. Latency sizes the receive buffer on an
established connection, which is exactly the part that was already surviving 20%
loss; it does not protect the handshake. Raise it for the symptom in the section
above, not this one.

---

## A destination produces nothing

**Open its process log on the Monitoring page first.** The platform's own
rejection is almost always there.

### An upload is refused

The Library probes every upload before it is stored under its final name, and
refuses anything ffprobe cannot read as media. Usually the message is ffprobe's
own: `Invalid data found when processing input` means the file is not what its
name says, and `moov atom not found` means an MP4 whose end is missing.

Two refusals are polyemesis's own words rather than ffprobe's:

- **"this file carries no video or audio stream"** — ffprobe read the container
  and found nothing playable in it. A renamed archive arrives this way.
- **"this file is a playlist or script naming other files, not media itself"** —
  the file is an ffconcat script, an HLS playlist or similar. These are refused
  even though ffprobe reports streams for them, because the streams belong to
  the files they NAME. A two-line, 44-byte text file would otherwise be stored
  with another video's codecs, resolution and duration shown as its own.

This is stricter than it used to be. The extension list was never a gate: an
unrecognised extension was stored as `.bin` and listed as media anyway, so a
PDF or a zip could sit in the Library looking like a video until a playlist
normalise job failed on it — or until it reached air.

A file the server accepts but you cannot play locally is worth checking the
other way round: **your** player may lack a codec this FFmpeg has.

#### Truncation is mostly NOT caught

Do not read the check as a completeness guarantee, because it is not one.
`moov atom not found` only appears for an MP4 whose index sits at the end of
the file, which is the default layout. Measured on the FFmpeg this repository
builds against:

| file, cut to 10% of its length | result |
| --- | --- |
| MP4, default layout | **refused**, `moov atom not found` |
| MP4 written with `-movflags +faststart` | **accepted**, and the Library shows the ORIGINAL duration |
| Matroska (`.mkv`) | **accepted**, and the Library shows the ORIGINAL duration |

So a partial download of a faststart MP4 or an MKV is accepted and listed as
ten minutes long while holding one. The check answers "is this media", not "is
this all of it". `internal/ffmpeg.TestProbeFileAcceptsMostTruncatedMedia` pins
each of the three rows above.

#### When the check is skipped — and what the file looks like afterwards

**The check is skipped rather than failed when it cannot run**, and the upload
is stored *unchecked* rather than refused. Refusing every upload in any of these
cases would be a worse outage than the one it guards against — and deleting the
file, which is what the first version of this did on a disconnect, destroys a
transfer that had already completed.

There are five ways it happens:

| what happened | why the file is kept |
| --- | --- |
| the server has no `ffprobe` | nothing to judge with |
| it has no running engine (an install whose video pipeline will not build logs the reason and keeps serving) | nothing to judge with |
| **the client disconnected while the probe was running** | the transfer completed; the inspection did not |
| the probe took longer than 30 seconds | a slow disk must not delete valid media |
| the probe could not be started, or printed something the server could not read | a fork that failed is a fact about this server, not about your file |

The third row is the one worth understanding, because **it is under the
caller's control, not the server's**. The check runs while the request is still
open, so anything that ends the request early — a dropped connection, a proxy
timeout, a browser tab closing — ends the check. It is not only an operational
condition; a client that sends a complete body and then hangs up gets the file
stored with nothing having read it, on purpose if it likes.

So the state is **recorded**, not merely logged:

- `GET /api/v1/media` carries `"verified": false` for it, always present, plus
  `unverifiedReason` saying which row above applies. A file that passed carries
  `"verified": true` and its `media` block. **`media` being absent is not the
  signal** — that is also how an upload from before this feature looks.
- `GET /api/v1/media` also carries `"outcome"`, always present, and it is the
  field to branch on. `verified` is still true only for "inspected and
  accepted", but **false covers four situations with different remedies**:

  | `outcome` | what it means | what to do |
  | --- | --- | --- |
  | `verified` | inspected and accepted | nothing |
  | `unverified` | this server produced no verdict (one of the five rows above) | upload it again |
  | `refused` | the bytes **were** inspected and are not media this server takes | replace the file; re-sending it changes nothing |
  | `unrecorded` | nothing was ever written about this file — every upload stored before verdicts existed | nothing; the normalise worker re-checks it at the moment of use |

  `unrecorded` is not stored anywhere: it is what the listing says when there is
  no `.probe-` record beside the file, and it stays distinct from every recorded
  state because refusing those uploads would strand media an operator has had
  for a year.
- The Library shows a **Not checked** marker on the row, or **Refused** for the
  `refused` state — never both, and never the first for the second.
- A settings save that **adds** such a file to a playlist is refused, naming the
  file and telling the operator to upload it again — or, for a `refused` file,
  saying it was inspected and refused and that sending it again will not change
  that. Items already in the stored playlist are not refused — see below.
- The normalise worker re-runs the same format check on whatever it is handed
  before it transcodes anything, so an item that reaches it by any other route
  is caught there instead.

The remedy is to upload the file again on a connection that stays up. There is
no way to mark a stored file as checked without re-uploading it, deliberately:
the server would be recording a pass it did not perform. A job that re-runs the
check against a file already on disk is issue #202.

**Nothing writes `refused` yet.** The upload handler still answers `400` and
discards the staged bytes, which is right — nothing references a file that was
never published. The state exists because anything that inspects an upload
*later* cannot do that: the file is published by then, `DELETE` answers `409`
while a playlist item names it, so the refusal has to be recorded — and until
this state existed the only way to record it was as `unverified`, which every
consumer answers by telling you to upload the same bytes again. The re-verify
job of #202 is the first writer.

One thing this does **not** cover: an unchecked file's pull URL still works, so
pasting it into a pull source bypasses all of the above. That is issue #201.

**Every one of them also writes a WARN line naming the upload:**

```
level=WARN msg="no ffprobe available; accepting this upload unchecked" reason="this install reports no ffprobe binary" name=show-629507cb.mkv
level=WARN msg="upload probe was interrupted; accepting the file unchecked" name=show-4aee482d.mkv cause="context deadline exceeded" err="signal: killed"
level=WARN msg="the upload probe could not be run; accepting the file unchecked" name=show-1f0c22a1.mkv err="fork/exec /usr/bin/ffprobe: no such file or directory"
level=WARN msg="an upload was stored without being inspected" name=show-4aee482d.mkv reason="the inspection was cut short before it finished"
```

`name` is the stored filename, so it matches what the Library shows.

The startup log will not tell you: it prints ffmpeg's version and path and says
nothing about ffprobe, and there is no "engine came up" line to look for. Grep
the running server's log for `unchecked`.

#### "polyemesis cannot use this container"

The file is real media in a format the upload path does not accept — the check
is an allowlist of containers whose streams live in the bytes we were handed,
so a legitimate AIFF, y4m, IVF or GIF is refused by it. Re-save it as MP4 or
MPEG-TS.

This is a different refusal from *"this file is a playlist or script naming
other files"*, which means the opposite thing: ffprobe read the file perfectly
and reported some **other** file's streams as its own. The two used to share one
message, so an operator refused a DV file was told to go looking for a script
that did not exist.

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

### The second (VOD) audio mix is not arriving

> **EXPERIMENTAL** — on Twitch this depends on Enhanced Broadcasting. The
> negotiation is proven against `ingest.twitch.tv`; a broadcast *published
> through* the key it mints is not. If you get to the end of this list and the
> track still is not there, that is the unobserved step, and an issue with the
> destination card's message in it is worth more than anything on this page.

polyemesis says which of these it was on the **destination's card**, once the
destination has gone live at least once. Check there first — it names the reason
rather than making you guess between them.

1. **Enhanced Broadcasting is off for this destination.** The ordinary Twitch
   RTMP ingest carries one audio track, so the engine refuses the pair before
   the broadcast starts and publishes the live mix alone. Switch it on in the
   destination's settings. The routing editor also says so, in the second-mix
   card, before you save.
2. **No GPU inventory is declared.** Twitch refuses the negotiation outright for
   a client that reports none — *"did not send GPU Information"* — and it checks
   the inventory it is *sent*, not the hardware (see
   [ENCODING.md §2](ENCODING.md#2-the-gpu-has-two-unrelated-jobs-and-only-one-is-a-workload)).
   Fill it in on the Settings page. A declared GPU Twitch does not recognise, or
   a driver version it considers out of date, is refused by name too.
3. **The ingest has not been probed yet.** The second mix is dropped on *every*
   platform while the channel layout is a guess — the live mix is already
   running provisionally and a second guessed mix is not stacked on top of it.
   It returns by itself on the first reconcile after a probe succeeds; nothing
   to do.
4. **The far end is not Twitch and takes one track.** Off Twitch nothing is
   negotiated: both mixes are published and whether the second is accepted is a
   property of that ingest. Many RTMP ingests ignore or reject it.

A refusal at any of these never fails the broadcast: the destination falls back
to publishing the live mix alone to the ordinary ingest.

### An NVENC / QSV / VA-API rendition opens but the bitrate looks wrong

> **EXPERIMENTAL** — the command-line flags polyemesis hands NVENC, QSV, VA-API
> and AMF encoders were read out of FFmpeg's own option tables rather than
> measured on silicon. VideoToolbox and the software encoders are not in that
> set. See [ENCODING.md § Per-encoder flags](ENCODING.md#per-encoder-flags).

If the encoder *opens* and produces picture, the argv is at least valid — an
invalid flag value refuses to open and the start gate reports it by name. What
is not established is whether the rate control then behaves as the numbers in
the editor say, and the capped-VBR path (a bitrate ceiling above the target) is
where that is most likely to show. Compare the measured output bitrate against
the target you set; if they disagree, the fallback that works today is to set
the ceiling equal to the target, which asks for CBR. Please report it — this is
the exact evidence gap the label exists to mark.

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
