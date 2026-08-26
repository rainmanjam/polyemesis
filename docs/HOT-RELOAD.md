# What a settings change costs

**A setting that reaches an FFmpeg command line replaces the process that runs
it. A setting that does not is delivered to the process still running.**

That is the whole rule, and it is mechanical rather than a matter of judgement:
FFmpeg has no way to change a muxer, an encoder, an output URL or a stream
mapping in flight, so anything that ends up in an argv costs a respawn. Anything
that does not must be pushed into the running process — because the alternative
is being told a change applied while the process goes on running the old one.

**The authority is `settingsReload` in
[internal/engine/reload.go](../internal/engine/reload.go).** It carries one rule
per settings field: a class, the function that carries the change, and why. A
test walks `db.Settings` by reflection and fails the build when a field has no
rule, when a rule names a field that no longer exists, or when it names a
function nobody wrote. **This document is a rendering of that table and can go
stale; the table cannot.**

## The classes

| Class | What it means | Fields |
|---|---|---|
| `live` | Applied to whatever is already running. No process replaced, no viewer or platform connection dropped | 89 |
| `respawn` | Baked into a child's argv. The named signature notices and the child is replaced | 51 |
| `rebind` | A bound socket. Stopped and rebound; every publisher on it reconnects | 1 |
| `on_demand` | Read at the moment it is needed. Nothing holds a copy, so nothing has to be applied | 8 |
| `next_start` | Stored now, acted on at the next process start | **0** |

Most of what an operator can change already applies without touching a process.
The 51 are the ones that reach an FFmpeg argv. The five classes hold 149 fields
between them, and `reload.go` is the authority for every number on this page.

### Applied live — nothing is replaced

| Field | How it reaches the running process |
|---|---|
| `destinations[].resilience.*` | `supervisor.SetPolicy`, straight into the running supervisor |
| `meters.intervalMs` | A throttle in the Go stdout parser, read per frame |
| `destinations.staggerMs` | Read per sweep; only ever affects processes started in that sweep |
| `preview.idleTimeoutSeconds` | Read by the preview sweeper each tick |
| `logging.*` | `applyLogging` swaps the file sink; children already running fill the new file |
| `recording.{maxAgeHours,maxGb,minFreeGb}` | Retention rules the sweeper re-reads per sweep |
| `failover.{graceSeconds,return,returnStableSeconds}` | Re-read by the selector sweep every 500 ms |
| `playout.{maxDiskMb,sessionIdleSeconds,maxSessions}` | Pushed into the playout manager before any variant is touched |
| `chat.*`, `automod.*`, `alerts.*`, `mqtt.*` | Applied out of band by the settings handler |
| `postProd.*` | Pushed into the jobs governor by the jobs policy endpoint; retention is re-read by the purge sweep |

### Read on demand — nothing holds a copy

- **Playout:** `playout.public` is evaluated per request, because a route table
  is built once at startup and this is a runtime setting.
  `playout.allowCrossOrigin` is a response header, decided per request.
- **Multitrack:** `multitrack.gpus.{model,vendorId,deviceId,dedicatedVideoMemory,sharedSystemMemory,driverVersion}`
  — the declared GPU capabilities Twitch Enhanced Broadcasting negotiates on.
  They are read out of settings **once per destination start**, in the go-live
  request body. A destination that is already publishing negotiated at go-live
  and holds a key minted for that negotiation: changing the declared hardware
  underneath it cannot retroactively change what Twitch agreed to, and
  restarting a live destination to re-ask would drop a platform connection to
  deliver a setting that only matters next time. So editing these while a
  destination is up costs nothing and changes nothing until its next start —
  which is the honest answer, not a missing feature.

### Requires a respawn — the value is in an argv or a socket bind

- **Ingest:** `ingest.mode`, the SRT and RTMP blocks, `ingest.pull.*`,
  `ingest.annotations.*` (which recompiles every routing graph).
- **Listeners:** `listeners.rtmpPort` is in the ingest child's argv.
  `listeners.srtPort` **rebinds a shared socket**.
- **Recorder:** `segmentSeconds`, `stems`, `stemCodec`, and `enabled`.
- **Preview and playout:** every geometry, bitrate and muxer field.
- **Renditions:** geometry, fps, bitrate, encoder, preset, GOP, deinterlace,
  overlays, text.
- **Destinations:** target URL, kind, filter graph, audio bitrate and codec,
  sample rate, upstream, video delay, expert args, transport tuning.
- **Failover and synth:** the tiers themselves, and the slate's own argv.

## What a restart costs

Not all respawns are equal, and the difference is what a viewer sees.

- **A destination restart** drops that platform's connection. How visible that
  is depends on the platform's own reconnect behaviour, not on polyemesis.
- **A rendition restart** cycles every destination and playout variant
  downstream of it. One edit, many dropped connections.
- **A silence-tier or selector transition** restarts every passthrough
  destination, because it moves the upstream signature they all read.
- **`listeners.srtPort` interrupts every programme**, not only the one being
  edited. It is one socket serving every source, told apart by token.

## What a settings save now tells you

`PUT /api/v1/settings` returns the stored settings — unchanged, at the top level
— plus a `reload` array saying what the save actually did:

```json
{
  "ingest": { "...": "..." },
  "reload": [
    {
      "sourceId": 1,
      "sourceName": "main",
      "notes": [
        { "tier": "destination", "name": "twitch", "action": "live",
          "reason": "reconnect policy retuned to 4s..1m0s, giving up after 9" },
        { "tier": "rendition", "name": "720p", "action": "restart",
          "reason": "its encode signature changed, or nothing selects it any more" }
      ]
    }
  ]
}
```

"Saved" is a statement about the database. This is a statement about the stream.

## A destination that is mid-reconnect

The state most likely to be got wrong. A destination in `reconnecting` has **no
child process** — the supervisor is asleep in its backoff.

- **Teardown is fast, not slow.** There is no child to signal, so the 12 s stop
  budget and 8 s grace are never paid. Removing eight reconnecting destinations
  does not take 96 seconds.
- **It still owns its port and subscription**, so teardown releases both
  correctly and nothing leaks.
- **Retuning shortens the wait it is already in, and never lengthens it.**
  Lowering the ceiling on a crawling destination brings it back sooner; raising
  it does not make the destination you are already waiting on wait longer.
- **Lowering `giveUpAfter` is not retroactive.** A destination is not failed for
  exits it made under the old rules; the new limit applies from the next exit.
- **Raising `giveUpAfter` on a destination that already gave up restarts it.**
  This is the one place a restart is deliberately chosen over a live apply —
  anything else would leave it failed for ever, which is the silent no-op this
  work exists to remove. The restart is reported in `reload`.

## Why FFmpeg cannot be reconfigured in flight

FFmpeg 8.1.2 offers exactly two in-flight channels: the `zmq`/`azmq` filters and
`sendcmd`/`asendcmd`. Both address **filters that were already instantiated with
a command interface**, and neither can change a muxer, an encoder, an output URL,
a stream mapping or a pixel format.

Using them would mean pre-instrumenting every filter graph against the
possibility of a future edit, accepting that only a subset of edits could be
delivered, and — the decisive objection — accepting a state where FFmpeg silently
declined a command and the process is running configuration nobody can read
back. That is precisely the "stored and reported as applied, but not in effect"
failure this whole design exists to eliminate. Declined.

## Not covered

Stated here rather than discovered later.

- **No overlay, geometry, bitrate, routing-graph or transport change is applied
  live.** All of them still respawn. The live set is deliberately small.
- **The ingest is never hot-reloaded.** Any ingest edit drops the publisher.
- **`listeners.srtPort` interrupts every programme**, not just the one being
  edited — it is a shared socket.
- **`reload` is best-effort telemetry, not an audit log.** Concurrent saves
  interleave into one recorder, so two operators saving at the same moment each
  see the union of what moved. That is the truth about the system; it is not a
  per-request record.
- **The classification table is checked for shape, not proven field by field.**
  The tests verify that every field has a rule, that no rule names a missing
  field, and that every named function exists. They do **not** prove that a
  field marked `live` genuinely applies live. Only the settings this work moved
  — `destinations[].resilience.*` and `meters.intervalMs` — have behavioural
  tests behind the claim.
- **Nothing is `next_start` any more, and the class is kept empty on purpose.**
  It was added for job-history retention, which was read once at boot so
  lowering it did nothing observable until a restart. Recording that honestly
  rather than mislabelling it as live is what made it obvious enough to fix —
  the purge now re-reads its settings every sweep. The class stays because the
  next one will be found the same way, and a reviewer needs somewhere to put it
  that is not a lie.
- **A settings save still reconciles everything.** Every engine walks its whole
  tier tree and re-hashes every signature on every save. That cost is unchanged
  and is O(engines × tiers) per save.

## See also

- [internal/engine/reload.go](../internal/engine/reload.go) — the table this
  document renders, and the only authority
- [CONFIGURATION.md](CONFIGURATION.md) — what each setting means
- [ARCHITECTURE.md](ARCHITECTURE.md) — the tiers a restart propagates through
