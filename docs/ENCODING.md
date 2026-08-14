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

Twelve hardware encoders are supported — NVENC, QSV, VAAPI, VideoToolbox and AMF
in H.264 and HEVC — and each is **probed with a real test encode** before being
offered, rather than assumed from the driver being present. A machine where every
probe fails software-encodes; it does not refuse to boot.

**A rendition does not touch your audio.** The argv carries `-map 0:a -c:a copy`,
so every ingest track arrives bit-identical and per-destination routing
downstream still sees the full multitrack ingest. If that ever became an audio
encode or a mixdown, the product's differentiator would be gone.

---

## 4. Rate control

| Mode | Live egress | How |
|---|---|---|
| **CBR** | **Yes, and the default** | `-b:v X -maxrate X -bufsize 2X`, and NVENC additionally gets `-rc cbr` |
| **Capped VBR** | **Yes** | Set a ceiling above the target bitrate; see below |
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

### Defaults

| Setting | Default | Bounds |
|---|---|---|
| Video bitrate | 4500 kbps | 100 – 100 000 kbps |
| Ceiling (maxrate) | = bitrate | 0, or ≥ the bitrate |
| Rate window (bufsize) | 2 × ceiling | 0, or ≥ half the ceiling |
| GOP | 2 s | 1 s – 10 s |
| Encoder | `libx264` | any probed encoder |

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
| `libx264` | `-profile:v high -pix_fmt yuv420p` |
| NVENC | `-rc cbr -profile:v high` |
| QSV | `-profile:v high` |
| VideoToolbox | `-realtime 1 -profile:v high` |
| AMF | `-usage transcoding -profile:v high` |

---

## 5. Frame rate

| | Supported | How |
|---|---|---|
| Constant, at a rate you choose | Yes | `-r` is emitted when a rendition sets FPS |
| Source rate passed through | Yes | when FPS is unset, nothing is emitted |
| Explicit VFR → CFR normalisation | **No** | `-fps_mode` and `-vsync` are never set; FFmpeg's default applies |

A variable-frame-rate source — screen capture, some phone encoders — is not
normalised. Under `-c:v copy` that is fine and the timing passes through
untouched. Through a rendition the result depends on FFmpeg's default handling
rather than on anything this project decided, and nothing tests it. Tracked
in #342.

---

## 6. Choosing

**Do not use a rendition** if the platform accepts your source. It is the whole
reason the CPU cost is flat.

**Use one** when a platform refuses your source resolution or bitrate, when you
need a keyframe interval the encoder is not sending, or when one destination
needs a smaller picture than the others.

**Add a GPU** when the number of *distinct* renditions across all sources is more
than a couple, or when you want Twitch Enhanced Broadcasting at all.
