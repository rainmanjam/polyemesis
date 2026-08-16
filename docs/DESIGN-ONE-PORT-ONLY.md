# One port, always: token-addressed SRT as the only SRT path

Supersedes the "both paths" position in
[DESIGN-ONE-PORT-INGEST.md](DESIGN-ONE-PORT-INGEST.md), which kept per-source
ports alongside the shared listener. This records why that changed, and what it
costs.

## What changes

| | Before | After |
|---|---|---|
| **SRT** | a UDP port per source; a shared token-addressed port as an opt-in extra, **off by default** | **one** UDP port, token-addressed, always, no alternative |
| **RTMP** | a TCP port per source | one TCP port, serving at most **one** source — *superseded 2026-08-06, see [RTMP](#rtmp)* |
| **Pull** | dials out, binds nothing | unchanged |
| **Port fields per source** | SRT port, RTMP port, with conflict detection and auto-move | **gone** |

A source is addressed by its **token**, and by nothing else:

```
srt://host:6000?streamid=<token>
```

## Why the earlier position was wrong

The original design kept both paths, and the argument was real: a dedicated port
per programme is genuinely useful for firewall rules, separate NICs and QoS
marking, and keeping both meant the newer path could be adopted without betting
an install on it.

What that argument missed is what the *default* costs everybody else:

- **It shipped off.** The feature was built, tested and documented, and then no
  new install used it. The default is the product for almost everyone.
- **It doubled the surface.** Per-source ports need allocation, conflict
  detection, auto-move-on-collision, a port field per source in the UI, and
  validation in two places. Every one of those was a place to get it wrong, and
  several of them *were* wrong at some point — a source silently receiving
  nothing because two sources shared a port was a real bug.
- **Docker made it worse.** Every new source needed a new published port, which
  means editing `docker-compose.yml` and restarting the container. Adding a
  programme should not require a restart of the thing that is streaming.
- **The shared port was unreachable anyway.** It defaulted to 6100, which
  `docker-compose.yml` never published and no document mentioned. Enabling it in
  a container bound a listener nothing could reach, while the UI reported it as
  enabled and enforcing. A feature that is off by default and broken when turned
  on is not really a feature.

## Why now

**Nothing has been released.** No tag, no published image, PR #1 unmerged. There
are no installs whose publish URLs would break, no encoders to reconfigure, no
migration to write for somebody else's running system.

This is the only moment when the simplification is free. After a release it
would be a breaking change with an upgrade note and a deprecation window.

> **Outcome, 2026-07-30.** The window was used: this landed before the first
> tag, so the breaking change never broke anything. That is the whole argument
> of this section, and it is worth keeping visible now that it has been
> spent — the same reasoning does not apply to the next simplification, because
> from the first release onward there are installs to carry.

## What it buys beyond simplicity

**Ingest becomes authenticated by default.** With the token as the only address,
there is no unauthenticated ingest path left: a publisher that does not present
a valid token is refused, with a typed reason. Before this, an SRT port with no
passphrase accepted whoever reached it, and [SECURITY.md](../SECURITY.md) listed
that under what polyemesis does *not* defend.

That line is now gone. SECURITY.md instead says SRT is authenticated by
construction, and the caveat that remains is RTMP's — which has no token routing
and is protected by its stream key alone.

> **Outcome, 2026-08-06.** The RTMP half of that caveat no longer holds either.
> RTMP is now routed by stream key on the shared listener, matched in constant
> time against every source's key, so a publisher presenting nothing or
> something unrecognised is refused rather than landing on whichever source
> happened to hold the socket. What separates the two protocols now is
> encryption, not authentication: SRT's passphrase is real AES, an RTMP stream
> key is a string comparison over a cleartext connection. See [RTMP](#rtmp).

**One published port, forever.** `docker-compose.yml` stops being something you
edit when you add a programme.

## What it costs

Honestly: per-source ports are gone, and with them the ability to give one
programme its own port for firewall, NIC or QoS purposes.

That is a real loss for a real use case, and the answer for anyone who needs it
is an external one — a proxy, a NAT rule, or a second instance. It is no longer
something polyemesis does natively, and this document exists so that is a
decision rather than an accident.

If it turns out to matter, the shape to bring back is a per-source *listen
address* rather than a per-source port — which is what the QoS and NIC cases
actually want — not the port-per-source model being removed here.

## RTMP

> **Superseded, 2026-08-06.** RTMP now has the same shape as SRT — one port,
> addressed by a key, unlimited sources — and the one-source limit is gone.
> `internal/rtmpserver` is the listener that closed it.
>
> The original argument is kept below rather than deleted, because two of its
> three premises were sound and are still the reason this was not done earlier.
> What actually moved is recorded in [What changed](#what-changed) after it.

RTMP keeps its single well-known port and serves **at most one source**.

This is not one-port routing; it is the absence of it. RTMP's URL carries
`app/streamkey` and every RTMP server on earth routes on it, but polyemesis has
no RTMP server — it uses `ffmpeg -listen 1`, which is a single-connection
receiver and cannot demultiplex by path.

Closing that needs either a real RTMP implementation in Go or a dependency that
brings one. The dependency was measured and rejected: `yutopp/go-rtmp` v0.0.7
links **seven** further modules, including `logrus` (a second logging framework
in a binary that uses `log/slog`), `pkg/errors` and `mapstructure` (both dead
upstream). Writing one is the largest single piece of work in the repository.

Neither is worth it *yet*, because **classic RTMP carries one stereo pair**. No amount
of RTMP work enables per-destination multitrack routing, which is the reason
this product exists. Multi-source RTMP only matters to somebody running several
independent single-track programmes, and nobody has asked.

So: **at most one source may use RTMP ingest**, enforced with an error that says
why and points at SRT. A second RTMP source is refused rather than accepted and
silently starved, which is what would happen if two of them tried to bind 1935.

### What changed

Three things, in the order they mattered.

**The principle was wrong, not just the implementation.** The clearest way to
see it: *how many programmes you can run should not depend on which protocol
your encoder speaks.* Unlimited over SRT and exactly one over RTMP was never a
position anyone argued for — it was what `ffmpeg -listen 1` happened to permit,
back-filled with a justification. "Nobody has asked" is a fine reason to defer
work; it is not a reason to call the resulting limit a design.

**The dependency was re-measured, and it is a different dependency.** The
rejection was of `yutopp/go-rtmp`, and that rejection still stands — it has not
moved since. What exists now did not exist in this form when the decision was
taken:

| | transitive deps | version | provenance |
|---|---|---|---|
| `github.com/datarhei/gosrt` (the bar) | **1** | v0.11.0 | datarhei |
| **`github.com/bluenviron/gortmplib`** | **3** — `abema/go-mp4`, `bluenviron/mediacommon/v2`, `google/uuid` | **v1.0.0** | gortsplib / MediaMTX maintainers |
| `github.com/yutopp/go-rtmp` | 7 — `logrus`, `pkg/errors`, `mapstructure`, … | v0.0.7 | unchanged |

Three modules against gosrt's one is a real cost, not a free one. But it is a
different order of magnitude from seven, none of it is dead upstream, there is
no second logging framework, and the provenance is the same argument that
justified gosrt. All three are MIT and `CGO_ENABLED=0` clean.

**"Writing one is the largest single piece of work in the repository" was
answering the wrong question.** Nothing here implements RTMP. `internal/rtmpserver`
wraps gortmplib and adds only the part that is ours — the same part
`internal/srtserver` adds over gosrt: a constant-time `Lookup` from key to
source, one publisher slot per (source, role), and a relay. It is ~540 lines
plus tests, not a protocol stack.

The premise that has moved *least* is the one about tracks. **RTMP still carries
one stereo pair on classic FLV.** Enhanced RTMP multitrack does work on FFmpeg
7.1+ — verified end to end — but not on 6.1.1, and it has not been confirmed
with OBS publishing. Multi-source RTMP does not change that and was never going
to. It closes a different gap: **encoders that cannot speak SRT at all.**
Hardware encoders and older appliances, where the install previously supported
exactly one of them. If your encoder can do SRT, SRT is still strictly better
and was already unlimited.

### The shape it took

One port, both directions. Encoders publish to the listener and this install's
own FFmpeg subscribes to it, on that same port:

```
encoder  --publish--> rtmp://host:1935/live/<streamkey>
ffmpeg   --play-----> rtmp://127.0.0.1:1935/live/<streamkey>
```

The first implementation relayed each publisher *outward* to a per-source FFmpeg
listening on its own loopback port. That works, and it is what `ffmpeg -listen 1`
forced before any of this existed, but it would have reintroduced per-source
ports — the exact surface this document removed — for no gain. Publish/subscribe
on one port is how RTMP servers are actually built: datarhei Core runs its
internal RTMP server this way, and so does everything else.

Three consequences worth stating, because each is a decision rather than a
detail:

- **Media is never parsed.** gortmplib also offers a `Reader` that hands over
  decoded access units. Using it would put a muxer in the critical path of every
  frame and make a whole class of bug ours that currently belongs to FFmpeg.
  This forwards RTMP *messages*, so the bytes reaching FFmpeg are the bytes the
  encoder sent and `-c copy` downstream is untouched.
- **Subscribers are loopback-only.** A stream key is a *publish* credential — it
  is what sits in an operator's OBS settings. If it also authorised playback,
  every ingest key would quietly become a viewing key for anyone who learned it.
  Playback for viewers is the playout page's job, behind authentication.
- **Setup messages are cached and replayed.** A subscriber that arrives after
  the metadata and codec sequence headers have gone past has a byte stream it
  cannot interpret. They are remembered per stream key and replayed on
  subscribe, which is what makes the order of "encoder connects" and "FFmpeg
  connects" not matter — and it does vary, since an encoder reconnecting
  mid-session is routine. This is the only message inspection in the package: a
  type switch, not a decode.

The failover standby gets a slot of its own on the same listener, keyed on
(source, role) exactly as SRT's is. Keying on the source alone would let the
primary and the standby evict each other, which is the failover feature failing
in the one situation it exists for.

The full record, including the options rejected, is in
[evidence/multi-source-rtmp.md](evidence/multi-source-rtmp.md).

## The failover backup needs an address too

Easy to miss, and it falls out of the same change. The backup ingest is a
*second listener* running alongside the primary — that is the whole point of it,
so the two cannot share a socket. Under per-source ports it got its own port and
the validator refused a collision.

With ports gone it needs a different address on the same port. The address is
**derived from the source's token** rather than being a second stored secret:

```
srt://host:6000?streamid=<token>           the primary encoder
srt://host:6000?streamid=<token>.backup    the standby encoder
```

Derived rather than minted, which was the first shape considered: one secret per
source is one thing to rotate, one thing to leak, and one thing to explain — and
rotating the source's token moves the standby's address with it automatically,
so the two can never drift apart.

The listener holds two targets per source and the token lists are keyed on
(source, isBackup), so neither address can reach the other's feed. A publisher
that could take the primary's slot by presenting the backup address would put
the standby on air without anyone asking, which is precisely the mix-up failover
exists to prevent.

The alternative considered and rejected was restricting the backup to pull mode
so it dials out and binds nothing. That is less code, but it removes the most
useful backup arrangement there is — a second encoder pushing to you, ready to
take over — and failover exists precisely for the case where the primary encoder
has gone away.

## The rule this leaves

- Every push source is addressed by its token on one SRT port.
- Every RTMP source is addressed by its stream key on one RTMP port. Any number
  of them, the same as SRT. *(Updated 2026-08-06; this line previously read
  "one source, at most, may additionally be reached over RTMP" — see
  [RTMP](#rtmp).)*
- Pull sources dial out and bind nothing.
- There are no per-source ports anywhere.
