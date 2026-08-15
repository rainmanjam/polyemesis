# polyemesis vs Restreamer, restream.io, obs-multi-rtmp, Aitum and MistServer

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
>
> **Re-verified again 2026-08-07** while surveying Castr, and the same drift had
> happened twice more: both playlist rows still said "Partial — no sequencing"
> long after `internal/playlistmedia`, `-stream_loop -1` and the scheduler's
> `playlist.start` / `playlist.stop` actions shipped. Note the direction —
> **every stale row so far has understated the product**, because the page is
> written when a gap is found and nobody returns to it when the gap closes.
>
> **2026-08-09: three tools were missing from the page entirely.** obs-multi-rtmp,
> Aitum Multistream and MistServer are what a prospect is usually already
> running when they find this project, and a comparison that omits the incumbent
> is not credible. They are added below. Each claim about them was read in their
> own source rather than taken from their marketing, and every external fact on
> this page now carries the date it was checked — including the ones that were
> already here. The drift warning above applies to those rows too: they are one
> person's reading of somebody else's repository on one day.

---

## The short version

**If you do not need different audio per destination, you already have a tool.**
Multistreaming from the stream PC: obs-multi-rtmp is free, mature and enough.
Want a server instead: Restreamer is more mature and far more widely deployed
than this. Want no server at all: restream.io. Want an industrial-strength
media server with real track selection: MistServer.

polyemesis exists for one case none of them serve: *this* platform gets the
clean mix, *that* one gets the full mix, from one upload and one video encode.
If that is not your problem, the rest of this page is academic.

## They select a track. polyemesis mixes.

This is the whole difference, and it is narrower and more checkable than "better
audio support", so it is worth stating as something you can go and read rather
than as a claim you have to take on trust.

Every tool here can decide **which** audio track leaves for a destination. None
of them can decide **what is in it**.

- **obs-multi-rtmp** — `src/output-config.h`, checked 2026-08-09:

  ```c++
  struct AudioTrackConfig {
      int mixer_track;
      int output_track;
  };
  ```

  A pair of integers: this OBS mixer track goes in that output track slot. No
  gain, no sum, no matrix. It is an assignment.

- **Aitum Multistream** — `multistream.cpp`, checked 2026-08-09, builds each
  destination's encoder with
  `obs_audio_encoder_create(..., obs_data_get_int(settings, "audio_track"), ...)`.
  One integer per destination: the mixer index that destination takes.

- **MistServer** — `Util::wouldSelect()` in `lib/stream.cpp`, checked
  2026-08-09, reads `audio=` and `video=` off a push target's query string and
  resolves them through `Util::findTracks`, which also accepts `all` and `*`. So
  a MistServer push target genuinely can carry different tracks from the one
  before it, and anyone who tells you a media server cannot select tracks has
  not read this file. It selects well. It does not mix: there is no `amix`, no
  downmix, no `loudnorm` and no `ebur128` anywhere under `src/` or `lib/`. It is
  not that it cannot touch audio — `src/process/process_av.cpp` decodes and
  re-encodes it, and NVENC is wired up for video — but the audio path there is
  `swr_alloc_set_opts`, a resampler. One track in, one track out. Nothing sums.

- **OBS itself** — the Twitch VOD track is
  `obs_audio_encoder_create(..., vodTrack, ...)`, and `VodTrackMixerIdx()`
  returns nothing unless `ServiceSupportsVodTrack(service)`
  (obsproject/obs-studio `frontend/utility/AdvancedOutput.cpp`, checked
  2026-08-09). One extra selected track, for a service that has opted into it.

polyemesis sums. A destination's mix is the set of ingest tracks you chose,
summed, with a gain per input-channel-to-output-channel cell, and a loudness
target measured *after* the routing — see [AUDIO-ROUTING.md](AUDIO-ROUTING.md).
That is a different operation from selection, which is why "add per-destination
audio to obs-multi-rtmp" is not a small patch to obs-multi-rtmp.

**And here is what mixing costs.** Selecting a track is a copy; mixing means
decoding the audio and encoding it again, once per destination. polyemesis does
that on every destination. The video is stream-copied so the bill stays small,
but it is not zero, and a selector's is.

## What polyemesis has that none of them have

| Capability | Restreamer | restream.io | obs-multi-rtmp | Aitum | MistServer |
|---|---|---|---|---|---|
| **Per-destination audio mix** from one multitrack ingest | No | No | No | No | No |
| **Channel-level mix matrix** with per-cell gain | No | No | No | No | No |
| **Per-destination loudness TARGET you set** — I, LRA and TP, measured after routing | Filter only<sup>4</sup> | No | No | No | No |
| **Multitrack archive** — every ingest track preserved, stream-copied | No | No | Via OBS | Via OBS | Yes |
| **Per-track stems** as 24-bit WAV or FLAC, segment-aligned to the master | No | No | No | No | No |
| Track annotations — what each incoming track actually is | No | No | n/a | n/a | Unverified |
| Typed SRT rejections — the publisher is told *why* it was refused | No | n/a | n/a | n/a | Unverified |
| A second audio mix to the same destination, from one ingest | No | n/a | n/a | n/a | Twitch Enhanced Broadcasting, and it needs a supported GPU; competitors unverified |

Two cells say **Unverified** rather than "No" on purpose. MistServer's own
recording row is a loss and is marked as one: a recording target with
`?audio=all` keeps every track (`Util::findTracks`, `lib/stream.cpp`, checked
2026-08-09), which is the same outcome as our multitrack archive by a different
route. The two "Unverified" cells are things nobody has gone and read, and this
page does not guess.

The multitrack ask is not hypothetical. "Multiple audio tracks for Twitch" is an
open request on Restreamer's tracker with 8 reactions (surveyed 2026-07-27) —
users asking Restreamer for the thing polyemesis was built to be.

## What the OBS plugins have that polyemesis does not

obs-multi-rtmp (GPL-2.0, ~4,975 stars, last pushed 2026-08-01) and Aitum
Multistream (GPL-2.0, ~250 stars, last pushed 2026-05-19), both checked
2026-08-09. They are the honest incumbent: free, installed in two minutes, and
already on the machine.

| What they have | polyemesis |
|---|---|
| **No server at all.** A plugin in OBS, no host to provision, no port to open | You run a server, or there is nothing to run |
| **Nothing new to learn.** The destinations sit in OBS beside the ones already there | A second UI, a second set of concepts |
| **No extra hop.** OBS talks to the platform directly, so nothing in the middle can fail | The server is one more thing between you and air |
| Per-target video encoder settings, decided in OBS | Shared renditions, decided on the server |

**What they cost you is upload.** Each target in `MultiOutputConfig` is another
OBS output with its own service parameters and its own optional encoder config
(`src/output-config.h`, checked 2026-08-09), so N destinations is N uploads off
one connection, and N video encodes on the machine that is also running the
show. polyemesis takes one upload and stream-copies the video to all of them.
That trade is the entire reason to put a server in the path, and if your upload
is comfortable it is not a reason at all.

## What Restreamer has that polyemesis does not

Ranked by how often it was asked for, which is the honest ordering. Reaction
counts surveyed 2026-07-27.

| Gap | Evidence | Status here |
|---|---|---|
| **Overlays** — text, logo, watermark, channel name | Asked 5 separate times (6+5+4+2+1 reactions), every one closed unimplemented | ✅ **Have.** Image watermarks and text overlays on renditions, where re-encoding is already the contract. Nine anchors, sizes as a percentage of the frame so one logo is correct on landscape and vertical tiers alike; two weights of Inter ship embedded because `drawtext` needs a font path |
| **WebRTC / WHIP output** | 6 reactions, closed unimplemented | **Missing.** Sizeable subsystem; the real use case is sub-second self-monitoring |
| **Decklink / SDI capture**, in and out | 4 + 2 reactions | **Missing.** Needs an FFmpeg built with `decklink`, so a third image variant |
| **Deinterlacing** | 1 reaction | ✅ **Have.** `bwdif` with off / only-interlaced / every-frame, placed first in the filter chain because scaling interlaced content bakes the combing in |
| **Playlist / scheduled file broadcast** | Five issues circling one capability | ✅ **Have, with one caveat.** An ordered list of uploads played through FFmpeg's concat demuxer under `-stream_loop -1`, and `playlist.start` / `playlist.stop` are scheduler actions. Every upload is normalised once on import to a single fixed profile ([playlistmedia](../internal/playlistmedia/playlistmedia.go)), because concat refuses a set whose codecs, timebase, resolution or channel layout disagree — and plays a drifting, tearing one if it does not refuse. **The caveat:** the playlist lives under `failover.playlist` and goes on air when no encoder is delivering. It is the fill tier, not a channel you can programme a day of content into |
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

## What MistServer has that polyemesis does not

MistServer (Unlicense, ~508 stars, last pushed 2026-08-06, checked 2026-08-09)
is the one tool here that is a peer rather than a different shape: a media
server that ingests and pushes out, and does it at a scale and protocol breadth
this project does not approach.

| What it has | polyemesis |
|---|---|
| **Protocol breadth** — an `src/output/` directory of muxers, including WebRTC and its own SRT output | SRT and RTMP in, RTMP/SRT out, HLS for preview |
| **Per-push track selection**, on any target, by index or codec or `all` | Selection is per destination too, but the point here is the mix |
| **Years of production deployment**, and a commercial vendor behind it | Pre-release, one maintainer |
| **Selection without decoding.** A push target that only selects tracks is a pure copy end to end; transcoding is a separate opt-in process | Every destination decodes and re-encodes its audio, always, because that is what mixing is |

**A caveat on reading their source.** The track-selection block in
`Util::wouldSelect` sits between `/*LTS-START*/` and `/*LTS-END*/` comment
markers. Those are plain comments, not preprocessor guards, and nothing in
`meson.build` or `meson_options.txt` strips them, so the code compiles in the
Unlicense repository as published (checked 2026-08-09). Whether every binary
DDVTECH distributes includes it is **unverified** — check before you depend on
it.

## What restream.io has that polyemesis does not

restream.io is SaaS, so self-hosting beats it on cost, privacy and limits by
construction — their plans cap *simultaneous channels* at 2/3/5/8 by tier
(surveyed 2026-07-27), which is a billing artefact rather than a technical one.

| restream.io feature | polyemesis |
|---|---|
| **Studio** — browser production, remote guests | **Missing.** Multi-source plus compositing is the closest path |
| **Pre-recorded upload, go live later** | ✅ **Have.** Upload the file, it is normalised on import, and a scheduled `playlist.start` puts it on air at a chosen time. Needs the failover tier on, since that is where the playlist lives |
| **Teams, roles, multiple workspaces** | **Missing.** Exactly one admin identity |
| Hosted chat across platforms | Have, self-hosted |
| Live health monitor | Have |
| No server to run | By design, no |

**Teams and roles is the one to take seriously.** polyemesis has a single admin
identity, and access to the UI is full control of the server's streaming. Any
multi-operator deployment — a church A/V team, an agency running several clients
— hits that immediately. See [../SECURITY.md](../SECURITY.md).

## Where the servers overlap

All of them do the basic job: ingest once, fan out to several platforms,
reconnect when a platform drops, show you whether it is working.

| | polyemesis | Restreamer | restream.io | MistServer |
|---|---|---|---|---|
| Self-hosted | Yes | Yes | No | Yes |
| Simultaneous destinations | No configured cap<sup>1</sup> | Unlimited | 2–8 by plan tier | Unverified |
| Video re-encoded per destination | **No** (`-c:v copy`) | Optional | Yes, server-side | Optional, as a process |
| Recording | Multitrack, stream-copied | No built-in<sup>2</sup> | Yes | Yes, tracks selectable |
| Unified chat | Yes | No | Yes | No |
| Metrics / API | Prometheus + REST | See<sup>4</sup> | REST | REST |
| Hardware encoding | NVENC, QSV, VA-API, VideoToolbox, AMF | Yes | n/a | NVENC |
| Public release in the last 12 months | Yes<sup>3</sup> | No<sup>3</sup> | n/a, hosted | Yes<sup>3</sup> |
| Cost | Your hardware | Your hardware | Subscription | Your hardware |

<sup>1</sup> No limit in the software. Upload bandwidth and CPU are the real
ones: measured at roughly 4% of one core per destination on a 6-core VPS, so a
4-core box runs out somewhere near 96. "Unlimited" was the previous wording and
it was not defensible.

<sup>2</sup> This row said "Yes" until 2026-08-09 and was wrong. Recording is an
open feature request on Restreamer's own tracker — [datarhei/restreamer#692](https://github.com/datarhei/restreamer/issues/692),
opened 2024-02-14, still open when checked 2026-08-09. The underlying datarhei
Core can write files; the Restreamer product does not put recording in front of
you, which is why the request exists.

<sup>3</sup> Release recency, not quality, and it cuts against the maturity row
above rather than replacing it. Restreamer's latest release is v2.12.0,
2024-09-13. MistServer's repository was last pushed 2026-08-06. polyemesis
tagged v0.1.0 on 2026-07-31 and v0.6.0 on 2026-08-09 — which is the velocity of
something pre-release with one maintainer, and is worth exactly that much. All
three checked 2026-08-09.

<sup>4</sup> **This row said "No" against Restreamer and that was wrong.**
Restreamer mounts a filter select per publication service, and `loudnorm` is one
of the filters it offers, so it does apply a per-destination loudness filter.
What it does not offer is a *target you set*: its control is an on/off checkbox
that emits the bare string `loudnorm` with no I, LRA or TP, so it takes FFmpeg's
defaults and an operator cannot ask for −16 LUFS rather than −24. That is the
real difference and it is narrower than the row used to claim. Corrected
2026-08-14 after a sweep read their source rather than their feature list — the
same correction the recording row needed in the other direction, and the second
time a cell here has asserted a competitor lacks something they ship.

The metrics row above was wrong the same way and is now blank on their side:
Restreamer's own README advertises resource monitoring "optionally by
Prom-Metrics", datarhei Core documents Prometheus support, and it serves GraphQL
as well as REST — so "REST" understated them twice over and implied Prometheus
was ours alone.

---

## See also

- [RESEARCH-COMPETITIVE.md](RESEARCH-COMPETITIVE.md) — the underlying survey,
  with reaction counts and the gap ranking
- [AUDIO-ROUTING.md](AUDIO-ROUTING.md) — the capability the comparison turns on
- [RENDITIONS.md](RENDITIONS.md) — how per-destination video specs work here
