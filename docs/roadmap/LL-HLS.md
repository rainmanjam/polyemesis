# Preview latency: tune HLS, do not build LL-HLS

**Status: DONE**, 2026-07-28. **Zero new dependencies.**
**Result:** preview latency **4.2–6.2 s → 2.2–3.2 s** (mean ≈2.7 s).

Two things changed on the way in, both because they were measured rather than
assumed. The frame-rate fix turned out to be a **bug fix**, not a tuning knob —
`-g SegmentSeconds*30` produced `EXTINF:1.200000` for a requested 1 s on a 25 fps
ingest, confirmed against FFmpeg 8.1.2. And `lowLatencyMode` was already set in
the player but genuinely inert, for a subtler reason than either the design or
its reviewer gave: `maxLiveSyncPlaybackRate` **defaults to 1**, so the guard
`!lowLatencyMode || maxLiveSyncPlaybackRate === 1` short-circuited regardless.

---

## The decisive finding

**FFmpeg cannot emit LL-HLS partial segments.** Verified against the pinned
binary, not recalled:

```console
$ ffmpeg -hide_banner -h muxer=hls | grep -i "part\|lhls\|low.lat"
     delete_segments   E.... delete segment files that are no longer part of the playlist
$ ffmpeg -version | head -1
ffmpeg version 8.1.2
```

The only match is the word "part" inside another option's help text. There is no
`hls_part_time`, no `hls_server_control`, no preload/hint/blocking option, and
nothing LL-HLS-related in the `hls_flags` set.

FFmpeg's **`dash` muxer does ship `-ldash` and `-target_latency`**, which shows
the omission in `hls` is a scope decision rather than a version lag. It is not
going to appear by upgrading.

So true LL-HLS is not a flag change. It is a new subsystem.

## Why not build that subsystem

A Go-side packager is not "a packager". Every item below is a normative MUST
from `draft-pantos-hls-rfc8216bis`:

1. an fMP4 box scanner confirming a complete `moof`+`mdat` pair is on disk
   before publishing, because a Partial Segment must be downloadable at full
   link speed the instant it appears;
2. an `EXT-X-PART` writer holding every part inside [85%, 100%] of
   `PART-TARGET` — against `frag_duration`, which FFmpeg treats as a target, not
   a guarantee;
3. `EXT-X-PART-INF` plus the `PART-HOLD-BACK` that is required alongside it;
4. `EXT-X-PRELOAD-HINT` with an origin that parks a request mid-flight and
   streams bytes as produced;
5. the full Blocking Playlist Reload state machine — `_HLS_msn`/`_HLS_part`, 400
   when `_HLS_part` arrives without `_HLS_msn`, 400 past the Advance Part Limit,
   503 after three Target Durations;
6. part-tag ageing so the playlist does not grow without bound.

And the spec it targets is an **Internet-Draft that expires 2 November 2026**.

Adopting `bluenviron/gohlslib` relocates the work rather than avoiding it: it
brings its own muxer and segment lifecycle into a repo whose entire shape is
"FFmpeg via `os/exec`", sitting beside `internal/playout`'s existing sweeper,
handler and session counter doing the same jobs differently.

**The prize is about one second.** `PART-HOLD-BACK` must be ≥2× and should be
≥3× `PART-TARGET`, so at 333 ms parts that is 0.67–1.0 s of *mandated* hold-back
before part completion and network. Realistic LL-HLS lands at 1.5–2.0 s against
the ~2.7 s this design reaches with flags — for **15–25 days** plus ongoing
conformance maintenance against an expiring draft.

## The changes

### 1. Frame-rate independence — a bug fix, and the largest single win

Today the GOP is `SegmentSeconds × 30`, hardcoded to 30 fps. On a 25 fps ingest
every segment overshoots by 20%.

```diff
- -g <SegmentSeconds*30> -keyint_min <same>
+ -force_key_frames expr:gte(t,n_forced*<SegmentSeconds>)
  -sc_threshold 0
```

Measured on FFmpeg 8.1.2, 25 fps source, `-hls_time 1`:

| keyframe control | emitted EXTINF |
|---|---|
| `-g 30 -keyint_min 30` (today) | `1.200000` every segment |
| `-force_key_frames expr:gte(t,n_forced*1)` | `1.000000` every segment |

`-sc_threshold 0` stays — it is what stops x264 inserting extra keyframes
between the forced ones.

> **Correction from verification.** The original rationale also claimed this
> removes `TARGETDURATION` inflation, which hls.js multiplies for target
> latency. That was **refuted by measurement**: at `-g 60 -hls_time 2` FFmpeg
> emits `EXTINF 2.400000` but `#EXT-X-TARGETDURATION:2` — it *rounds* rather
> than ceilings, so player hold-back is unaffected. The fix is still correct and
> still worth doing, because segments become the length they claim to be; but it
> buys accurate segment duration, not amplified latency savings.

### 2. One-second segments

`SegmentSeconds` default 2 → 1. Validation range stays 1–10.

The standard objection is quality, and it was measured. Same 20 s source,
800 kbps CBR, veryfast, zerolatency, PSNR against an uncompressed reference:

| GOP | bytes | PSNR average |
|---|---|---|
| 60 frames (2 s) | 2,073,077 | 41.398 dB |
| 30 frames (1 s) | 2,092,188 | 41.462 dB |

+0.9% bytes, and the shorter GOP scored marginally *higher*. At 360p/800 kbps
the cost of halving the segment is not measurable. Objection retired with a
number — though note this is a single-session measurement, not a repeated one.

### 3. Scale the live window with the segment

`-hls_list_size 6` is currently a constant, so halving the segment halves the
window. Derive it instead: `listSize := max(6, ceil(8 / SegmentSeconds))` — 6 at
2 s (unchanged), 8 at 1 s. Add `-hls_delete_threshold 2` so one
already-unreferenced segment survives a player's in-flight request.

> **Correction from verification.** The original rationale claimed the 1 s
> window falls *below* hls.js's gap-controller seek threshold. It does not: the
> window is `listSize × T` and the threshold is
> `liveMaxLatencyDurationCount × T`, so at 2 s it is 12 vs 12 and at 1 s it is
> 6 vs 6 — **exactly equal at both scales**. The margin is scale-invariant.
> Bumping to 8 is still worth doing because equal is not margin, but the claimed
> new hazard does not exist.

### 4. `program_date_time`

Add `+program_date_time` to `hls_flags`, matching what `internal/playout`
already does. Not cosmetic: it is the only mechanism by which latency becomes a
**measured** number rather than a claimed one. `EXT-X-PROGRAM-DATE-TIME` of the
first segment plus cumulative `EXTINF` gives the wall-clock time of the live
edge; subtract from `now` for origin latency, directly.

### 5. Stay on MPEG-TS

fMP4 measured ~8–9% smaller (53,251 vs 58,468 bytes for segment 0). It buys
**zero latency**, and costs an `init.mp4` lifecycle that must survive the
on-demand stop/start cycle plus an explicit `video/iso.segment` content type for
Safari's native path. The only strategic argument for fMP4 was as a prerequisite
for the Go-side packager — which this design declines to build.

### 6. hls.js configuration

```ts
const hls = new Hls({
  // Load-bearing: gates the playback-rate catch-up controller that
  // maxLiveSyncPlaybackRate drives. Not merely a parts feature.
  lowLatencyMode: true,
  maxLiveSyncPlaybackRate: 1.08,

  // Target latency = liveSyncDurationCount x EXT-X-TARGETDURATION.
  // At 1 s segments that is 2 s.
  liveSyncDurationCount: 2,
  liveMaxLatencyDurationCount: 6,
});
```

> **Correction from verification.** The original design commented that
> `lowLatencyMode` "is already the library default and is inert here". That was
> **refuted** at `ui/node_modules/hls.js/dist/hls.js:33592`, which reads
> `if (!lowLatencyMode || maxLiveSyncPlaybackRate === 1 || !levelDetails.live)`
> and returns early. It gates the catch-up controller. Setting it is meaningful,
> and the comment must not say otherwise.

## The corrected latency budget

| | encode | in-progress segment | player hold-back | total |
|---|---|---|---|---|
| today, 30 fps | 0.2 s | 0–2 s | 2 × 2 s = 4 s | **4.2–6.2 s** |
| today, 25 fps | 0.2 s | 0–2.4 s | 4 s (unchanged) | **4.2–6.6 s** |
| after | 0.2 s | 0–1 s | 2 × 1 s = 2 s | **2.2–3.2 s** |

> **Correction from verification.** The original design summed today's budget as
> 5.2–6.2 s, which does not follow from its own components (0.2 + 0 + 4 = 4.2),
> and gave 6.5–8 s for 25 fps, which double-counted the refuted `TARGETDURATION`
> effect. **The mean saving is ≈2.5 s, not the headline 3.0 s.** Any acceptance
> gate must be set against 2.5, not 3.0.

## Test plan

Measure, do not assert:

- **Segment duration accuracy.** Run at 25 and 30 fps; parse every `EXTINF` and
  assert it equals `SegmentSeconds` within one frame interval. This is the
  regression test for change #1 and it fails today at 25 fps.
- **`TARGETDURATION` sanity.** Parse the playlist and record the emitted value
  alongside the measured `EXTINF` spread, so the rounding behaviour is pinned
  rather than assumed.
- **End-to-end latency, reported as a number.** With `program_date_time`,
  compute live-edge wall clock from the playlist and subtract from `now`.
  Report the delta; gate loosely at ≥2.0 s improvement.
- **Window invariant**, in Go: `listSize * SegmentSeconds >=
  liveMaxLatencyDurationCount * SegmentSeconds`.
- **Playwright**: assert `hls.latency` settles below target across two samples.

## Risks

1. **This is a preview, and the preview is the one place polyemesis re-encodes
   video.** Halving the GOP raises its cost slightly on a box that is already
   running everything else.
2. **Version-keyed behaviour.** The measurements above are FFmpeg 8.1.2. The
   project floor is 6.0, where rounding and `force_key_frames` behaviour should
   be re-measured rather than assumed.
3. **The migration touches a stored default.** `SegmentSeconds` lives in the
   settings blob; changing the default must not silently rewrite an operator's
   deliberate 2.
4. **Deferring WHEP makes this the only low-latency path.** At ~2.7 s it is a
   real improvement and it is not sub-second. If sub-second is required,
   [WEBRTC.md](WEBRTC.md) is the answer, not this.

---

## See also

- [ROADMAP](README.md)
- [WEBRTC.md](WEBRTC.md) — the sub-second tier, currently deferred
- [../MONITORING.md](../MONITORING.md)
