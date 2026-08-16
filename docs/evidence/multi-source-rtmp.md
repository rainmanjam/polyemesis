# Multi-source RTMP: the design record

> **Built, 2026-08-06.** This started as a note asking what multi-source RTMP
> would take. It is kept as the record of the decision because it holds the
> measurements the choice rested on and the options that were rejected —
> `DESIGN-ONE-PORT-ONLY.md#rtmp` is the shorter statement of what shipped and is
> the one to read first.
>
> What shipped: `internal/rtmpserver`, one TCP port, sources addressed by the
> stream key in the publish URL, no per-source ports of any kind, media never
> decoded. `checkRTMPExclusive` is gone.
>
> Sections below are left in their pre-decision tense where that is what makes
> the reasoning legible; the outcome of each is marked.

## What was actually in the way

Nothing about RTMP. The URL carries `app/streamkey` and every RTMP server on
earth routes on it. polyemesis just did not have an RTMP server — it used
`ffmpeg -listen 1`, a single-connection receiver that cannot demultiplex by
path. A second RTMP source was refused at the form (`checkRTMPExclusive` in
`internal/db/sources.go`) rather than accepted and silently starved, which is
what would have happened if two of them tried to bind 1935.

That refusal was the right call for as long as the limit existed — a source that
saves cleanly, reports itself configured and silently receives nothing is the
worst of the available failures. It is the limit that was wrong, not the guard.

## The dependency argument, re-measured

The original rejection was about `yutopp/go-rtmp` pulling seven modules. That is
still true, and it has not moved since:

| | transitive deps | version | notes |
|---|---|---|---|
| `github.com/datarhei/gosrt` (in use today) | **1** | v0.11.0 | the bar |
| **`github.com/bluenviron/gortmplib`** | **3** — `abema/go-mp4`, `bluenviron/mediacommon/v2`, `google/uuid` | **v1.0.0** | MediaMTX/gortsplib team |
| `github.com/yutopp/go-rtmp` | 7 — `logrus`, `pkg/errors`, `mapstructure`, … | v0.0.7 | unchanged; `mapstructure` pinned at a 2021 release |

**gortmplib did not exist in this form when the decision was made.** It is v1.0.0
rather than v0.0.7, carries no second logging framework, no `pkg/errors`, no
`mapstructure`, and comes from the people who maintain gortsplib and MediaMTX —
i.e. the same provenance argument that justified `gosrt`.

Three deps against `gosrt`'s one is a real cost, not a free one. But it is a
different order of magnitude from seven, and the "dead upstream" objection no
longer applies to anything in the tree.

## The shape that fits this codebase

`internal/srtserver` is the precedent, and it is worth being precise about what
it does: it is **not** a from-scratch protocol implementation. It wraps `gosrt`
and adds the part that is ours — a `Lookup func(token string) (Target, bool)`
for auth, one-publisher-per-key enforcement, and piping the accepted connection
to a relay sink. About 1,300 lines including tests.

An RTMP equivalent lands in the same place, and gortmplib exposes exactly the
right seam:

```go
type ServerConn struct {
    RW         io.ReadWriter
    URL        *url.URL   // filled by Accept — this is app/streamkey
    Publish    bool
    // ...
}

type Conn interface {
    Read() (message.Message, error)
    Write(msg message.Message) error
    // ...
}
```

`URL` after `Accept()` is the demux key, which is the entire feature. `Conn` then
works at **RTMP message level** — not raw bytes, but not decoded media either.

That distinction decides the whole design.

### Option A — routing front-end, FFmpeg still ingests (recommended)

Accept the connection, read the URL, look up the source by stream key exactly as
`srtserver` looks up by token, then forward the RTMP messages to a per-source
`ffmpeg` process. polyemesis never parses a frame; FFmpeg keeps doing every bit
of media work it does today, and `-map 0 -c copy` is untouched.

This preserves the architecture's central rule — *accept, do not decode,
republish* — and it is the reason to prefer gortmplib's `Conn` over its `Reader`.

- ~600–900 lines plus tests, closely modelled on `internal/srtserver`
- One new dependency tree (3 modules)
- Roughly a week, including the port/listener plumbing and the settings work to
  remove `checkRTMPExclusive`

> **Outcome.** This is what was built. The package came in at ~540 lines plus
> ~380 of tests, and the dependency tree measured as predicted: three modules in
> `go.mod`'s indirect block, of which `google/uuid` was already present via
> sqlite. The one thing the estimate got wrong is described under *Correction —
> one port, both directions* below: "forward to a per-source ffmpeg" turned out
> to be a per-source port by another name, and the pub/sub shape removed it.

### Option B — full ingest in Go

Use gortmplib's `Reader` with its `OnDataH264` / `OnDataMPEG4Audio` callbacks,
then re-mux to MPEG-TS in Go and feed the relay directly.

This is the wrong trade. Those callbacks hand you parsed access units, so a
muxer written here lands in the critical path of every RTMP frame — a class of
bug the project currently does not own at all, in exchange for removing one
FFmpeg process. Several weeks, and permanently ours to maintain.

## What has NOT changed

The strongest argument in the original rejection was never about dependencies:

> **RTMP carries one stereo pair.** No amount of RTMP work enables
> per-destination multitrack routing, which is the reason this product exists.

That is now **partly** false, and only partly. Enhanced RTMP multitrack does work
on FFmpeg 7.1+ — verified end to end, a destination configured for tracks 1 and 3
received exactly those two tones. So multi-source RTMP would no longer be limited
to single-track programmes on a current FFmpeg.

But it still gives nothing SRT does not already give, to anyone who can use SRT.
The honest case for this work is narrow and specific: **encoders that cannot do
SRT at all**, where an install supported exactly one of them. Hardware encoders,
older appliances, and anything speaking only RTMP. If you have two of those,
this is the only thing that unblocks you; if you have OBS, SRT is strictly
better and was already unlimited.

## Decision (2026-08-06): build it

The requirement is that **how many sources you can run must not depend on which
protocol your encoder speaks**. That is the right principle, and the current
asymmetry — unlimited over SRT, exactly one over RTMP — is an implementation
artifact of `ffmpeg -listen 1`, not a design position anyone chose.

### Rejected: a TCP port per RTMP source

The cheap version. It is also the surface `DESIGN-ONE-PORT-ONLY.md` deliberately
removed: allocation, conflict detection, auto-move-on-collision, a port field per
source in the UI, and validation in two places — "several of them *were* wrong at
some point". Reintroducing that for RTMP undoes a good decision to save a week.

### Chosen: gortmplib as a routing front-end

RTMP gets the same shape SRT already has — one port, addressed by a key,
unlimited sources:

```
srt://host:6000?streamid=<token>      # today
rtmp://host:1935/live/<streamkey>     # same model, same one port
```

`ServerConn.URL` is filled by `Accept()`, so the path IS the demux key. The
package mirrors `internal/srtserver` almost line for line:

| `internal/srtserver` | `internal/rtmpserver` |
|---|---|
| `Lookup func(token string) (Target, bool)` | `Lookup func(streamKey string) (Target, bool)`, same constant-time contract |
| `PublisherKey{SourceID, Backup}` | identical — the failover standby is a second slot, not a contender |
| `handlePublish` pipes bytes to `Target.Sink` | relays `message.Message` to the source's FFmpeg |

**Media is never parsed.** Both `ServerConn` and `Client` implement
`Conn{Read() (message.Message, error); Write(message.Message) error}`, so the
relay is message-level: accept the publisher, open a `Client` publishing to that
source's FFmpeg, and pump. gortmplib's `Reader`/`OnDataH264` path is explicitly
NOT used — it hands over decoded access units and would put a muxer in the
critical path of every frame.

**Correction — one port, both directions.** The first implementation relayed each
publisher *outward* to a per-source FFmpeg on its own loopback port. That works,
but it is not how RTMP servers are built and the extra port was unnecessary:
datarhei Core runs its internal RTMP server as a publish/subscribe hub on a
single port, with FFmpeg pulling `rtmp://127.0.0.1:1935/<path>` back out of the
same listener the encoder pushed into. Every other RTMP server does the same.

So the server serves both roles:

```
encoder  --publish--> rtmp://host:1935/live/<key>
ffmpeg   --play-----> rtmp://127.0.0.1:1935/live/<key>
```

No per-source ports at all — not even loopback ones.

Two consequences fall out of it:

- **Subscribers are loopback-only.** A stream key is a *publish* credential; if
  it also authorised playback, every ingest key would silently become a viewing
  key. Playback for viewers is the playout page's job, behind authentication.
- **Setup messages are cached and replayed.** A subscriber joining after the
  metadata and codec sequence headers have gone past cannot decode anything.
  They are remembered per stream and replayed on subscribe, which is what makes
  the order of "encoder connects" and "FFmpeg connects" not matter — and it does
  vary, since an encoder reconnecting mid-session is routine. This is the only
  message inspection in the package: a type switch, not a decode.

### Work

1. `internal/rtmpserver` — accept, demux by key, constant-time lookup,
   one-publisher-per-slot with takeover, message relay. ~600 lines + tests.
2. Manager wiring, mirroring `manager.go`'s shared SRT listener.
3. Delete `checkRTMPExclusive` and its tests; RTMP sources become ordinary.
4. Publish URL for RTMP sources becomes per-source (`/live/<key>`), so the
   Sources page offers one the way it does for SRT.
5. Docs: `DESIGN-ONE-PORT-ONLY.md`'s RTMP section is superseded.

Enhanced RTMP needs nothing extra — it is the same connection, and the tracks
ride through the relay untouched on FFmpeg 7.1+.

## What this does and does not give you

Worth being exact, because "multi-source RTMP" invites a reading it does not
support.

**It gives you:** any number of RTMP sources on one install, on one port, told
apart by stream key, each an ordinary source with its own destinations,
renditions, recordings and routing. No per-source ports, no `docker-compose.yml`
edit per programme, no refusal at the form.

**It does not give you multitrack over classic RTMP.** That is a property of FLV
and no amount of listener work changes it. Enhanced RTMP multitrack does work on
**FFmpeg 7.1+** — verified end to end, a destination configured for tracks 1 and
3 received exactly those two tones — and does **not** work on **FFmpeg 6.1.1**,
which is Ubuntu 24.04's stock build. It has **not** been confirmed with OBS
itself as the publisher; the verification used a synthetic FFmpeg publisher. See
[enhanced-rtmp-multitrack.md](enhanced-rtmp-multitrack.md).

**It does not make RTMP the recommended path.** If your encoder can speak SRT,
SRT is still strictly better: more tracks, encryption that is real rather than a
string comparison, and it was already unlimited. The audience for this work is
encoders that cannot speak SRT at all — hardware encoders, older appliances —
where an install previously supported exactly one.

## Why it was built when it was

The original recommendation was "Option A, if and when the second RTMP encoder
is real rather than hypothetical", on the grounds that the case was narrow.

That deferral was overturned by a better statement of the requirement:
**how many programmes you can run must not depend on which protocol your encoder
speaks.** Framed that way it is not a feature request waiting on demand, it is a
rule the product was breaking. The asymmetry existed because of what
`ffmpeg -listen 1` permitted, and "nobody has asked" was never evidence that the
shape was right — only that nobody had hit it yet. Waiting for the second
hardware encoder to show up would have meant fixing it under pressure, in front
of somebody whose install already could not do what they needed.
