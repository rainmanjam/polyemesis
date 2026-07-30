# polyemesis vs datarhei/Restreamer vs restream.io

An honest comparison, including the parts where polyemesis loses.

Sourced from [RESEARCH-COMPETITIVE.md](RESEARCH-COMPETITIVE.md) — a survey of
Restreamer's issue trackers (263 open, 548 closed) and restream.io's published
plan comparison, done 2026-07-27 — with the polyemesis column verified against
the code rather than assumed.

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
| **Overlays** — text, logo, watermark, channel name | Asked 5 separate times (6+5+4+2+1 reactions), every one closed unimplemented | **Missing.** Planned on renditions, where re-encoding is already the contract |
| **WebRTC / WHIP output** | 6 reactions, closed unimplemented | **Missing.** Sizeable subsystem; the real use case is sub-second self-monitoring |
| **Decklink / SDI capture**, in and out | 4 + 2 reactions | **Missing.** Needs an FFmpeg built with `decklink`, so a third image variant |
| **Deinterlacing** | 1 reaction | **Built, but unreachable.** `bwdif` with off/auto/all modes exists in the encoder and the schema; there is no UI control, so no operator can switch it on ([correction](RESEARCH-COMPETITIVE.md#correction-2026-07-28-deinterlacing-is-built-but-unreachable)) |
| **Playlist / scheduled file broadcast** | Five issues circling one capability | **Partial.** Pull ingest reads `file://` and schedules exist; no playlist sequencing |
| **Multi-input compositing / video grid** | 1 reaction | **Missing.** Natural once multi-source is settled |
| **MQTT** | Core tracker | **Missing.** Alert webhooks exist instead |
| **HDR 10-bit HEVC** | 3 reactions, closed | **Partial.** `libx265` and `hevc_nvenc` exist; no HDR tone-map path |
| **LL-HLS** | 2 reactions, closed | **Partial.** SRT latency is configurable; the HLS preview is not low-latency |
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
