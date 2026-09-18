# Encoding: what is copied, what is encoded, and what it costs

The short version: **video is copied by default and only encoded when you ask
for a rendition. Audio is encoded once per destination mix — unless that
destination copies its ingest tracks instead, which SRT and file destinations
can.** Everything below follows from that.

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
product makes — and the one way out of it is
[copying the audio as well](#copying-the-audio-as-well).

### What copy cannot check: the codec the ingest chose

`-c:v copy` ships the bytes OBS sent, including the bytes of a codec the
platform will not take. Selecting HEVC or AV1 in OBS produces Enhanced RTMP,
which polyemesis ingests happily, and FFmpeg will mux HEVC into FLV happily —
Enhanced RTMP defines the mapping. The platform then drops the stream. It
muxes cleanly, it uploads cleanly, and it looks correct at every point the
operator can see.

So a destination that is RTMP, has **no rendition**, and sits on an ingest
whose probed codec is not `h264` carries a warning in its status. Kick is the
only platform in the tree with a sourced refusal — its own help page, checked
2026-08-06, says H.264 only — so on Kick with an HEVC ingest the warning is
certain:

> The ingest is sending HEVC and this destination copies it. Kick documents
> that it accepts H.264 only and refuses H.265, so this stream will be
> rejected. Switch the encoder to H.264, or give this destination an H.264
> rendition.

Every other platform gets the hedge, because nothing in this repository
establishes what they accept: "Most RTMP destinations accept H.264 only, so
this may be rejected even though it uploads cleanly."

Nothing is refused. A custom RTMP endpoint that does take HEVC is a real
setup, and refusing a stream that would have worked is worse than the hazard.
The fix when the warning is right is the one it names: change the ingest
encoder to H.264, or put an H.264 rendition on that destination, which
re-encodes and so takes the ingest's codec out of the path entirely. SRT and
file destinations carry HEVC and AV1 without complaint and are never warned
about; neither is a destination with a rendition, or one whose ingest has not
been probed yet — "unknown" must never render as "H.264".

### Copying the audio as well

A destination can skip the mix entirely. Turn on **Copy the ingest tracks**
(destination editor → Audio; `audio.copy` over the API, off everywhere by
default) and that destination's argv loses `-filter_complex` and gains a
`-map 0:a:N` per selected track followed by `-c:a copy`. No decode, no mix, no
encoder — the archive or contribution feed carries the same bits the encoder
sent us, and that destination costs about what the video copy costs.

Track *selection* survives. The compiled routing profile still decides which
tracks go out and the role policy still removes the excluded ones, so the DMCA
switch keeps working. What is given up is everything the mix stage does to the
samples — and each of those is **refused at save time rather than ignored**,
because a setting that is silently dropped leaves the operator looking at a
form that says one thing and a stream that does another. A save fails, naming
the control to turn off, if the destination carries a limiter or loudness
normalization (`auto` and `off` are fine; they are not a request), a loudness
target, ducking, an audio delay, a mix-matrix gain other than unity, channel
routing between outputs, `mono`, or an audio codec other than `aac` — "nothing
is encoded, so the codec is whatever the ingest sent".

Two destination kinds refuse copy outright:

- **RTMP** — "copying audio is not available on an RTMP destination: platform
  ingests expect one encoded stereo track, so a copied multitrack stream would
  upload cleanly and be rejected."
- **Audio-only** — "its codec is chosen by the output container, not by the
  ingest", so copy has no meaning that could be honoured.

That leaves **SRT and file**, which is the shape of the feature. It matters for
sizing: the "one audio encode per destination" rule above counts only the
destinations that mix, so an archive or a contribution feed on copy is not in
that count.

One thing is deliberately *not* checked when you save: whether the output
container can carry the ingest's *audio* codec. Nothing has connected at save
time, so that codec is simply not known — guessing would either refuse
combinations that work (FFmpeg 8.1.2 muxes two copied AAC tracks into flv,
mpegts, matroska and mp4 without complaint) or bless ones that do not. A bad
combination fails loudly at start instead.

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

More precisely: what Twitch validates is the inventory the request *declares* —
vendor ID, device ID and driver version, against a list it does not publish. It
has no way to inspect your machine, which is why polyemesis asks you to fill the
inventory in on the Settings page rather than reading it: it sends what it is
told. `internal/multitrack/live_test.go` demonstrates the mechanism directly —
it declares an NVIDIA card and Twitch grants Enhanced Broadcasting, on an Apple
Silicon machine that has no such card in it.

> **EXPERIMENTAL — no broadcast has been published through a key Enhanced
> Broadcasting minted.** The negotiation is not the gap: those tests reach
> `ingest.twitch.tv` on every run and Twitch grants the VOD audio track and
> mints a key. Everything after that return is unobserved. Nothing is gated — a
> negotiation that does not succeed falls back to the ordinary Twitch ingest.

Renditions are the opposite: real work, and where a card earns its place.

## 3. Where a GPU actually helps

Cost scales with **distinct renditions**, not with destinations, and not with
ingests directly.

- One engine runs per source, so N ingests are N engines.
- A rendition is **shared and ref-counted**: one encode feeds every destination
  that selected it. Five destinations on one 1080p tier is one encode, not five.
- Two ingests each needing 1080p and 720p is four encodes. That is where a card
  stops being optional.

These three sentences are a claim about a bill, so they are measured rather than
asserted. `scripts/acceptance-ladder.sh` builds a real 1080p/720p/480p ladder off
one ingest and counts the encoder processes in the live process table: three
tiers are three encodes, a fourth destination joining an existing tier is still
three and joins the same process rather than restarting it, and the encode stops
only when its LAST subscriber leaves. It prints the CPU each tier actually cost.

Twelve encoders are offered: `libx264` and `libx265` in software, plus **ten
hardware** encoders — NVENC, QSV, VAAPI, VideoToolbox and AMF, each in H.264 and
HEVC. Those twelve are the **live** set — what a rendition may be put on. The
jobs that run off the live path draw from a different list with different flags,
including AV1 encoders no rendition can select; see
[Off the live path](#off-the-live-path).

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
| Video bitrate | 6000 kbps | 100 – 100 000 kbps |
| Ceiling (maxrate) | = bitrate | 0, or ≥ the bitrate, and ≤ 100 000 kbps |
| Rate window (bufsize) | 2 × ceiling | 0, or ≥ half the ceiling, and ≤ 400 000 kbps |
| Width, height | source dimension | 128 – 7680, and **even** — 4:2:0 chroma requires it |
| Frame rate | source rate | ≤ 240 fps |
| GOP | 2 s | 1 s – 10 s |
| Encoder | in the editor, the first **working** hardware encoder; `libx264` when an API payload omits the field | any of the twelve |
| Preset | `veryfast`, whatever the encoder | one word of letters, digits, `-`, `_` or `.`, 1 – 32 characters. Never empty |

The bounds are deliberately generous. They exist to catch a typo or a unit
mix-up — `6` where `6000` was meant — not to express any platform's policy.

**The encoder default is not `libx264` on a machine with a usable GPU.** The
create form seeds itself from `GET /api/v1/encoders`, whose `default`
field is the first encoder in the preference order — VideoToolbox, NVENC, QSV,
VA-API, AMF, then x264 — that *passed its probe*. Being listed by the build is
not evidence; defaulting to a listed-but-dead encoder is how an operator finds
out about libcuda after going live. `libx264` is that answer only when every
probe failed, or when no probe ran at all. So on a box with an NVIDIA card, a
rendition you clicked through without touching the encoder is already on
`h264_nvenc`, and the EXPERIMENTAL warning under
[Per-encoder flags](#per-encoder-flags) applies to your default rather than to
an opt-in. Check which encoder a rendition is actually on before sizing CPU
from this table. `docs/RENDITIONS.md` covers the reasoning.

The probe is *not* consulted when a field is missing from an API payload: a
`POST` that omits `encoder` gets `libx264` and one that omits `preset` gets
`veryfast`, so a short create request is software-encoded on a machine where
the editor would have picked the card. Name the encoder explicitly if you mean
the card.

### Keyframes

The GOP is expressed in **seconds**, not frames, so changing the frame rate does
not silently change the keyframe interval. It is emitted as `-g` and
`-keyint_min` at the same value with `sc_threshold 0`, which pins the interval
rather than letting scene cuts move it.

FFmpeg wants frames, so the seconds are multiplied by a frame rate and rounded
(never below 1). That rate is, in order: the rendition's own frame rate if it
sets one; otherwise the **probed source rate**; otherwise an assumed **30 fps**.

The third case is worth knowing about, because it is the one where the interval
in seconds is not the interval you get. A rendition that leaves FPS at `0` on a
source that has not been probed yet gets `2 × 30 = 60` frames — and if that
source turns out to be 60 fps, 60 frames is a keyframe every **one** second,
not two. Twice the keyframes, measurably more bitrate at the same quality, and
a segment duration a platform's HLS packager may not be expecting. Guessing low
is the deliberate direction: this failure costs a little bitrate, where guessing
*high* would stretch the same 2 s GOP to 4 s on a 30 fps source and break
platform-side segmenting outright. If a destination depends on an
exact keyframe cadence, set the rendition's frame rate explicitly rather than
leaving it at source — that takes the guess out of the arithmetic entirely.

This is a real reason to use a rendition even at the same resolution: with
`-c:v copy` you inherit whatever keyframe interval OBS was set to, and a 10 s
interval breaks HLS and DASH packaging on the platform side.

### Per-encoder flags

> **EXPERIMENTAL — the `_nvenc`, `_qsv`, `_vaapi` and `_amf` rows below are
> unconfirmed on real hardware.** Those values were read off FFmpeg's own option
> tables (`ffmpeg -h encoder=<name>`) inside a container, which answer on a
> machine with no such device in it at all. No NVENC, QSV or VA-API encode has
> been observed running with them, including the capped-VBR behaviour described
> under [Setting a ceiling](#setting-a-ceiling), whose entire effect is on this
> argv. Nothing is disabled — the encoders stay selectable, and a flag that is
> wrong shows up as an encoder that refuses to open, which the start gate
> reports by name.
>
> The `_videotoolbox` rows are **not** in that set and neither are `libx264` and
> `libx265`. `TestEveryConfiguredEncoderOpensWithItsOwnFlags`
> (`internal/ffmpeg/rendition_encoder_profiles_test.go`) runs a real encode per
> registered encoder using that encoder's own row from the table below — preset
> flag, rate control and the capped-VBR path included — and on macOS
> `h264_videotoolbox` and `hevc_videotoolbox` both pass. It is the strongest
> evidence any row here has, and it answers for whichever encoders the machine
> running it registers: a CI runner with an NVIDIA card would retire the
> paragraph above on its own.

| Encoder | Adds (the preset shown is the default; see below) |
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

### The preset is a field, not a constant

The preset in each row above is the value used when a spec carries none, and a
rendition always carries one. The editor's **Encoder preset** box (`preset` over
the API) is free text — the vocabulary is per-encoder and changes between FFmpeg
releases, so it is validated for shape only: one word of letters, digits, `-`,
`_` or `.`, 1 to 32 characters, refused empty. Whatever is in that box is what
lands on the command line, in place of the row's default. `ffmpeg -h
encoder=<name>` lists what your build actually accepts.

This is the CPU-for-quality lever on a rendition, and it is the one knob in the
table you are meant to turn. `libx264` at `medium` rather than `veryfast` costs
noticeably more CPU per encode and gives a better picture at the same bitrate;
`h264_nvenc` at `p6` rather than `p4` does the same on the card — NVENC's
`p1`–`p7` replaced the named presets, fastest to slowest, with `p4` the middle.
AMF spells the same knob `-quality`, which is why its rows read `-quality
speed`, and a value you set there goes after `-quality`.

**The seeded value is `veryfast` whatever the encoder.** A new rendition — from
the form, from one of the starting-point tiers, or from an API create that omits
the field — carries `veryfast`, while the encoder beside it may have been set to
this machine's working hardware default. `veryfast` is an x264/x265/QSV
spelling; whether it means anything to NVENC or AMF is that encoder's business,
so when you move a rendition to another family, set the preset in the same edit
rather than leaving the one that came with the tier.

**Four encoders ignore it, silently.** `h264_videotoolbox`,
`hevc_videotoolbox`, `h264_vaapi` and `hevc_vaapi` have no preset option at
all, so the field is **dropped** rather than passed — sending one makes FFmpeg
complain about an unused AVOption on every restart. The consequence is that a
rendition on VideoToolbox or VA-API shows `veryfast` in its editor and hands
its encoder nothing: the box is inert for those four, and no error says so.
VideoToolbox's nearest lever is the `-realtime 1` its row already carries, and
VA-API takes its behaviour from the device and the filter chain instead.

An encoder with no row in the table at all — a name this product has no profile
for — is handed `-preset <your value>` and nothing else. No default is assumed
there, because assuming one would break precisely the encoders that have no such
option.

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

### Off the live path

Everything above is the rendition ladder. Three other jobs encode video, each
with its own encoder list — overlapping the twelve in the software names and, in
the archive's case, in three of the hardware ones (`hevc_nvenc`, `hevc_qsv`,
`hevc_vaapi`), and reaching AV1 encoders no rendition can select. None of them uses CBR: they
are the CRF row of the rate-control table.

**Archive transcodes** re-encode a finished recording to save disk. The codec is
`hevc` (the default) or `av1`, and each has its own encoder preference:

| Codec | Preference order | Flags per encoder |
|---|---|---|
| `hevc` | `libx265`, `hevc_nvenc`, `hevc_qsv`, `hevc_vaapi` | `-crf 28 -preset medium -x265-params log-level=error`; `-cq 28 -preset p5 -rc vbr -b:v 0`; `-global_quality 28 -preset medium`; `-qp 28` with its own `-vaapi_device` |
| `av1` | `libsvtav1`, `libaom-av1`, `av1_nvenc` | `-crf 32 -preset 6`; `-crf 32 -b:v 0 -cpu-used 4 -row-mt 1`; `-cq 32 -preset p5 -rc vbr -b:v 0` |

**The order is software first, which is the reverse of the live ladder and is
not an oversight.** A rendition optimises for throughput because it runs live;
an archive optimises for quality per bit because it is the last encode this
footage will ever get, and fixed-function hardware encoders are meaningfully
worse per bit than x265 or SVT-AV1 at any speed. Hardware is *offered* for the
operator with a thousand hours to get through, not chosen for them — so sizing a
box for overnight archiving means sizing CPU, and a GPU will sit idle unless the
job names one of the hardware encoders. The quality bounds on the number an
archive job may ask for are in [API.md](API.md): the floor is `1`, and the
ceiling is the profile's own default when the job replaces the original, or that
default plus 6 when it writes a file beside it.

**Media proxies** are the 360p scrub copies the library plays. They default to
`libx264` at `-crf 28 -preset veryfast`, 2 s GOP, 96 kbps AAC, and the hardware
probe is deliberately *not* consulted: a 360p encode is a rounding error on any
CPU that can run this product, while the GPU is the one resource the live stream
cannot share. Name a different encoder and it gets `-crf` only if it is one of
the seven whose `-crf` means what we mean by it — `libx264`, `libx264rgb`,
`libx265`, `libvpx-vp9`, `libsvtav1`, `libaom-av1`, `librav1e`. Anything else
gets average-bitrate rate control at 700 kbps instead, because hardware encoders
spell their quality knob differently and disagree about its direction.

**The clipper** re-encodes only the leading partial GOP of a cut and copies the
rest. That head encode gets `-bf 0` always, plus `-crf 16 -preset veryfast` when
the encoder is `libx264` or `libx265` — a fraction of a second of video, where
the wait is what is being optimised. `-threads` is emitted for every encoder,
hardware included: it is the only lever that package has over how much of the
machine a cut takes, and the live stream's claim comes first.

---

## 5. Frame rate

| | Supported | How |
|---|---|---|
| Constant, at a rate you choose | Yes | an `fps=N` filter at the head of the chain, **and** `-r N` on the encoder |
| Source rate passed through | Yes | when FPS is unset, neither is emitted |
| VFR → CFR normalisation | **Deliberately not done** | `-fps_mode` and `-vsync` are never set |

**Setting a frame rate emits two things, and the filter is the one that does the
work.** `fps=N` goes at the head of the filter chain — after any deinterlace,
before the scale — and it is what actually drops and duplicates frames to reach
N. `-r N` stays on the command line as well, because that is what the muxer
writes into the container, and a stream whose header disagrees with its frames
is one some players refuse.

The ordering is measured, not tidy. `-r` alone drops frames at the encoder,
which is *after* the filter graph, so a 60 → 30 rendition used to scale all
sixty frames and then discard half of them. On a 6-core Haswell VPS, 4K60 →
1080p30 at veryfast/6000k: 2.13x realtime scaling first, 2.49x decimating first
— 17% for free. The control, 4K60 → 1080p60 where nothing is dropped, moved
1.50x to 1.53x, which is what says the gain is the avoided scaling rather than
measurement drift.

The consequence for VFR is the important one: **the paragraph below holds when
FPS is left at `0`.** Set a frame rate on a rendition and you have asked for
exactly the frame duplication described here — the `fps` filter fills the gaps a
variable-rate source left, which is the right behaviour for a platform that
demands CFR and the wrong one if you wanted the source's timing. Leave FPS
unset and nothing touches the timing at all.

A variable-frame-rate source — screen capture, some phone encoders — passes
through a rendition with **its timing intact**, when that rendition sets no
frame rate. Measured on a fixture that is 30 fps nominal and about 18 fps
actual: 72 frames over 3.967 s in, 72 frames over 3.967 s out. Nothing is
dropped, nothing is duplicated, and the presentation timestamps are carried
through unchanged. Under `-c:v copy` the same is true for
the simpler reason that nothing touches the stream at all.

**Forcing CFR would be worse, and this was measured too.** Adding
`-fps_mode cfr` to the same fixture takes 72 frames to 120 — 48 duplicated
frames filling the gaps where the source had nothing to say, 66% more encoded
frames for no additional information. The default is the right behaviour, and
`TestAVFRSourceKeepsItsTimingThroughARendition` now fails if it changes. That
test builds its argv with FPS at `0` on purpose — the rendition that *sets* a
frame rate is not the case it covers, which is the other reason to read the
timing guarantee as scoped to an unset rate.

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
need a keyframe interval the encoder is not sending, when one destination needs
a smaller picture than the others, or when your ingest is HEVC or AV1 and the
destination is RTMP — there the rendition is what keeps the ingest's codec off
the wire (§1).

**Add a GPU** when the number of *distinct* renditions across all sources is more
than a couple, or when you want Twitch Enhanced Broadcasting at all — remembering
that for Enhanced Broadcasting the card is a gate rather than a workload (§2),
and that the feature carries an EXPERIMENTAL label: the negotiation is proven
against Twitch, a broadcast published through the key it mints is not.

If a rendition is the reason for the card, note which encoder family you are
buying into: the flags polyemesis hands NVENC, QSV, VA-API and AMF are
unconfirmed on silicon, per [§ Per-encoder flags](#per-encoder-flags). They are
not disabled and a wrong flag surfaces as an encoder that refuses to open, named
by the start gate — but you would be the first to run them.
