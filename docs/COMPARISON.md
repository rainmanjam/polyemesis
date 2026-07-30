# polyemesis vs datarhei/Restreamer vs restream.io

An honest comparison, including the parts where polyemesis loses.

Sourced from [RESEARCH-COMPETITIVE.md](RESEARCH-COMPETITIVE.md) — a survey of
Restreamer's issue trackers (263 open, 548 closed) and restream.io's published
plan comparison, done 2026-07-27 — with the polyemesis column verified against
the code rather than assumed.

> **The polyemesis column was re-verified 2026-07-30**, and three rows had gone
> stale in the product's favour: overlays and MQTT had shipped while still
> listed as missing, and LL-HLS had been deliberately declined rather than
> merely absent. A comparison page drifts in whichever direction the project
> moves, so the column is worth re-checking whenever this page is cited.

---

## The short version

**If you do not need different audio per destination, use Restreamer.** It is
more mature, more widely deployed, and it does the common restreaming job well.

polyemesis exists for one case Restreamer does not serve: *this* platform gets
the clean mix, *that* one gets the full mix, from one upload and one video
encode. If that is not your problem, the rest of this page is academic.

## What polyemesis has that neither has

| Capability | Restreamer | restream.io |
|---|---|---|
| **Per-destination audio mix** from one multitrack ingest | No | No |
| **Channel-level mix matrix** with per-cell gain | No | No |
| **Per-destination loudness target**, measured after routing | No | No |
| **Multitrack archive** — every ingest track preserved, stream-copied | No | No |
| **Per-track stems** as 24-bit WAV or FLAC, segment-aligned to the master | No | No |
| Track annotations (what each incoming track *is*) | No | No |
| Typed SRT rejections — the publisher is told *why* it was refused | No | n/a |

The multitrack ask is not hypothetical. "Multiple audio tracks for Twitch" is an
open request on Restreamer's tracker with 8 reactions — users asking Restreamer
for the thing polyemesis was built to be.

## What Restreamer has that polyemesis does not

Ranked by how often it was asked for, which is the honest ordering.

| Gap | Evidence | Status here |
|---|---|---|
| **Overlays** — text, logo, watermark, channel name | Asked 5 separate times (6+5+4+2+1 reactions), every one closed unimplemented | ✅ **Have.** Image watermarks and text overlays on renditions, where re-encoding is already the contract. Nine anchors, sizes as a percentage of the frame so one logo is correct on landscape and vertical tiers alike; two weights of Inter ship embedded because `drawtext` needs a font path |
| **WebRTC / WHIP output** | 6 reactions, closed unimplemented | **Missing.** Sizeable subsystem; the real use case is sub-second self-monitoring |
| **Decklink / SDI capture**, in and out | 4 + 2 reactions | **Missing.** Needs an FFmpeg built with `decklink`, so a third image variant |
| **Deinterlacing** | 1 reaction | ✅ **Have.** `bwdif` with off / only-interlaced / every-frame, placed first in the filter chain because scaling interlaced content bakes the combing in |
| **Playlist / scheduled file broadcast** | Five issues circling one capability | **Partial.** Pull ingest reads `file://` and schedules exist; no playlist sequencing |
| **Multi-input compositing / video grid** | 1 reaction | **Missing.** Natural once multi-source is settled |
| **MQTT** | Core tracker | ✅ **Have.** Retained telemetry with Home Assistant discovery, so the stream appears as entities in a dashboard the operator already runs. Alert webhooks exist alongside it — see [MQTT.md](MQTT.md) |
| **HDR 10-bit HEVC** | 3 reactions, closed | **Partial.** `libx265` and `hevc_nvenc` exist; no HDR tone-map path |
| **LL-HLS** | 2 reactions, closed | **Addressed by tuning, not by building LL-HLS.** FFmpeg cannot emit LL-HLS partial segments at all — verified against the pinned binary — so the protocol would need a Go-side packager, which [roadmap/LL-HLS.md](roadmap/LL-HLS.md) declines. Tuning instead took preview latency from **4.2–6.2 s to 2.2–3.2 s**, measured. Two of the wins were bug fixes rather than knobs: a wrong GOP calculation, and a player flag that was inert because of an unrelated default |
| Maturity | — | Restreamer is established; polyemesis is pre-release with one maintainer |

**One caveat on Restreamer's side.** Two of its four most-reacted open issues are
not feature requests — "State of Restreamer" (31 reactions) and "Future release
plans? (last release September 2024)" (11). Roughly a fifth of the open
tracker's reaction weight is users asking whether the project is alive. That is
context for the maturity row, not a criticism of the software.

## What restream.io has that polyemesis does not

restream.io is SaaS, so self-hosting beats it on cost, privacy and limits by
construction — their plans cap *simultaneous channels* at 2/3/5/8 by tier, which
is a billing artefact rather than a technical one.

| restream.io feature | polyemesis |
|---|---|
| **Studio** — browser production, remote guests | **Missing.** Multi-source plus compositing is the closest path |
| **Pre-recorded upload, go live later** | **Partial** — pull ingest reads `file://`, no scheduling of it |
| **Teams, roles, multiple workspaces** | **Missing.** Exactly one admin identity |
| Hosted chat across platforms | Have, self-hosted |
| Live health monitor | Have |
| No server to run | By design, no |

**Teams and roles is the one to take seriously.** polyemesis has a single admin
identity, and access to the UI is full control of the server's streaming. Any
multi-operator deployment — a church A/V team, an agency running several clients
— hits that immediately. See [../SECURITY.md](../SECURITY.md).

## Where the three overlap

All three do the basic job: ingest once, fan out to several platforms, reconnect
when a platform drops, show you whether it is working.

| | polyemesis | Restreamer | restream.io |
|---|---|---|---|
| Self-hosted | Yes | Yes | No |
| Simultaneous destinations | Unlimited | Unlimited | 2–8 by plan tier |
| Video re-encoded per destination | **No** (`-c:v copy`) | Optional | Yes, server-side |
| Recording | Multitrack, stream-copied | Yes | Yes |
| Unified chat | Yes | No | Yes |
| Metrics / API | Prometheus + REST | REST | REST |
| Hardware encoding | NVENC, QSV, VA-API, VideoToolbox, AMF | Yes | n/a |
| Cost | Your hardware | Your hardware | Subscription |

---

## See also

- [RESEARCH-COMPETITIVE.md](RESEARCH-COMPETITIVE.md) — the underlying survey,
  with reaction counts and the gap ranking
- [AUDIO-ROUTING.md](AUDIO-ROUTING.md) — the capability the comparison turns on
- [RENDITIONS.md](RENDITIONS.md) — how per-destination video specs work here
