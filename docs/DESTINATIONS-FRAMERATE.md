# Destination kinds and frame-rate handling

Generated 2026-09-03 from `internal/db/destinations.go`, `internal/ffmpeg/build.go`,
`internal/ffmpeg/rendition.go`, `docs/ENCODING.md`.

## The rule that makes the table short

**No destination encodes video.** Every kind emits `-map 0:v:0 -c:v copy`
(`build.go:1272`, `1348`). `destinations.go:213` states it: "Whatever the
rendition, the destination still does `-c:v copy`", and `:863`: "bitrate,
resolution and frame rate belong to the rendition, not to the destination."

So frame timing is decided ONE level up, and a destination can only inherit it.
`-fps_mode` and `-vsync` are never set anywhere in the tree.

## Destinations

| Kind | Value | Muxer | Video | Audio | Frame timing |
|---|---|---|---|---|---|
| RTMP / RTMPS | `rtmp` | `-f flv` | `-c:v copy` | encoded (aac, or copy) | inherited |
| SRT | `srt` | `-f mpegts` | `-c:v copy` | encoded (aac/opus, or copy) | inherited |
| File | `file` | by extension | `-c:v copy` | encoded, or copy | inherited |
| Audio-only | `audio` | icecast / file | **none mapped** | encoded | n/a |

`file` muxer by extension (`fileFormat`): `.mp4`/`.m4v` -> mp4, `.flv` -> flv,
`.ts` -> mpegts, `.mov` -> mov, anything else -> matroska.

`audio` is the one kind with no video at all: `build.go:1271` maps video only
when `Kind != DestAudio`, and `:1279` branches it to an audio-only output.

## Where the frame rate is actually decided

| Upstream | Emits | Result |
|---|---|---|
| Passthrough (no rendition) | nothing touches video | source timing exactly; VFR stays VFR |
| Rendition, `FPS > 0` | `-r <fps>` (`rendition.go:456`) | CFR at the chosen rate |
| Rendition, `FPS == 0` | nothing | source rate preserved; VFR stays VFR |
| Synth / slate | `-r` always (`synth.go:306`) | CFR (frames are generated) |

`FPS == 0` means "keep the source's" in both `rendition.go:157` and
`db/renditions.go:93`.

## VFR is deliberately preserved

`docs/ENCODING.md` section 5 records the measurement. On a fixture 30 fps
nominal / ~18 fps actual:

	through a rendition   72 frames / 3.967s in -> 72 frames / 3.967s out
	with -fps_mode cfr    72 frames -> 120 frames  (48 duplicated)

Forcing CFR costs 66% more encoded frames for no additional information.
Pinned by `TestAVFRSourceKeepsItsTimingThroughARendition`
(`internal/ffmpeg/vfr_test.go:37`).

**Measurement trap (#342):** ffprobe reports a uniform `duration_time` for every
frame of an MPEG-TS, derived from `r_frame_rate`, because the container stores
no per-frame durations. Read that field and a VFR stream looks silently
resampled to CFR. Presentation timestamps are the ground truth.

## Consequence worth knowing

Because destinations only copy, a platform that requires CFR cannot be served by
pointing a passthrough destination at a VFR source -- the fix is a rendition
with an explicit FPS, which is the only place `-r` is emitted.
