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

| # | Item | Effort | Status | Why here |
|---|---|---|---|---|
| **0** | [Unreachable features](UNREACHABLE-FEATURES.md) | 1–2 d | ✅ **done** | Two features were already built and no user could reach them. Also closed a validation gap found on the way in |
| **1** | [Playlist — Phase 0](PLAYLIST-AND-COMPOSITING.md#playlist-the-mostly-wiring-claim-is-half-right) | 1 d | ✅ **done** | Scheduled file broadcast already worked. Now documented, and pinned by a 15-check suite in CI |
| **2** | [LL-HLS](LL-HLS.md) | 3 d | ✅ **done** | Preview latency 4.2–6.2 s → 2.2–3.2 s, zero new dependencies. Also fixed a 30 fps assumption that made every segment 20% long on a 25 fps ingest |
| **3** | [MQTT](MQTT.md) | **5–6 d** telemetry only | **shipped** | Exactly one net-new module. Retained telemetry has no existing path at all |
| **4** | Selector generalisation | **~3–5 d** | ready | Shared prerequisite for 5 and 7. Doing it once is much safer than twice in the most safety-critical pure function in the tree |
| **5** | [Playlist — full](PLAYLIST-AND-COMPOSITING.md#sequencing-take-the-concat-demuxer) | **17–22 d** | ready | Most-requested by volume; lower technical risk than compositing |
| **6** | [Overlays](OVERLAYS.md) | **6 d** v0.5 · **16 d** full | **v0.5 shipped** | The most-repeated unmet request. Sits before 7 because it extracts the `overlay`/`evenExpr` helpers compositing then reuses |
| **7** | [Compositing](PLAYLIST-AND-COMPOSITING.md#compositing-multi-source-landed-but-as-isolation) | **21–26 d** | ready | Largest, riskiest, and the one that puts the audio differentiator in play |
| **8** | [WebRTC — WHEP](WEBRTC.md) | **8–12 d** | **deferred** | The sub-second preview tier. Independent of everything else; slots in whenever latency becomes the priority |
| **9** | [Teams and roles](TEAMS-AND-ROLES.md) | **20–30 d** | ready | A security boundary retrofitted across ~120 routes. Do it when it is the priority, not alongside other work |
| **10** | [Destination settings & metadata](DESTINATION-SETTINGS.md) | **14–19 d** in 4 parts | ✅ **A–D shipped** | Three metadata fields against a documented ten-plus, two of which are compliance items. Shipped 2026-07-29 bar the two items under [what remains](DESTINATION-SETTINGS.md#what-remains) |
| **11** | [Chat moderation](CHAT-MODERATION.md) | — | ✅ **shipped** | Ban, timeout and delete across four platforms, plus upstream retraction. Shipped 2026-07-30 with every item in the plan and two more the research turned up |
| — | [Unreachable knobs](UNREACHABLE-KNOBS.md) | — | survey | The sibling of item 0, for *settings* rather than features: knobs the server honours that no operator can reach. Surveyed 2026-07-30; one shipped, the rest open |

[WHIP](WEBRTC.md#whip-design) ingest is a separate decision, deliberately not
given a number: it carries one audio track, so it has RTMP's exact limitation
and cannot feed the per-destination routing this product exists for. Decide it
after WHEP has seen real use, if at all.

**Deferred** means not scheduled, not abandoned. The research for both is
complete and current — including a measured pion dependency audit that will not
need repeating — so picking either up is a scheduling decision rather than a
fresh investigation.

### Two consequences of the current deferrals

**Deferring WHEP makes LL-HLS the only low-latency path.** At ~2.7 s that is a
real improvement and it is not sub-second. If sub-second monitoring becomes a
requirement, item 8 is the answer and item 2 cannot be tuned into it — the
remaining gap is structural, not a matter of flags.

**Deferring overlays leaves item 7 doing its own helper extraction.** Compositing
reuses the `overlay=` construction and the `evenExpr` chroma-grid alignment that
the overlays work would have factored out of `rendition.go`. Built in this order
it is a few days cheaper; built in the other order, compositing pays that cost
itself. Neither blocks the other.

## The first five days

Items 0–2 total **five to six days** and are mutually independent. They are also
the only items with no architectural risk at all:

- **two shipped features become reachable** (vertical/dual-format output,
  deinterlacing);
- **scheduled pre-recorded broadcast becomes a documented, tested feature**
  rather than an accident of `file://` pull;
- **preview latency roughly halves.**

After that the next two are still small — MQTT telemetry at 5–6 days and the
overlays v0.5 at 6 — but both introduce something new: MQTT takes a dependency
and a broker, and overlays restructure the rendition argument builder. **Items 4
onward are all multi-week**, and item 4 is the point at which the selector, the
most safety-critical pure function in the tree, gets opened up.

So the honest shape is three tiers, not two: a week of pure win, a fortnight of
small-but-real features, and then everything else.

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

**`Queue: nil` in autopaho is a no-op — and the no-op is harmless.**
`autopaho/auto.go:271` really does substitute an in-memory queue for a nil one,
and the field's own comment contradicts the code. But building the feature
showed the substitution is **inert**: the queue is read only by
`PublishViaQueue`, while `ConnectionManager.Publish` bypasses it and returns
`ConnectionDownError` when the link is down — which is exactly the no-buffering
behaviour the design wanted. The verification pass found a true code fact and
drew a false conclusion from it; no no-op queue implementation was needed.
Recorded here because "the reviewer was half right" is the more useful lesson
than either "the reviewer caught it" or "the reviewer was wrong".

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
