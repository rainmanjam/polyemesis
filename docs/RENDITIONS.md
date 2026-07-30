# Video renditions

A **rendition** is a named video output profile that several destinations can
share. It is how polyemesis serves platforms that will not accept your source
video without giving up the thing that makes it fast.

- [The problem](#the-problem)
- [A rendition re-encodes video only](#a-rendition-re-encodes-video-only)
- [Passthrough is a rendition](#passthrough-is-a-rendition)
- [A rendition only runs when something needs it](#a-rendition-only-runs-when-something-needs-it)
- [Presets are starting points](#presets-are-starting-points-not-limits)
- [Hardware encoders](#hardware-encoders)

---

## The problem

You ingest 4K60. YouTube will take it. Twitch, Kick and X cap well below 4K, so
they reject it — or accept it and quietly transcode it into something worse.

Without renditions the only way out is to drop **your whole ingest** to the
lowest common denominator: YouTube gets 1080p because Kick cannot do 4K. Running
one polyemesis destination per resolution does not help either, because each
destination copies video — none of them can change it.

Destinations *select* a rendition rather than owning one, so three platforms
that all want 1080p60 cost **one** encode, not three.

```
relay ─────────────────────────► dest:youtube   -c:v copy + audio graph A
  │                               (rendition = passthrough, zero cost)
  └──► rendition "1080p60 6M"     ONE encode
         (video encoded, ALL audio tracks copied through untouched)
         └──► rendition's own relay hub
                ├─► dest:twitch   -c:v copy + audio graph B
                ├─► dest:kick     -c:v copy + audio graph C
                └─► dest:x        -c:v copy + audio graph D
```

## A rendition re-encodes video only

This is the load-bearing rule, and it is worth stating plainly: a rendition
encodes **video**, and passes **every audio track through with `-c:a copy`**. It
never mixes, never downmixes, never re-encodes audio. There is no audio setting
on a rendition and there never will be one.

That is what keeps the differentiator intact on top of shared video. The
destinations downstream of a rendition still receive the full multitrack stream,
still compile their own `-filter_complex` from their own routing profile, and
still do `-c:v copy` — exactly as a passthrough destination does. Audio is
encoded once, at the destination, and never twice.

Consequently, changing which rendition a destination is on does not change its
audio, and changing its audio routing does not restart the rendition or disturb
the other destinations sharing it.

## Passthrough is a rendition

**Passthrough** is the zero-cost default: no process, no encode. The destination
subscribes straight to the ingest relay and copies the source video, which is
precisely what every destination has always done.

Every destination stays on passthrough with no action from you. This feature is
strictly additive: if you never create a rendition, nothing about your install
changes.

## A rendition only runs when something needs it

The encode starts when the **first enabled** destination selects the rendition,
and stops when the last one releases it. A rendition that nothing enabled points
at has no process and burns no CPU — creating a tier you are not using yet is
free, and stopping the last destination on a tier stops its encode too.

| Action | What restarts |
|---|---|
| Editing a rendition | That encode, and exactly the destinations reading it |
| Renaming it, or editing its note | Nothing |
| Deleting it | Its destinations fall back to passthrough and keep running |

Deleting a rendition does **not** delete its destinations — but it does mean
they are suddenly being handed your source video, so the delete tells you how
many destinations that just happened to. Check the source still fits what each
of those platforms accepts.

Each destination's card on the dashboard shows the rendition it is on directly
above the audio tracks it receives, so "what video and what audio does this
platform get" is one glance, not two.

## Presets are starting points, not limits

The rendition editor offers these as editable starting points:

| Preset | Size | Rate | Video bitrate | Encoder |
|---|---|---|---|---|
| Source passthrough | source | source | — (no encode) | — |
| 1080p60 | 1920×1080 | 60 fps | 6000 kbps | `libx264`, `veryfast`, 2 s GOP |
| 1080p30 | 1920×1080 | 30 fps | 4500 kbps | `libx264`, `veryfast`, 2 s GOP |
| 720p60 | 1280×720 | 60 fps | 4500 kbps | `libx264`, `veryfast`, 2 s GOP |
| 720p30 | 1280×720 | 30 fps | 3000 kbps | `libx264`, `veryfast`, 2 s GOP |

> **Verify current limits with the platform.**
>
> These are not authoritative ceilings and are not presented as any platform's
> policy. Published limits change without notice, and they differ by partner,
> affiliate and beta status for two different accounts on the same platform.
> Being confidently wrong about one of these numbers breaks a live stream, so
> where we were unsure we picked the *lower* value: an under-spec stream is
> watchable, an over-spec one is rejected at the ingest.
>
> Check your own account's current limits, then edit the rendition. Every field
> is yours to change.

Keyframe interval is set in **seconds**, not frames, so it stays correct when
you change the frame rate. Two seconds suits every live platform we know of.

## Aspect handling — one ladder rung, not a second pipeline

When the rendition's shape does not match the source's, something has to give.
A 9:16 rendition of a 16:9 ingest is **one more entry in the ladder**, encoded
once and shared, rather than a parallel pipeline.

| Mode | What happens |
|---|---|
| **Stretch to fit** | Scales to the target size and lets the picture distort. Anamorphic, almost never what anyone wants — but it is what renditions did before the other modes existed, so it stays the default |
| **Crop to fill** | Centre-crops to the target shape, then scales. Subjects keep their on-screen size; the edges of the frame are gone |
| **Letterbox** | Scales the whole frame to fit and fills the rest with a flat colour. Nothing is lost, but a 16:9 source on a 9:16 canvas is mostly bars |
| **Blurred fill** | Fills the remainder with a blurred, cropped-to-fill copy of the frame itself |

Blurred fill is the convention vertical feeds have settled on, and it is the
difference between a repurposed landscape stream looking deliberate and looking
lazy. The blur is computed on a 1/8-scale proxy, because a gaussian wide enough
to read as "background" costs more per frame at 1080p than the H.264 encode it
feeds — and the upscale back to full size does most of the blurring for free.

**Aspect handling needs both a width and a height.** With one axis free the
scale already preserves the aspect ratio, so there is no shape to convert to and
the control is disabled rather than saved as something quietly inert.

## Deinterlacing

For SDI bridges, capture cards and legacy broadcast kit. Progressive sources —
which is almost everyone — should leave this off, because deinterlacing a
progressive frame softens it for no gain.

| Mode | What happens |
|---|---|
| **Off** | Default |
| **Only interlaced frames** | Touches only frames the source flagged as interlaced, so progressive frames pass through untouched. The right choice for anything mixed — a camera that switches modes, a playout chain splicing SD and HD |
| **Every frame** | Unconditional. For sources that are interlaced but do not say so — plenty of capture kit flags everything progressive regardless of what it was fed, and on those "only interlaced" is a no-op that looks like a broken setting |

It uses `bwdif` rather than `yadif` — the same idea done better, for a few
percent more CPU — in `send_frame` mode, which emits one progressive frame per
input frame rather than one per *field*. `send_field` would double the frame
rate, which silently doubles the bitrate the platform receives and breaks the
keyframe arithmetic computed from the source rate.

**Deinterlacing runs first, before any scaling.** That ordering is load-bearing
rather than tidy: scaling interlaced content blends the two fields together, and
once that has happened the combing is baked into the pixels and no later filter
can remove it.

## Watermarks

A rendition can burn a still image into the picture — a logo, a sponsor card, a
channel mark.

It lives here, on the rendition, rather than on a destination, and that is not
an arrangement of the settings page. A destination copies video with
`-c:v copy`; there is no mechanism by which a copied bitstream acquires a logo.
Burning one in *is* a re-encode, so it belongs where re-encoding is already the
contract.

The practical consequence is worth being clear about:

- **Adding a watermark to a rendition costs no new process.** A few percent CPU
  on an encode that was already running. This case is cheap.
- **Giving two platforms *different* branding costs a second full encode.** They
  are different pictures, so they are different encodes — roughly 1.5–3 cores on
  x264 `veryfast`, or near-zero CPU on NVENC at the cost of one of its 3–8
  concurrent sessions.
- **A clean feed is a destination on no rendition, or on one with no watermark.**
  Nothing to switch off.

### Everything is a percentage

| Setting | Means |
|---|---|
| **Width** | the image's width as a percentage of the frame's width |
| **Margin X / Y** | the gap from the anchored edges, as percentages of the frame |
| **Position** | one of nine anchors — corners, edge centres, or the middle |
| **Opacity** | 1–100%; the image's own transparency is respected either way |

Percentages rather than pixels, because the same watermark has to be correct on
a 1920×1080 tier and a 1080×1920 one. A logo placed 40 px from the right edge of
a landscape frame is in a sensible place; the same 40 px on a vertical frame is
not the same place at all, and a size in pixels that reads well on one lands
comically large or invisible on the other.

Margins are ignored on a centred axis — a centred logo is centred.

### Getting the image in

Put the file in `<data-directory>/overlays/` and give the rendition the relative
path, `overlays/logo.png`. PNG with transparency is the usual choice.

The path is confined to the data directory, the same way a slate image and a
`file://` pull source are. An absolute path or a `..` is refused rather than
resolved: the field is operator input that becomes an FFmpeg argument, and
anything else would be a file-read primitive for whoever reaches the API.

### Two things that will catch you

**A watermarked rendition needs an explicit width *and* height.** The image is
sized as a percentage of the output, so the output has to have a size. A
rendition with one axis free is refused when you save it rather than starting
and quietly having no logo.

**Editing a watermark restarts the encode**, and therefore every destination
riding that rendition. Nudging a logo by 2% while live drops them for a second
or two. Replacing the image file has the same effect, and deliberately so — the
alternative is an encoder that keeps compositing the picture you just replaced.

## Text overlays

A rendition can also burn in a line of text. The settings mirror the watermark's
reasoning — percentages, not pixels:

| Setting | Means |
|---|---|
| **Content** | the line to draw |
| **Font** | Inter Regular or Inter Bold, both shipped embedded |
| **Position** | the same nine anchors a watermark uses |
| **Size** | as a percentage of frame height |
| **Colour** | the text colour |
| **Margin X / Y** | as percentages of the frame |
| **Box** | an optional background box, with its own colour and opacity |

Fonts are embedded rather than assumed because FFmpeg's `drawtext` takes a font
*path*, not bytes, and a container image routinely has neither fontconfig nor a
single font file on it. Asking the operator to supply one would make the feature
work on a developer's laptop and fail in Docker.

**`drawtext` is optional in FFmpeg, not guaranteed.** A build without libfreetype
has no way to render text at all — the FFmpeg in Homebrew is sometimes one of
them. polyemesis probes for the filter and, when it is missing, runs the
rendition without the text rather than refusing to start. Dropping the text
keeps the picture up, which is the right way round: nobody watching would prefer
a black screen with correct typography.

Editing text restarts the encode, exactly as editing a watermark does.

### What this is not, yet

No clock, no viewer counts, no animation, no browser sources. Those are designed
and costed in [the roadmap](roadmap/OVERLAYS.md); they are not built.

## Hardware encoders

At startup polyemesis **encodes one frame** with each encoder your FFmpeg
registers, and keeps the exit status:

```bash
ffmpeg -f lavfi -i testsrc2=size=320x240:rate=1 -frames:v 1 -c:v h264_nvenc -f null -
```

That is the only test that means anything. `ffmpeg -encoders` lists what the
*build* was compiled with, not what the *machine* can do: a stock Ubuntu FFmpeg
lists `h264_nvenc`, `h264_qsv`, `h264_vaapi` and `h264_amf` on a box with no GPU
in it at all.

The editor offers only what encoded a frame here, and shows everything else
greyed out with FFmpeg's own reason — `Cannot load libcuda.so.1`, `No VA display
found for device /dev/dri/renderD128` — so you find out at the dropdown rather
than after you have gone live. A rendition saved on an encoder that later stops
working is refused at start with the same message, instead of crash-looping.

The scan is bounded, runs its probes concurrently, and cannot fail the launch:
if it cannot run at all, every encoder stays on offer and the product falls back
to software. Measured cost on the development machine: **218 ms** added to
startup.

`Renditions → re-detect hardware` re-runs the whole thing without a restart,
which is what you want after installing a driver or passing a GPU into a
container.

| Family | Encoders | Notes |
|---|---|---|
| Software | `libx264`, `libx265` | Always available. Identical rate control everywhere. |
| NVIDIA | `h264_nvenc`, `hevc_nvenc` | Presets are `p1`–`p7`; `p4` is the honest middle. |
| Intel Quick Sync | `h264_qsv`, `hevc_qsv` | Needs a working VA-API/QSV runtime, not just the CPU. |
| Apple | `h264_videotoolbox`, `hevc_videotoolbox` | No preset knob; `-realtime` is the lever. |
| VA-API (Linux) | `h264_vaapi`, `hevc_vaapi` | Needs a render node, `/dev/dri/renderD128` by default. This is the AMD path on Linux. |
| AMD | `h264_amf`, `hevc_amf` | Windows. Ubuntu's packaged FFmpeg contains no `*_amf` encoder at all — on Linux, use VA-API for AMD. |

A **working** hardware encoder is the default for a new rendition, because a
machine with a usable GPU that quietly software-encodes cannot serve the feature
the GPU was bought for. `libx264` is the default everywhere else, and is always
selectable: at a given bitrate it still beats every fixed-function encoder on
quality, so choosing it over hardware is a legitimate trade of headroom for
picture.

See [HARDWARE.md](HARDWARE.md) for per-vendor container images
(`Dockerfile.cuda`, `Dockerfile.vaapi`), the GPU passthrough flags, and what each
driver error message actually means.

### Be realistic about software 4K60

`libx264` at 4K60 is not a workload a normal streaming box handles. Even at
`veryfast` it needs a very high core count to hold realtime, and this machine is
*already* running your ingest, your recorder, the preview and one FFmpeg per
destination. If it cannot keep up, the encode falls behind realtime and every
destination on that rendition suffers.

If you are ingesting 4K60 and need it re-encoded, use a hardware encoder. If you
have no hardware encoder, rendition *down* from 4K rather than at 4K — that is
what renditions are for.

Note also that most RTMP ingests accept **H.264 only**. The HEVC encoders are
listed because they are real and occasionally useful (SRT, a file destination,
an ingest you control), not because a live platform is likely to take one.

---

## See also

- [ARCHITECTURE.md](ARCHITECTURE.md#3-renditions--the-shared-video-encode) — hub
  topology, ref counting and reconcile order
- [HARDWARE.md](HARDWARE.md) — GPU passthrough and driver errors
- [PLATFORMS.md](PLATFORMS.md) — what each platform will accept
- [AUDIO-ROUTING.md](AUDIO-ROUTING.md) — the audio a rendition never touches
