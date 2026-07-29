# Roadmap

Research complete, nothing started. Each item below has its own document with a
design, a file-level change list, a measurement-based test plan, risks and an
effort estimate.

Every design here was checked against the code and, where it depends on an
external spec or tool, against the official source or the local binary.
**Three of them came back needing revision, and the corrections are recorded in
the documents themselves** rather than quietly fixed — the wrong version of a
claim is often more instructive than the right one.

---

## The sequence

| # | Item | Effort | Why here |
|---|---|---|---|
| **0** | [Unreachable features](UNREACHABLE-FEATURES.md) | **1–2 d** | Two features are already built and no user can reach them. Cheapest work on the list by an order of magnitude |
| **1** | [Playlist — Phase 0](PLAYLIST-AND-COMPOSITING.md#playlist-the-mostly-wiring-claim-is-half-right) | **1 d** | Scheduled file broadcast *already works*. Documenting and testing it answers three tracker requests at zero risk |
| **2** | [LL-HLS](LL-HLS.md) | **3 d** | Preview latency 4.2–6.2 s → 2.2–3.2 s with zero new dependencies. Now the only low-latency path, since WHEP is deferred |
| **3** | [MQTT](MQTT.md) | **5–6 d** telemetry only | Exactly one net-new module. Retained telemetry has no existing path at all |
| **4** | Selector generalisation | **~3–5 d** | Shared prerequisite for playlist and compositing. Doing it once is much safer than twice in the most safety-critical pure function in the tree |
| **5** | [Playlist — full](PLAYLIST-AND-COMPOSITING.md#sequencing-take-the-concat-demuxer) | **17–22 d** | Most-requested by volume; lower technical risk than compositing |
| **6** | [Compositing](PLAYLIST-AND-COMPOSITING.md#compositing-multi-source-landed-but-as-isolation) | **21–26 d** | Largest, riskiest, and the one that puts the audio differentiator in play |
| **7** | [Teams and roles](TEAMS-AND-ROLES.md) | **20–30 d** | A security boundary retrofitted across ~120 routes. Do it when it is the priority, not alongside other work |

**Deferred, research complete and current:**
[Overlays](OVERLAYS.md) (~16 d, or a 6-day v0.5) ·
[WebRTC WHEP/WHIP](WEBRTC.md) (8–12 d WHEP).

## The first five days

Items 0–2 total **five to six days** and are mutually independent. They are also
the only items with no architectural risk at all:

- **two shipped features become reachable** (vertical/dual-format output,
  deinterlacing);
- **scheduled pre-recorded broadcast becomes a documented, tested feature**
  rather than an accident of `file://` pull;
- **preview latency roughly halves.**

Nothing after that is under two weeks.

## What the verification pass changed

Each design's factual claims were checked against the cited source or the local
binary by an adversarial reviewer told to default to *unsupported*. Highlights:

**FFmpeg cannot emit LL-HLS partial segments.** Confirmed directly against the
pinned binary — no `hls_part_time`, no `EXT-X-PART`, nothing. FFmpeg's `dash`
muxer *does* ship `-ldash`, so this is a scope decision, not a version lag.
That single fact turned LL-HLS from a possible 15–25 day subsystem into a 3-day
flag change with a documented ceiling.

**The LL-HLS latency arithmetic was wrong** in the direction that flatters the
work: today's budget is 4.2–6.2 s, not the claimed 5.2–6.2 s, so the mean saving
is ≈2.5 s rather than 3.0 s. Two supporting claims — `TARGETDURATION` inflation
and a segment-window hazard — were refuted by measurement and are recorded as
refuted.

**`Queue: nil` in autopaho is a no-op.** The MQTT design documented it as
deliberately disabling offline buffering; `autopaho/auto.go:271` silently
substitutes an in-memory queue. Shipping as written would have produced exactly
the stale-replay behaviour the design argued against, with a comment claiming
otherwise.

**`chi.Walk` emits one row per (method, pattern), not per registration.** The
teams/roles route-census test — the backstop meant to catch an unguarded route —
does not work as designed. The intent is right and the mechanism has to be
rebuilt.

**`paho.golang` is MQTT 5.0 only**, while the design cited MQTT 3.1.1
throughout. The substance survives; the declaration was missing.

## Two documentation bugs this surfaced

Both are fixed:

- `docs/DEPENDENCIES.md` opened with **"Eight, deliberately"** and listed eight
  direct modules. There are **nine** — `datarhei/gosrt` was added during the
  one-port work and never recorded. It now has an entry and a section, because
  it is the precedent every future protocol dependency gets measured against.
- The competitive research listed **deinterlacing as a gap.** It is built. See
  [UNREACHABLE-FEATURES.md](UNREACHABLE-FEATURES.md).

## A note on estimates

The day counts are one engineer already familiar with this codebase, and they
include tests to this project's standard — which is measurement rather than
assertion, and is a real fraction of each number. The compositing estimate
carries the widest uncertainty because it is the first feature to cross the
per-source isolation boundary that `internal/engine/manager.go` deliberately
established.

Where an estimate is a guess rather than a measurement, the document says so.

---

## See also

- [../COMPARISON.md](../COMPARISON.md) — how these gaps look against Restreamer
  and restream.io
- [../RESEARCH-COMPETITIVE.md](../RESEARCH-COMPETITIVE.md) — the demand evidence
  the ordering rests on
- [../ARCHITECTURE.md](../ARCHITECTURE.md)
