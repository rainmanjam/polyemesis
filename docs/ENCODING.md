# Encoding: what is copied, what is encoded, and what it costs

The short version: **video is copied by default and only encoded when you ask
for a rendition. Audio is always encoded, once per destination mix.** Everything
below follows from that.

`HARDWARE.md` covers getting a GPU working and diagnosing it when it does not.
This covers what the encoder is actually told to do.

---

## 1. The default path costs almost nothing

With no rendition selected, a destination gets `-c:v copy`. The picture is
remuxed, not re-encoded — the bytes OBS sent are the bytes the platform
receives. CPU is then nearly independent of resolution, because nothing is
decoding or encoding the video at all: a 4K60 ingest costs about what a 1080p30
one does.

What *is* encoded is the audio, once per destination, because each destination
gets its own mix and a mix has to be rendered. That is the trade the whole
product makes.

## 2. The GPU has two unrelated jobs, and only one is a workload

This is the most common confusion, so it is first.

| | What the GPU does | Required for |
|---|---|---|
| **Twitch Enhanced Broadcasting** | **Nothing at all.** Twitch's `GetClientConfiguration` requires GPU *information* in the request and refuses a client that reports none. | The second (VOD) audio track |
| **Renditions** | The actual video encode. | Any re-encode |

Twitch's requirement is a gate, not a workload — it is checking what kind of
machine you are, not using the card. A headless VPS with no GPU cannot negotiate
Enhanced Broadcasting no matter how much CPU it has, and a machine with a GPU
can negotiate it while still encoding on the CPU.

Renditions are the opposite: real work, and where a card earns its place.

## 3. Where a GPU actually helps

Cost scales with **distinct renditions**, not with destinations, and not with
ingests directly.

- One engine runs per source, so N ingests are N engines.
- A rendition is **shared and ref-counted**: one encode feeds every destination
  that selected it. Five destinations on one 1080p tier is one encode, not five.
- Two ingests each needing 1080p and 720p is four encodes. That is where a card
  stops being optional.

Twelve encoders are offered: `libx264` and `libx265` in software, plus **ten
hardware** encoders — NVENC, QSV, VAAPI, VideoToolbox and AMF, each in H.264 and
HEVC.

**Six of the twelve are probed with a real test encode** before being offered:
the five H.264 hardware encoders and `libx264`. The HEVC verdict is *inferred*
from its H.264 sibling, which opens the same device through the same driver — if
`h264_nvenc` cannot load libcuda then neither can `hevc_nvenc`, and there is no
machine where one is true and the other is not. The editor labels an inferred
verdict as inferred. That inference is good enough to stop offering a choice and
deliberately not good enough to refuse a start, so a rendition already saved on
`hevc_qsv` is never killed on a guess. Probing all twelve would double a cost
paid on every startup to measure a thing the sibling already answers.

A machine where every probe fails software-encodes; it does not refuse to boot.

**A rendition does not touch your audio.** The argv carries `-map 0:a -c:a copy`,
so every ingest track arrives bit-identical and per-destination routing
downstream still sees the full multitrack ingest. If that ever became an audio
encode or a mixdown, the product's differentiator would be gone.

---

## 4. Rate control

| Mode | Live egress | How |
|---|---|---|
| **CBR** | **Yes, and the default** | `-b:v X -maxrate X -bufsize 2X`, and NVENC additionally gets `-rc cbr` |
| **Capped VBR** | **Yes** | Set a ceiling above the target bitrate; NVENC additionally gets `-rc vbr`. See below |
| **CRF / CQ** (quality-targeted) | **No** | Used only off the live path: the clipper, media proxies, and archive transcodes |
| **ABR ladder to one destination** | **No** | One rendition per destination. The platform builds its own ladder from what you send. |
| **Several resolutions at once** | **Yes** | Several renditions, ref-counted, each feeding the destinations that selected it |

CBR is the right default for live: every platform ingest documents a target and a
ceiling, and a stream that undershoots its bitrate on a static scene and then
overshoots on a cut is the one that buffers.

### Setting a ceiling

A rendition has two more fields, both `0` by default:

- **Ceiling** (`maxrateKbps`) — `0` matches the target bitrate, which is CBR.
  Set it higher to allow burst up to what a platform publishes as its maximum;
  the services registry already carries a `MaxVideoKbps` per platform, read out
  of OBS's own service list, and that number is what this is for.
- **Rate window** (`bufsizeKbps`) — `0` is twice the ceiling. Smaller windows
  correct faster and pump more visibly on a scene cut.

A ceiling **below** the target bitrate is refused rather than clamped. There is
no way to resolve `-b:v 6000 -maxrate 4000` without overriding one of the two
numbers, and whichever is chosen the operator gets a stream at a bitrate they
did not pick, with no sign that a field they filled in was ignored.

Until #341 these fields existed on `RenditionSpec`, were used correctly by the
argv builder, and were reachable from nowhere — so the code described a
capability the product did not have. Leaving both at `0` emits byte-for-byte the
command line it always did.

**Only NVENC needs to be told which mode it is in.** `-rc cbr` *pins* NVENC to
constant bitrate, so until #343 a ceiling above the target reached the command
line and did nothing — the operator set a number that was silently ignored. A
ceiling above the target now emits `-rc vbr`; an equal or unset ceiling keeps
`-rc cbr`, which is the default every existing install already emits.

The other four families were checked and need no such flag:

| Family | Its rate-control lever | Why nothing changes |
|---|---|---|
| NVENC | `-rc cbr` / `-rc vbr` | **The one that needed fixing.** `-rc` overrides the preset's own choice, and `cbr` ignores the ceiling |
| QSV | none | It has no `-rc` option at all; the bitrate-control mode is derived from `-b:v` against `-maxrate` |
| VA-API | `-rc_mode` | Defaults to `auto`, documented as "choose mode automatically based on other parameters" |
| VideoToolbox | `-constant_bit_rate` | Defaults to false, i.e. already capped VBR. `-realtime` is a latency hint, not rate control |
| AMF | `-usage` | `transcoding` selects the streaming behaviour. Not verified against a binary — no Linux or macOS FFmpeg registers an `*_amf` encoder |

### Defaults

| Setting | Default | Bounds |
|---|---|---|
| Video bitrate | 4500 kbps | 100 – 100 000 kbps |
| Ceiling (maxrate) | = bitrate | 0, or ≥ the bitrate, and ≤ 100 000 kbps |
| Rate window (bufsize) | 2 × ceiling | 0, or ≥ half the ceiling, and ≤ 400 000 kbps |
| Width, height | source dimension | 128 – 7680, and **even** — 4:2:0 chroma requires it |
| Frame rate | source rate | ≤ 240 fps |
| GOP | 2 s | 1 s – 10 s |
| Encoder | `libx264` | any of the twelve |

The bounds are deliberately generous. They exist to catch a typo or a unit
mix-up — `6` where `6000` was meant — not to express any platform's policy.

### Keyframes

The GOP is expressed in **seconds**, not frames, so changing the frame rate does
not silently change the keyframe interval. It is emitted as `-g` and
`-keyint_min` at the same value with `sc_threshold 0`, which pins the interval
rather than letting scene cuts move it.

This is a real reason to use a rendition even at the same resolution: with
`-c:v copy` you inherit whatever keyframe interval OBS was set to, and a 10 s
interval breaks HLS and DASH packaging on the platform side.

### Per-encoder flags

| Encoder | Adds |
|---|---|
| `libx264` | `-preset veryfast -profile:v high -pix_fmt yuv420p` |
| `libx265` | `-preset veryfast -profile:v main -pix_fmt yuv420p` |
| `h264_nvenc` | `-preset p4 -rc cbr\|vbr -profile:v high` |
| `hevc_nvenc` | `-preset p4 -rc cbr\|vbr` |
| `h264_qsv` | `-preset veryfast -profile:v high` |
| `hevc_qsv` | `-preset veryfast` |
| `h264_videotoolbox` | `-realtime 1 -profile:v high` |
| `hevc_videotoolbox` | `-realtime 1` |
| `h264_vaapi` | `-vaapi_device <node>`, and `format=nv12,hwupload` on the filter chain |
| `hevc_vaapi` | the same |
| `h264_amf` | `-quality speed -usage transcoding -profile:v high` |
| `hevc_amf` | `-quality speed -usage transcoding` |

**`-profile:v high` is H.264-only.** HEVC's profiles are `main` / `main10` /
`rext`, and every HEVC encoder *refuses to open* when handed `high` rather than
ignoring it — `x265 [error]: unknown profile <high>`, `Unable to parse "profile"
option value "high"`. That is why the HEVC rows above are not their H.264 rows
with the name changed.

A profile is pinned at all only to stop a 10-bit or 4:2:2 ingest producing a
High10/422 stream that no platform will accept — and that is only safe to state
where we also state the pixel format. We do for the two **software** encoders,
so they pin one. We cannot for the **hardware** ones, which take whatever
surface format the driver hands them, so the HEVC hardware rows let the encoder
pick a profile that matches its own input. The H.264 hardware rows keep the
`high` they have always sent, which is a valid value for every one of them.

The VAAPI rows are the load-bearing ones: VAAPI encodes from GPU surfaces, so
without **both** the device and the `hwupload` filter tail it cannot open at
all. `hevc_vaapi` was missing both until #343 — it was selectable in the editor
and structurally unable to start.

---

## 5. Frame rate

| | Supported | How |
|---|---|---|
| Constant, at a rate you choose | Yes | `-r` is emitted when a rendition sets FPS |
| Source rate passed through | Yes | when FPS is unset, nothing is emitted |
| VFR → CFR normalisation | **Deliberately not done** | `-fps_mode` and `-vsync` are never set |

A variable-frame-rate source — screen capture, some phone encoders — passes
through a rendition with **its timing intact**. Measured on a fixture that is 30
fps nominal and about 18 fps actual: 72 frames over 3.967 s in, 72 frames over
3.967 s out. Nothing is dropped, nothing is duplicated, and the presentation
timestamps are carried through unchanged. Under `-c:v copy` the same is true for
the simpler reason that nothing touches the stream at all.

**Forcing CFR would be worse, and this was measured too.** Adding
`-fps_mode cfr` to the same fixture takes 72 frames to 120 — 48 duplicated
frames filling the gaps where the source had nothing to say, 66% more encoded
frames for no additional information. The default is the right behaviour, and
`TestAVFRSourceKeepsItsTimingThroughARendition` now fails if it changes.

One measurement trap, recorded because it produced a false alarm first: ffprobe
reports a **uniform** `duration_time` for every frame of an MPEG-TS, because the
container does not store per-frame durations and ffprobe derives them from
`r_frame_rate`. Read that field and a VFR stream looks like it was silently
resampled to CFR and lost a fifth of its running time. It was not. Presentation
timestamps are the ground truth; `duration_time` on MPEG-TS is a guess. #342.

---

## 6. Choosing

**Do not use a rendition** if the platform accepts your source. It is the whole
reason the CPU cost is flat.

**Use one** when a platform refuses your source resolution or bitrate, when you
need a keyframe interval the encoder is not sending, or when one destination
needs a smaller picture than the others.

**Add a GPU** when the number of *distinct* renditions across all sources is more
than a couple, or when you want Twitch Enhanced Broadcasting at all.
