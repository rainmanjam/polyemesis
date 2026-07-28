# One port, always: token-addressed SRT as the only SRT path

Supersedes the "both paths" position in
[DESIGN-ONE-PORT-INGEST.md](DESIGN-ONE-PORT-INGEST.md), which kept per-source
ports alongside the shared listener. This records why that changed, and what it
costs.

## What changes

| | Before | After |
|---|---|---|
| **SRT** | a UDP port per source; a shared token-addressed port as an opt-in extra, **off by default** | **one** UDP port, token-addressed, always, no alternative |
| **RTMP** | a TCP port per source | one TCP port, serving at most **one** source |
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

## What it buys beyond simplicity

**Ingest becomes authenticated by default.** With the token as the only address,
there is no unauthenticated ingest path left: a publisher that does not present
a valid token is refused, with a typed reason. Today an SRT port with no
passphrase accepts whoever reaches it, and
[SECURITY.md](../SECURITY.md) lists that under what polyemesis does *not*
defend. That line can go.

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

Neither is worth it *yet*, because **RTMP carries one stereo pair**. No amount
of RTMP work enables per-destination multitrack routing, which is the reason
this product exists. Multi-source RTMP only matters to somebody running several
independent single-track programmes, and nobody has asked.

So: **at most one source may use RTMP ingest**, enforced with an error that says
why and points at SRT. A second RTMP source is refused rather than accepted and
silently starved, which is what would happen if two of them tried to bind 1935.

## The failover backup needs an address too

Easy to miss, and it falls out of the same change. The backup ingest is a
*second listener* running alongside the primary — that is the whole point of it,
so the two cannot share a socket. Under per-source ports it got its own port and
the validator refused a collision.

With ports gone it needs a different address on the same port, and the model
already has one: **the backup gets its own token**, minted with the source's and
rotatable the same way.

```
srt://host:6000?streamid=<token>          the primary encoder
srt://host:6000?streamid=<backupToken>    the standby encoder
```

The alternative considered and rejected was restricting the backup to pull mode
so it dials out and binds nothing. That is less code, but it removes the most
useful backup arrangement there is — a second encoder pushing to you, ready to
take over — and failover exists precisely for the case where the primary encoder
has gone away.

## The rule this leaves

- Every push source is addressed by its token on one SRT port.
- One source, at most, may additionally be reached over RTMP.
- Pull sources dial out and bind nothing.
- There are no per-source ports anywhere.
