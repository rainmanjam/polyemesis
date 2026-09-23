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

**An ingest port that cannot be bound does not stop the server** — which is why
this symptom does not look like a start-up failure at all. A listener that fails
to bind is logged at `ERROR` and skipped; the web UI comes up, the other
protocol's listener comes up, and the install simply has no ingest on the
protocol that lost:

```
level=ERROR msg="one-port ingest could not start" proto=rtmp addr=:1935 err="listen tcp :1935: bind: address already in use"
level=ERROR msg="ingest not started: listener port out of range" proto=srt port=0
```

That is deliberate: an install whose `1935` is held by something else should
still ingest over SRT. The cost is that the only evidence is that one line, and
`ss -lntup` shows the port held by the *other* process with nothing to say that
polyemesis wanted it. If an encoder cannot connect on one protocol while the
server is otherwise healthy, grep the server log for `one-port ingest`.

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
5. **Not this: your FFmpeg's SRT support.** `ffmpeg -protocols | grep -x srt`
   is worth running before you configure an SRT *destination* — Homebrew's
   build has no libsrt and cannot dial one — but it has nothing to do with
   **ingest**. The SRT listener is a Go server inside polyemesis
   (`datarhei/gosrt`), not an FFmpeg child, so an FFmpeg without libsrt accepts
   publishers perfectly well. An older build did spawn FFmpeg here as well as
   the Go listener, and on a host lacking libsrt that child's
   `Protocol not found` hid the real problem: two things trying to bind one
   port. Installing a libsrt build cannot change an ingest symptom.
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
| `REJ_BADSECRET` | Wrong passphrase, a token matching no source, or a peer currently rate-limited for repeated wrong tokens (see below) |
| `REJ_CLOSE` | The source exists but is disabled |
| `REJ_RESOURCE` | Something is already publishing to that source, no pipeline is running for it, or its ingest is not set to SRT (RTMP, pull, or not chosen yet) |
| `REJ_ROGUE` | The `streamid` is empty or over the length limit |
| `REJ_UNSECURE` | The source requires a passphrase and none was offered — or the publisher encrypted and the source has no passphrase set |

A token that does not exist and a token for a source that does not exist give
the same answer deliberately, so a caller cannot use the refusal to enumerate
sources. Neither token value is ever logged.

The `REJ_RESOURCE` cases are worth telling apart, and the server log does:
*already publishing* names the incumbent peer, *no SRT pipeline for source*
means nothing is running to receive it over SRT — the source's engine is down,
or its ingest is set to RTMP or pull, or no protocol has been chosen for it. A
source created from its name alone has none chosen: pick **SRT** on its card
before publishing. (Earlier releases admitted an SRT publish into any source,
whatever its mode, which put a second writer into an RTMP or pull source's
stream.)

#### The third `REJ_BADSECRET`: the peer is rate-limited

Wrong tokens are counted per peer address, and **five of them inside 30 seconds
gets that address refused outright for the next 5 seconds** — `REJ_BADSECRET`,
before the token is even looked at. So a `REJ_BADSECRET` does not always mean
the credential in front of you is wrong; it can mean the last handful were, and
this attempt never got a hearing.

The details that decide whether this is what you are looking at:

- **Only an unrecognised token counts.** Every refusal past that point —
  disabled source, nothing running to receive it, encrypted against a source
  with no passphrase — has already proved the caller holds a real token, and
  does not feed the counter.
- **It is scoped by address, with the port stripped**, so every retry from one
  encoder lands on the same counter. An encoder behind NAT shares that counter
  with everything else behind the same address.
- **One success clears it.** A peer that authenticates has its penalty deleted,
  so an operator who fat-fingers a token and then fixes it carries nothing
  forward.
- **5 seconds, not a lockout.** This is a speed bump against automated guessing,
  not something anyone has to go and clear.

**At the default log level it is almost silent, which is the trap.** Crossing
the threshold is logged once, at `WARN`:

```
level=WARN msg="srt: peer rate-limited after repeated wrong tokens" peer=203.0.113.7:51234
```

Every refusal made *during* the 5 seconds is `DEBUG` only, so unless you started
with `-log debug` you see nothing:

```
level=DEBUG msg="srt connect refused: peer is rate-limited after repeated wrong tokens" peer=203.0.113.7:51234
```

The individual wrong tokens that got you there are `WARN`
(`srt publish refused: token not recognised`), so the sequence to look for is a
run of those followed by the rate-limit line.

**Why this matters for the advice further down.** A rate-limited encoder
produces server-side silence at the default log level, which
[the macOS section](#macos-an-ipv4-publisher-times-out-and-nothing-is-logged--fixed)
and [the loss section](#it-connects-on-some-attempts-and-not-others) both teach
you to read as "no connection was ever offered". It is not the same thing, and
the remedy is the opposite one: an encoder retrying on a stale token — a
rotation it did not pick up, say — is feeding the counter with every reconnect,
so turning auto-reconnect *on* makes it worse. Fix the token first; a peer that
has stopped guessing is out of the penalty window 5 seconds later.

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

One thing produces the same silence without being loss: a peer being
[rate-limited for repeated wrong tokens](#the-third-rej_badsecret-the-peer-is-rate-limited),
whose refusals are logged at `DEBUG` only. Before you conclude the link is
lossy, run the server with `-log debug` for one reconnect and check.

**What to do:** retry, and turn on your encoder's auto-reconnect so it retries
for you — provided the token is right. On a stale token auto-reconnect is the
wrong move, because every attempt feeds the rate limiter. At 10% loss a second attempt is very likely to succeed where the first
did not, and once it does the connection will carry the stream.

Raising SRT latency does not help here. Latency sizes the receive buffer on an
established connection, which is exactly the part that was already surviving 20%
loss; it does not protect the handshake. Raise it for the symptom in the section
above, not this one.

---

## A destination produces nothing

**Open its process log on the Monitoring page first.** The platform's own
rejection is almost always there.

### Nothing starts for up to 75 seconds after the ingest goes live

**Destinations are held down until the ingest's channel layout has been
measured**, and while they are held there is no process — so the Monitoring page
has no log to show you, and the only account of it is in the server log.

The reason is that a destination's routing matrix is written against the tracks
the source is actually sending. Starting one on a guessed layout produces a
destination that is live, green, and mixing the wrong things, which is a worse
outcome than a destination that is thirty seconds late. So the probe runs first:
every 3 seconds while bytes are flowing, each attempt bounded at 10 seconds.

The first failure says so plainly:

```
level=WARN msg="ingest probe failed; destinations are held until a layout is measured" err="..." source=1
```

**The hold has two exits, and neither of them is "wait forever".** Five
consecutive failures ends it, which is about 65 seconds on a stream where probes
are actually being attempted back to back; and a 75-second wall-clock ceiling
ends it for the case a consecutive-failure count cannot see, where probes are
not being attempted at all. Whichever fires, destinations then start — but **on
a runtime downmix rather than their configured routing matrices**:

```
level=WARN msg="ingest layout cannot be measured; starting destinations with a runtime downmix instead of their routing matrices" failures=5 err="..." source=1
```

That line is the whole explanation for the second symptom: destinations that
came up carrying audio nobody routed. If you see it, the routing editor is not
lying to you and nothing is misconfigured — the layout underneath it was never
established. Fix the probe failure in `err` and the next successful reconcile
puts the real matrices back.

A source that has **already** been probed once is not held again if a later
probe fails: the layout in use is simply the last one measured, and the log says
so — `ingest probe failed; keeping the layout already measured` at `INFO`, then,
after five in a row, `ingest probes keep failing; the layout in use is the last
one measured and may no longer match the stream` at `WARN`. That second line is
worth acting on if the source changed its track count.

### An upload is refused

The Library probes every upload before it is stored under its final name, and
refuses anything ffprobe cannot read as media. Usually the message is ffprobe's
own: `Invalid data found when processing input` means the file is not what its
name says, and `moov atom not found` means an MP4 whose end is missing.

Four refusals are polyemesis's own words rather than ffprobe's:

- **"this file carries no video or audio stream"** — ffprobe read the container
  and found nothing playable in it. A renamed archive arrives this way.
- **"this file is a playlist or script naming other files, not media itself"** —
  the file is an ffconcat script, an HLS playlist or similar. These are refused
  even though ffprobe reports streams for them, because the streams belong to
  the files they NAME. A two-line, 44-byte text file would otherwise be stored
  with another video's codecs, resolution and duration shown as its own.
- **"polyemesis cannot work out how long this file is"** — the container is one
  we accept, but its length could neither be read from the file nor counted by
  decoding it. It reaches you inside the generic wrapper, with the format
  ffprobe saw and the reason the count failed:

  ```
  this file could not be read as media: polyemesis cannot work out how long this
  file is (ffprobe read it as "matroska,webm" and reported no duration, and it
  could not be counted: ...; re-save it as MP4 or MPEG-TS and upload it again)
  ```

  This is a verdict about the file, not a corrupt container — the remedy in the
  message is the remedy.
- **"ffprobe printed more about this file than polyemesis will read"** — ffprobe
  ran fine and produced more than 8 MiB of JSON about one file, which no correct
  media does. The reply would have to be parsed as a fragment, so it is refused
  instead: `... (over 8388608 bytes)`.

The last two arrive prefixed with **"this file could not be read as media: "**,
which is the wrapper the upload path puts around any refusal it has no sentence
of its own for. Do not read that prefix as ffprobe's words here — everything
after the colon in those two is polyemesis's verdict.

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
  accepted", so **false covers three situations with different remedies** — and
  `outcome` is the field that tells them apart. Here are all four of its values:

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

**The remedy is the "Check again" button** on the row, in the Library's media
list. It queues a `media.verify` job that re-runs the same inspection against
the copy already on disk — nothing is uploaded, and no bytes have to be found
again. The request returns as soon as the job is queued, so the row does not
change on the press; the verdict appears on the next refresh, and the job is
listed on the Jobs page like any other. Re-uploading works too, but it is not
required, and for a file that is the operator's only copy it may not be
possible.

A verdict is never invented: the button re-reads the file, so what it records is
a check that was actually performed. The button is offered only where a re-check
can change the answer — a file already `refused` has had a verdict, and a second
identical read is not a remedy.

**Both outcomes are written.** The re-verify job writes `verified` when the file
passes and `refused` when it does not, so `refused` is a state you will see on a
stored file rather than a shape only the upload handler could produce. The
upload handler still answers `400` and discards the staged bytes for a file that
never landed, which is right — nothing references a file that was never
published.

One thing to know about pull sources: an unchecked file's pull URL is **gated**.
Introducing a pull source that names an upload carrying "nobody read this" is
refused by the settings API itself, not merely warned about in the UI — which
matters because a pull source configured by automation from a listing never sees
a row. What remains open under issue #201 is the bigger fix: pushing the format
allowlist into the engine's own file-input path, so there is one gate instead of
a per-consumer list.

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

#### "polyemesis does not accept this container format"

The message in full is **"polyemesis does not accept this container format;
re-save it as MP4 or MPEG-TS"**, which is what to search this page for if you
are holding one.

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
(**Settings → Synthetic audio**), which synthesises a silent stereo track so
destinations have something to select.

### It writes a file once and then never again

Fixed. Older builds passed a fixed output path to FFmpeg, which refuses an
existing file — so the first restart died with "already exists" and every one
after it did too. Current builds pick a fresh path per spawn and never overwrite
existing footage.

### Extra FFmpeg arguments are refused, or quietly are not applied

A destination's expert boxes — **extra input arguments** and **extra output
arguments**, `extraInputArgs` and `extraOutputArgs` over the API — take one
pasted line each and split it into an argv. **No shell is ever involved**: the
tokens go straight to `os/exec`. That is the whole reason for the rules below,
because an argument list written as though a shell would read it does something
other than what its author expects.

What the splitter does:

| Rule | Detail |
| --- | --- |
| Quoting | single or double quotes, which is how a space gets into one argument |
| Backslash | **not an escape** — it is a path separator, so `C:\media\out` survives intact |
| Rejected outside quotes | `;` `\|` `&` `$` `` ` `` `<` `>` |
| Deliberately allowed | `*` `?` `[` `]` `{` `}` `~` `!` `#` — filter-graph syntax and the optional-stream suffix in `-map 0:a:1?` live in these |
| Control characters | a NUL, CR or LF anywhere is refused outright |
| Limits | 2000 characters and 64 arguments per box |

So this is accepted, because the `&` is inside quotes:

```
-metadata "title=Rock & Roll" -muxdelay 0
```

and this is refused, naming the box it came from:

```
input args: contains the shell metacharacter "&". These arguments are handed to
FFmpeg directly and never reach a shell, so "&" would be passed through as a
literal character rather than doing what it does in a terminal. Remove it, or
quote it if it really is part of a value.
```

The other four read the same way: `output args: has an unclosed '"' quote`,
`input args: contains a control character`,
`output args: too long (2431 characters, limit 2000)`, and
`input args: has 71 arguments, limit 64`.

**A stored value that no longer parses is dropped rather than failing the
destination**, and this is the case worth knowing about, because nothing goes
red. The API validates on the way in, so anything unparseable at spawn time was
stored before the rules were what they are now. The destination starts on its
generated command — live, green, and running without the arguments the operator
believes are applied. The editor shows the stored text with the reason it will
not apply, and the server log says it once per start:

```
level=WARN msg="ignoring unparseable expert arguments" dest="Twitch main" field=output err="has an unclosed '\"' quote"
```

`field` is `input` or `output`. Re-save the destination with the argument list
corrected and it applies again.

### The platform accepts it and then disconnects

- **Bitrate above what the platform allows.** The platform presets set limits
  the platform will actually take; a manual configuration can exceed them.
- **An HEVC or AV1 ingest going to an RTMP destination.** Selecting HEVC or AV1
  in OBS produces Enhanced RTMP, which polyemesis ingests happily — the RTMP
  listener does not parse media — and video is then **stream-copied end to
  end**, so HEVC in means HEVC out to every destination. FFmpeg muxes it into
  FLV without complaint. Most mainstream RTMP ingests take H.264 only, so the
  stream uploads cleanly and the platform drops it. This is the worst-shaped
  failure available: it looks correct everywhere you can see. polyemesis says so
  in the **server log**, the moment the ingest layout is known:

  ```
  level=WARN msg="the ingest video codec is not one this platform accepts; the stream will upload and be rejected" dest="Twitch main" platform=twitch ingestCodec=hevc accepts=h264
  ```

  It is a warning rather than a refusal because the video codec is not a setting
  you picked in polyemesis — it is whatever the encoder sends, discovered at
  probe time, long after every destination was saved. There is no save to
  refuse. Support is recorded for four platforms: Twitch, Facebook and Kick take
  `h264` only, YouTube takes `h264`, `hevc` and `av1` — so the same encoder
  setting is a problem on three of them and not on the fourth, and no warning is
  raised for the destination that is fine. For any other platform you get the
  honest version rather than a guess:

  ```
  level=WARN msg="the ingest video codec is not H.264 and this platform's support is not recorded; if it is rejected, this is why" dest="Custom RTMP" platform=custom ingestCodec=av1
  ```

  The fix is in the encoder: send H.264.
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

**If the ports are published and the UI is still unreachable, check whether you
overrode `command:`.** The image's own `CMD` is
`["-addr", ":8080", "-data", "/data"]`, and a `command:` in Compose replaces it
outright rather than adding to it. The binary's built-in default for `addr` is
`127.0.0.1:8080` — loopback, inside the container — so a `command:` that leaves
`-addr` out publishes a port to a listener no packet from outside the container
can reach. Carry both flags whenever you override it:

```yaml
command: ["-addr", ":8080", "-data", "/data", "-log", "debug"]
```

### Recordings vanish on restart

`/data` is not on a volume. One mount covers everything — the database, the key,
recordings and TLS material all live under it.

### Shutdown takes a while

Seconds, and that is on purpose: recordings are finalised on the way down. The
supervisor gives each child 8 seconds to exit before escalating to `SIGKILL`
(1 second for the children that write nothing — meters, loudness, silence), and
a measured shutdown with three destinations live takes about 8.3s. Forcing a
shorter timeout truncates the recording you were making, and a truncated
Matroska file is exactly the right size on disk — nothing reports it, and you
find out on playback.

**The compose file's `stop_grace_period: 30s` is 5 seconds shorter than the
process's own budget, not equal to it.** polyemesis works to a single shutdown
deadline of **35 seconds**, sized as the systemd unit's `TimeoutStopSec=45` less
a 10-second margin for systemd to observe a clean exit and for the last log
lines to flush. A normal shutdown never approaches that, which is why 30s has
been fine in practice — but a shutdown that actually used its full budget, one
wedged child being the way to get there, is killed by Docker at 30s with five
seconds of its own budget left.

If you want Docker to cover the full budget, raise it to the same number the
systemd unit uses:

```yaml
services:
  polyemesis:
    stop_grace_period: 45s
```

---

## Still stuck

Open an issue with the bug template. The most useful report has
`polyemesis -version`, `ffmpeg -version`, the ingest mode, the relevant process
log from the Monitoring page — and for anything about audio or timing, what you
*measured* rather than what it sounded like.

Check for stream keys and passphrases before pasting logs.
