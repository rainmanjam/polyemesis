# Destination health from what we already measure

**Status:** proposal. Nothing here is built.

polyemesis knows whether a destination's FFmpeg child is *running*. It does not
say whether that child is *keeping up*, and the difference is most of a
broadcast going wrong before anything reports it.

This proposes closing that gap using measurements the code already takes and
throws away — no platform API, no OAuth, no quota, and no dependency that can
deprecate underneath a self-hosted install.

- [Why not the platform APIs](#why-not-the-platform-apis)
- [What is already measured and discarded](#what-is-already-measured-and-discarded)
- [The signal that matters](#the-signal-that-matters)
- [Design](#design)
- [The two events](#the-two-events)
- [What could make this annoying](#what-could-make-this-annoying)
- [Files that change](#files-that-change)
- [Testing](#testing)
- [Out of scope](#out-of-scope)
- [Open questions](#open-questions)

---

## Why not the platform APIs

This was scoped first and rejected, so the reasoning belongs here rather than in
someone's memory.

| Platform | Stream-health API |
|---|---|
| YouTube | `liveStreams.status.healthStatus` — real, with typed `configurationIssues[]` |
| Facebook | partial; raw ingest numbers, no verdict, undocumented error taxonomy |
| Kick | live status only |
| Twitch | **none.** Inspector is a web tool with no public equivalent |
| Custom RTMP/SRT | none, and never will be |

Three things sank it as a first build.

**It cannot see most destinations.** polyemesis does not store a `liveStream`
id, and recovering one needs a token for the channel that owns the stream key.
A destination created from a **pasted stream key has no account row and
therefore no token**, so no call can be made on its behalf at all. For a
self-hosted tool that is the common case, not the edge.

**It competes with chat for quota.** `internal/chat/quota.go` already paces
YouTube chat against the same 10,000 units/day, and at its floor consumes the
whole daily budget in under three hours. An unbudgeted health poller starves the
chat pane, which is the fastest way to get a feature switched off.

**It is one platform's answer to a question every platform poses.** Building
YouTube-only monitoring teaches operators that the health badge means nothing on
three of four platforms.

The local signals below have none of those properties. They work for every
destination — pasted keys, custom RTMP, Twitch, all of it.

---

## What is already measured and discarded

`internal/ffmpeg/progress.go` parses **eight fields per destination per second**
from FFmpeg's `-progress` stream:

```go
type Progress struct {
    Frame, TotalSize, OutTimeMS, DupFrames, DropFrames int64
    FPS, BitrateKbps, Speed                            float64
    Done                                               bool
}
```

They reach `supervisor.Status.Progress`. Then, measured on the tree at the time
of writing:

| Field | Non-test consumers |
|---|---|
| `BitrateKbps` | 4 — all Prometheus |
| `DropFrames` | 1 — Prometheus |
| `Speed`, `DupFrames`, `FPS`, `TotalSize`, `OutTimeMS` | **0** |

Nothing raises an event from any of it. A destination can be losing a third of
its frames and the only way to find out is to be looking at a Grafana panel at
the time.

The relay computes more of the same, in `relay.Stats`: `TSLost`,
`Discontinuities` and `LossPercent`, inferred from MPEG-TS continuity counters.
The SRT listener exposes `RTTMs`, `LossPkts` and `RetransPkt` per publisher.
Both are surfaced in the API. Neither raises anything either.

---

## The signal that matters

**`Speed` is the one to build on, and the reason is not the obvious one.**

`Speed` is FFmpeg's ratio of output time to wall-clock time. For an offline
encode it measures CPU. For a **live push it measures something better**: a
destination running at `speed < 1.0` is not keeping up with realtime, and for a
passthrough destination — which is every destination in polyemesis by default,
since `CopyVideo` is unconditionally true — there is almost no CPU work to be
slow at. So a sustained low speed on a passthrough means one of:

- the remote platform is not accepting data fast enough, and FFmpeg is blocking
  on its socket write
- the network path to the platform is congested
- the source is starving (which the ingest tier reports separately)

The first of those is **the same question the platform APIs were going to
answer** — "is the platform still taking this?" — arrived at from the local side,
for every destination, with no credential and no quota.

That is the case for building this first. It is not a consolation prize for
missing out on `healthStatus`; for the question an operator actually has, it is
better coverage.

**Caveat, stated up front:** `speed` is a *symptom*, not a cause. It cannot
distinguish "the platform is throttling us" from "the uplink is saturated" from
"the disk backing a file destination is slow". The event must therefore say what
was observed and must not name a cause. See [the events](#the-two-events).

### Supporting signals

- **`DropFrames` rising** — frames FFmpeg discarded to keep up. Corroborates a
  low speed and is the more legible number for a human.
- **`DupFrames` rising** — frames FFmpeg *padded*. Means the opposite problem:
  the source is not delivering fast enough. Useful for telling a starving ingest
  apart from a congested output, which is exactly the distinction an operator
  gets wrong at 2am.
- **`FPS` far below the source's rate** — the same thing `Speed` says, in units
  a person recognises. Good for display; redundant for alerting.

---

## Design

### Where it lives

**In the existing alert watcher.** Not a new package, not a new goroutine.

`internal/alerts/watch.go` already holds exactly the right machinery:
`downState.observe(bad, now, after)` folds one observation and reports which
edge was crossed, with a dwell before firing and an automatic recover. It is
what `ingest.lost`, `destination.down`, `disk.low` and `loudness.out_of_compliance`
are all built from, and its doc comment says why it is shaped that way — given
the same sequence of snapshots it emits the same sequence of events, which makes
flap behaviour a table test rather than a judgement call.

Adding a fifth condition to it is a smaller change than any alternative, and
inherits debounce, per-rule subscription and the rate floor for free.

### The seam

`alerts.DestState` is what the watcher sees per destination. It carries `ID`,
`Name`, `Enabled`, `Running`, `Platform`, `Error`. It gains the progress fields:

```go
type DestState struct {
    // ... existing fields ...

    // Speed is FFmpeg's output-time-over-wall-clock ratio for this
    // destination, or 0 when there is no process to have one. A live push
    // sitting below 1.0 is not keeping up with realtime.
    Speed float64
    // DropFrames and DupFrames are cumulative counts. The watcher holds the
    // previous sample, so a RATE is what gets judged -- a destination that
    // dropped a hundred frames an hour ago and none since is healthy, and a
    // cumulative threshold would call it sick for ever.
    DropFrames int64
    DupFrames  int64
}
```

Populated where `DestState` is already built, from `d.Process.Progress`.

### The condition

```
falling behind  :=  Running && Speed > 0 && Speed < SpeedFloor
                    sustained for FallingBehindFor
```

`Speed > 0` is load-bearing: a child that has not yet emitted a progress block
reports `0`, and zero is not slow, it is unknown. Treating unknown as bad would
fire on every destination for the first second of every broadcast — the single
fastest way to get this muted.

Proposed defaults, both configurable and both **needing measurement before they
are fixed** (see [open questions](#open-questions)):

| Setting | Proposed | Why |
|---|---|---|
| `SpeedFloor` | `0.95` | 1.0 is the target; a few percent under is normal jitter |
| `FallingBehindFor` | `30s` | long enough to ride out a keyframe-alignment stall, short enough to matter in a 40-minute stream |

The drop-frame rate is **reported, not thresholded**. It rides in the event as
context, because a second independent threshold is a second thing to tune wrong
and it would fire on the same incidents.

---

## The two events

Matching the existing `lost`/`recovered` pairs exactly.

| Type | Severity | Fires when |
|---|---|---|
| `destination.falling_behind` | `warning` | speed under the floor for the dwell |
| `destination.caught_up` | `info` | it comes back |

Not `destination.degraded`/`destination.recovered`: **`destination.recovered`
already exists** and pairs with `destination.down`. Reusing it would make one
message close out two different conditions, and an operator reading a channel
could not tell which.

Message shape, following the name-not-cause rule:

> **Twitch is falling behind**
> Encoding at 0.87× realtime for the last 34s, and has dropped 412 frames.
> This usually means the platform or the network is not taking data fast
> enough.

It reports the measurement and hedges the cause, because the measurement is
certain and the cause is not.

### Not double-reporting

A destination in real trouble eventually dies, and `destination.down` fires. The
two must not both shout about one incident.

`falling_behind` is an **early warning that precedes** `down` by design — 30s of
degradation before the process gives up. If a destination goes down while
`falling_behind` is latched, the watcher clears the latch **without** emitting
`caught_up`: the condition did not recover, it was overtaken. That is one extra
branch in the watcher and it is the difference between a useful pair and a noisy
one.

---

## What could make this annoying

Taken from what the audit-events review found the hard way: the failure mode of
an alerting feature is not being wrong, it is being ignored.

- **Firing at startup.** Handled by `Speed > 0` and the dwell.
- **Firing on a file destination.** Writing to a local file is often far faster
  than realtime, so `speed` runs well above 1.0 and never trips. Fine — but a
  file destination on a slow disk *should* trip, and does.
- **Firing on every destination at once.** A congested uplink degrades all of
  them, producing N simultaneous events. The coalescer groups by subject, and
  the subject here is per destination, so N destinations means N groups. **This
  is the one real noise risk in the design.** Options in
  [open questions](#open-questions).
- **A rendition destination that is legitimately CPU-bound.** A destination with
  its own rendition does real encoding work and can sit under 1.0 on a busy box
  without anything being wrong with the platform. The event text must not claim
  it is the platform's fault, which is why it hedges.

---

## Files that change

| File | Change |
|---|---|
| `internal/alerts/alerts.go` | two `Type` constants, appended to `AllTypes()` |
| `internal/alerts/watch.go` | `DestState` gains three fields; one condition; the overtaken-by-`down` branch |
| `internal/alerts/config.go` | `SpeedFloor`, `FallingBehindFor` with defaults |
| `internal/engine/…` | populate the new `DestState` fields from `d.Process.Progress` |
| `internal/engine/reload.go` | new settings keys must be classified — `reload_test.go` walks every leaf path and fails on an unclassified one |
| `ui/src/pages/AutomationPage.tsx` | two label-map entries |
| `ui/src/components/DestinationCard.tsx` | show speed and drop rate while running |
| `docs/MONITORING.md` | document both events and the thresholds |

No new package, no new goroutine, no schema change.

---

## Testing

The watcher is a pure function of its snapshot sequence, which is the whole
reason to put this there. Table tests over synthetic sequences:

- speed dips below the floor and returns **inside** the dwell → nothing fires
- speed stays below for the dwell → exactly one `falling_behind`
- speed recovers → exactly one `caught_up`
- speed oscillates around the floor → does not flap
- a destination with no process (`Speed == 0`) → never fires
- `falling_behind` latched, then the destination goes **down** → `down` fires,
  `caught_up` does **not**
- two destinations degrade together → two independent events, neither
  suppressing the other

End to end, in an acceptance suite: push a stream, throttle one destination's
egress with `tc netem` (the technique the SRT loss testing already used), assert
`falling_behind` is raised and that removing the shaping raises `caught_up`.

---

## Out of scope

- **Platform APIs.** Possibly a later enrichment for YouTube specifically; not
  this.
- **A verdict on the numbers.** No "health score". The event reports what was
  measured. See the platform-health scoping for why a computed verdict presented
  as a platform's opinion is worse than no verdict.
- **Ingest-side signals.** SRT `RTTMs`/`LossPkts` and relay `LossPercent` are
  about the *source*, and `ingest.lost` already covers the source dying. Alerting
  on ingest quality is a separate proposal with its own thresholds.
- **Per-destination video preview.** Scoped separately; the copy-remux is cheap
  but per-destination video is really per-*upstream* video, so passthrough
  destinations show identical pictures.

---

## Open questions

These need a decision or a measurement before this is built.

1. **The two thresholds are guesses.** `0.95` and `30s` are plausible, not
   measured. The honest way to set them is to log `speed` for a real broadcast
   across a few destinations and look at the distribution — in particular what
   it does at startup, at a keyframe boundary, and during a platform hiccup.
   Until then they are placeholders.

2. **Simultaneous degradation.** A congested uplink fires N events at once. Is
   that right — N destinations really are affected — or should the watcher
   notice that *all* destinations degraded together and raise one event about
   the uplink instead? The second is more useful and more machinery, and it
   needs a rule for "all" when one destination is disabled.

3. **Is `speed` reliable on every FFmpeg build in the support range?** The floor
   is 6.0. The `-progress` fields the supervisor parses are documented as
   reliable from 6.x, but `speed` specifically has not been checked against 6.1
   and 8.1 side by side. One measurement settles it.

4. **Should `DropFrames` get its own event?** Argued against above — it fires on
   the same incidents — but a destination dropping frames at a steady 1% while
   holding `speed == 1.0` is possible in principle and would go unreported.
   Worth a look at real data before deciding.
