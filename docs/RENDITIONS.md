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

**A rendition belongs to one programme.** It re-encodes exactly one ingest, so
it is created against a source — `POST /api/v1/renditions` refuses a body with
no `"sourceId"` — and only that source's destinations can select it. Pointing a
destination at another programme's rendition is refused when you save it:

```
invalid destination: rendition 4 ("1080p60 6M") belongs to source 1, but this
destination belongs to source 2. A destination can only select a rendition from
its own programme, because only that programme's engine runs it. Pick one of
source 2's renditions, or leave this destination on passthrough.
```

That is not house policy, it is who runs the process: each programme's engine
reconciles only its own renditions, so a destination wired across programmes
would find no encode to read, and its card would explain itself with "rendition
4 is no longer available" — a sentence that is false, because rendition 4 exists
and is encoding under the other programme.

On a single-source install, which is most of them, none of this is visible. On a
multi-programme one it means the shared-encode arithmetic is **per programme**:
two programmes that both want 1080p60 need a rendition each, and that is two
encodes, not one. There is also no request that moves a rendition between
programmes — a `PUT` carrying a different `sourceId` is refused for the same
reason — so make the second one where it belongs and point its destinations at
that.

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

The encode starts when the **first enabled consumer** selects the rendition, and
stops when the last one releases it. A rendition that nothing enabled points at
has no process and burns no CPU — creating a tier you are not using yet is free.

**Destinations are not the only consumers.** An enabled playout variant that
names a rendition counts exactly as an enabled destination does: the ref count
the engine reconciles against is this programme's enabled destinations *plus*
every enabled variant in `playout.variants` whose `renditionId` is set (none of
them, if playout itself is switched off). Two consequences an operator hunting
CPU should know before they go looking for a leak:

- A rendition can be encoding with **every destination on that tier disabled**,
  because the public player is reading it. The ref count is not stuck; it is
  counting something that is not on the destinations list.
- Disabling your last destination on a tier does **not** give the core back
  while a playout variant is still on it. Disable the variant, or move it to
  another tier, and the encode stops on the next reconcile.

The consumer figure on the rendition's card includes the playout refs, so a card
showing a running process never reads "0 consumers" — if it says 2 and you can
only find one destination, the other is a variant.

| Action | What restarts |
|---|---|
| Editing a rendition | That encode, and exactly the destinations and playout variants reading it |
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

| Setting | Means | Range |
|---|---|---|
| **Width** | the image's width as a percentage of the frame's width | 1–100% (`widthPct` 0.01–1.0) |
| **Margin X / Y** | the gap from the anchored edges, as percentages of the frame | 0–45% each (`marginXPct`, `marginYPct` 0–0.45) |
| **Position** | one of nine anchors — corners, edge centres, or the middle | `top-left`, `top-center`, `top-right`, `middle-left`, `center`, `middle-right`, `bottom-left`, `bottom-center`, `bottom-right` |
| **Opacity** | the whole watermark's transparency; the image's own alpha is respected either way | 0–100% (`opacity` 0–1) |

Percentages rather than pixels, because the same watermark has to be correct on
a 1920×1080 tier and a 1080×1920 one. A logo placed 40 px from the right edge of
a landscape frame is in a sensible place; the same 40 px on a vertical frame is
not the same place at all, and a size in pixels that reads well on one lands
comically large or invisible on the other.

Those ranges are refused on save, not clamped, and the refusal quotes the number
you sent: `overlay width 12.000 out of range (0.01-1.00 of the output width)`,
`overlay horizontal margin 0.500 out of range (0-0.45)`. The margin ceiling is
45% rather than 50% because a margin at half the canvas pushes an edge-anchored
logo out past the opposite edge, and a watermark rendered off-frame is
indistinguishable from one that never rendered at all.

**The API's unit is a fraction, not a percentage.** The browser form shows whole
percentages and divides by 100 on its way out; what is stored, and what
`POST`/`PUT /api/v1/renditions` expects, is 0–1. A script that sends
`"widthPct": 12` is asking for twelve times the frame width and gets the save
refused. Worked example — a logo 12% of the frame wide, 3% in from the
bottom-right corner, at 80% opacity:

```bash
curl -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer pmk_..." https://host:8080/api/v1/renditions \
  -d '{
    "sourceId": 1,
    "name": "1080p60 6M",
    "width": 1920, "height": 1080, "fps": 60, "videoBitrate": 6000,
    "overlay": {
      "image": "overlays/logo.png",
      "anchor": "bottom-right",
      "widthPct": 0.12,
      "marginXPct": 0.03,
      "marginYPct": 0.03,
      "opacity": 0.8
    }
  }'
```

**An opacity of `0` means fully opaque, not invisible.** Zero is what a row
saved before the field existed carries, and rendering those as nothing would
have made every pre-existing watermark disappear on upgrade — so zero is read as
1. It also means you cannot hide a logo by turning the opacity down to nothing:
an invisible watermark is indistinguishable from a broken one, and the operator
did ask for a watermark. Clear the image path instead.

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

| Setting | Means | Range |
|---|---|---|
| **Content** | the line to draw — **one** line | at most 200 characters; a line break or NUL is refused, not escaped |
| **Font** | Inter Regular or Inter Bold, shipped embedded — or your own, dropped in `<data-directory>/fonts/` | a bare filename, at most 128 characters; empty means `Inter-Regular.ttf` |
| **Position** | the same nine anchors a watermark uses | the same nine names, `top-left` to `bottom-right` |
| **Size** | as a percentage of frame height | 1–50% (`sizePct` 0.01–0.5) |
| **Colour** | the text colour | a name or hex, optionally `@alpha`; at most 32 characters; empty means `white` |
| **Margin X / Y** | as percentages of the frame | 0–45% each (`marginXPct`, `marginYPct` 0–0.45) |
| **Box** | an optional background box, with its own colour and opacity | same colour syntax, empty means `black`; opacity 0–100% (`boxOpacity` 0–1) |

The fractions-not-percentages rule the watermark has applies here too: `sizePct`
is `0.05` for 5% of the frame height, not `5`. The 50% size ceiling is already a
caption half the picture tall — above it nothing legible fits — and 1% is where
type stops surviving the encode, under four pixels on a 360p tier.

**Content is a single line, capped at 200 characters.** A newline would end the
filter argument and a NUL would truncate the C string FFmpeg receives, so both
are refused at save (`text contains a line break or control character; it must
be a single line`) rather than escaped or silently stripped. A two-line lower
third is not a shorter string away — it is a feature with its own line-spacing
question, and it is not built. The text is never interpreted, either: `drawtext`
runs with `expansion=none`, so a `%` in a station name is a glyph and not a
directive.

**Colours are names and hex only, and the parser here is stricter than
FFmpeg's.** Before the optional `@`, only letters, digits and `#` are accepted;
after it, a number from 0 to 1. That is deliberate — the value becomes a filter
argument, and a validator that took arbitrary punctuation would be one escaping
bug away from letting a database row rewrite the filtergraph.

| Accepted | Refused |
|---|---|
| `white`, `black`, `yellow` | `rgb(255,255,255)` — `text colour "rgb(255,255,255)" must be a colour name or 0xRRGGBB, optionally @alpha` |
| `0xFFCC00` | `white @ 0.8` — the spaces are not part of the syntax |
| `white@0.8`, `0x000000@0.5` | `white@80%` — `text colour alpha "80%" must be a number from 0 to 1` |

One box subtlety worth knowing before you fight it: the box opacity is folded
into the box colour as FFmpeg's `colour@alpha`, so a box colour that already
carries its own `@alpha` wins and the opacity field is ignored — appending a
second alpha would produce a colour FFmpeg rejects. A box opacity of exactly 0
or 1 also passes the colour through untouched, which means a fully opaque slab;
it is the values in between that make the box readable over a picture.

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

**A font file that will not resolve does the same thing, and comes back by
itself.** Validation refuses a font name that is not a bare filename when you
save, so this only happens when the file is removed from under a running
install — renamed, tidied or replaced in `<data-directory>/fonts/` mid-broadcast.
The rendition then runs with no text rather than failing: taking a live output
off air over a caption is worse, and quietly substituting a different font ships
a frame you did not design. Nothing on the card says the font is the reason, so
if a caption vanishes and the picture stays up, look at the fonts directory
before you look at the text settings. The fix is to put the file back: the
encode's signature includes the font file's size and modification time, and
records a missing font as missing rather than omitting it, so the caption
returns on the next reconcile the moment the file reappears.

Editing text restarts the encode, exactly as editing a watermark does — and so
does replacing a font file with a corrected version of the same name, for the
same reason replacing the watermark image does.

### What this is not, yet

No clock, no viewer counts, no animation, no browser sources. Those are designed
and costed in [the roadmap](roadmap/OVERLAYS.md); they are not built.

## Hardware encoders

> **EXPERIMENTAL — the flags polyemesis hands an NVENC, QSV, VA-API or AMF
> encoder have not been confirmed on real hardware.** The probe described below
> is genuine evidence about whether an encoder *opens* on your machine; it says
> nothing about whether the rate control, preset and profile flags then behave
> as documented, and for those four families they were read out of FFmpeg's
> option tables rather than measured against silicon. No NVENC, QSV or VA-API
> encode has been observed. Every encoder stays selectable and there is no flag
> to turn this off; see
> [ENCODING.md § Per-encoder flags](ENCODING.md#per-encoder-flags).
>
> **VideoToolbox is not in that set.**
> `TestEveryConfiguredEncoderOpensWithItsOwnFlags` runs a real encode per
> registered encoder with that encoder's own flags, and
> `h264_videotoolbox`/`hevc_videotoolbox` pass on macOS.

At startup polyemesis **encodes one frame** with each of the six encoders it
probes — the five H.264 hardware encoders and `libx264` — and keeps the exit
status:

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

**The six HEVC encoders are not probed.** Each takes its sibling's verdict:
`hevc_nvenc` opens the same device through the same driver as `h264_nvenc`, so
if one cannot load libcuda neither can the other. The editor says when a verdict
was inferred rather than measured. The inference is good enough to stop offering
a choice and deliberately not good enough to refuse a start, so a rendition
already saved on an HEVC encoder is never killed on a guess.

The scan is bounded, runs its probes concurrently, and cannot fail the launch:
if it cannot run at all, every encoder stays on offer and the product falls back
to software. Measured cost on the development machine: **218 ms** added to
startup.

`Renditions → re-detect hardware` re-runs the whole thing without a restart,
which is what you want after installing a driver or passing a GPU into a
container.

| Family | Encoders | Notes |
|---|---|---|
| Software | `libx264`, `libx265` | Always available, and the only two whose behaviour is identical on every machine. |
| NVIDIA | `h264_nvenc`, `hevc_nvenc` | Presets are `p1`–`p7`; `p4` is the honest middle. The only family that must be *told* whether it is doing CBR or capped VBR — `-rc cbr` otherwise pins it to constant bitrate and a ceiling does nothing. |
| Intel Quick Sync | `h264_qsv`, `hevc_qsv` | Needs a working VA-API/QSV runtime, not just the CPU. |
| Apple | `h264_videotoolbox`, `hevc_videotoolbox` | No preset knob; `-realtime` is the lever. |
| VA-API (Linux) | `h264_vaapi`, `hevc_vaapi` | Needs a render node, `/dev/dri/renderD128` by default, **and** a `format=nv12,hwupload` filter tail — it encodes from GPU surfaces and cannot open without both. This is the AMD path on Linux. |
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
