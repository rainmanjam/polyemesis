# Overlays: text, logo, watermark, channel name

> **Status: image watermarks and text SHIPPED.** A still per rendition and a
> line of burnt-in text, both with percentage geometry, nine anchors and
> opacity. Operator page: [../RENDITIONS.md](../RENDITIONS.md#watermarks).
>
> Text arrived after this note first said it had not, along with the two things
> the design correctly identified as its prerequisites: **the embedded font**
> (two weights of Inter, because `drawtext` takes a font *path* and a container
> image has neither fontconfig nor a font file) and **the `drawtext` filter
> probe** (the filter is optional in FFmpeg; a build without it drops the text
> and keeps the picture up rather than failing the rendition).
>
> **Not built, and still designed below:** the clock, externally-fed data,
> multiple overlays per rendition, and the one-frame preview endpoint.
>
> **Three things building it changed.**
>
> 1. **`scale2ref` is deprecated in FFmpeg 8.1.2** and warns on every start. Its
>    documented replacement -- a two-input `scale=rw:rh` -- works, but the
>    reference input has to come from a `split` of the main chain, costing a
>    frame copy per frame. So the image width is computed in Go instead, which
>    means **a watermarked rendition needs an explicit width and height**. That
>    is a real restriction the design did not anticipate, and it is enforced at
>    validation time rather than discovered as a stream with no logo.
> 2. **Columns, not tables.** The design argues for `overlays` +
>    `rendition_overlays` because the full feature has several overlays per
>    rendition and reuses rows across renditions. v0.5 has neither, and a join
>    table for a strictly 1:1 relationship is structure with nothing in it.
>    Growing to the table later is a six-column data migration.
> 3. **`r.Deinterlace` was missing from `renditionSig`** -- found while adding
>    the overlay to it. Changing a rendition's deinterlace mode was stored,
>    shown in the UI, and never reached the running encoder. Dates from the
>    item-0 work; fixed here.
>
> The hardware-encoder finding held exactly as written: the overlay is an
> ordinary software stage appended before VAAPI's one-way `format=nv12,hwupload`
> tail, and nothing changed for the other four encoder families.

**Status of the original design below: superseded by the note above.** It was
written on 2026-07-28 and deferred; watermarks shipped that week and text
followed. The body is kept because its reasoning about the filter chain, the
geometry model and the hardware-encoder interaction is what the implementation
was built from, and two of its three predictions held exactly.

**Evidence:** the most-repeated unmet request on the competitor's tracker —
asked **five separate times** (6+5+4+2+1 reactions across distinct issues), every
one closed unimplemented.
**Effort:** ~16 days full, or a credible **6-day v0.5**.

---

## The constraint that shapes everything

An overlay forces a video re-encode. polyemesis's central promise is that video
is copied, never touched.

So overlays belong on **renditions**, where re-encoding is already the contract —
not on destinations, which do `-c:v copy` and have no mechanism by which a copied
bitstream acquires a logo.

The novel angle nobody else ships is **per-destination branding**: a different
sponsor card for Twitch than for YouTube, a clean feed with no branding for the
archive, a vertical-safe lower-third that only applies to the 9:16 rendition.
Overlays elsewhere are a property of *the stream*; here they can be a property of
*the destination*.

## What already exists

**The filter chain is one function.** `videoFilter`
([internal/ffmpeg/rendition.go:351](../../internal/ffmpeg/rendition.go)) comma-joins
a `[]string` and hands it to `-vf`. Stages are already deliberately ordered:
deinterlace → aspect → VAAPI tail.

**A labelled multi-chain graph is already proven.** `blurredPadFilter`
(rendition.go:462) emits `split=2[bgsrc][fgsrc];…;[bg][fg]overlay=…` inside
`-vf`, so `-vf` accepting link labels is established. What has never been done is
adding a **second input**.

**Image handling has a precedent, and a rule.** `SlateSettings.ImagePath` is
stored relative to `DataDir`, validated by `SlateImageProblem()` (length cap,
control characters, backslash normalisation, `..`, leading `/`, drive letters),
and joined to an absolute path only at process-build time. And `synth.go:345`
states the rule an overlay must obey:

> A plain path, never a `movie=` filter argument: paths routinely contain the
> characters a filtergraph would treat as separators.

That one comment forecloses the `movie=` design. **Image overlays must be real
`-i` inputs.**

**The acknowledgement vocabulary already exists.** `Destination.ExpertAckReencode`
*"records the operator agreeing, in as many words, that an argument here
overrides something the product otherwise guarantees"*, and `checkExpertArgs`
builds a promise string and forces acknowledgement. Overlays should borrow this
wholesale rather than invent a new warning idiom.

## The hardware-encoder finding

This is the most useful thing the research turned up, because it removes a
problem everyone assumes is there.

`prof.vaapi` is true for **exactly one encoder** (rendition.go:188). There is no
`-hwaccel` on the input anywhere in `RenditionArgs`, so NVENC, QSV, VideoToolbox
and AMF all decode and filter in **system memory** and upload internally.

**The `hwupload`/`hwdownload` sandwich problem does not exist in this codebase
except for VAAPI** — and for VAAPI it is not a sandwich, it is a one-way tail
(`format=nv12,hwupload`). An overlay is simply another software stage appended
*before* that tail. No download, no round trip, and no change at all for the
other four encoder families.

## Design

### Data model — tables, not columns

Columns on `renditions` cannot express "logo **and** channel name", and an
overlay must be reusable across renditions (the same card on 16:9 and 9:16):

```
overlays(id, source_id, name, kind, text, image_path, font_file, font_size_pct,
         font_color, box, box_color, opacity, anchor, margin_x_pct,
         margin_y_pct, width_pct, source_url, refresh_seconds, …)
rendition_overlays(rendition_id, overlay_id, z, PRIMARY KEY(rendition_id, overlay_id))
```

**Geometry is percentage-based, and this is non-negotiable.** The entire
per-destination angle is the same overlay attached to a 1920×1080 and a 1080×1920
rendition; pixel geometry lands off-canvas on the second. Percentages compile to
expressions against `main_w`/`main_h`, so one row is correct at every size.

A **clean feed is the absence of a `rendition_overlays` row** — deliberately
mirroring "passthrough is the absence of a `rendition_id`".

### Filter graph

With ≥1 image overlay, `RenditionArgs` switches from `-vf` to `-filter_complex`:

```
-vaapi_device …                              # unchanged, still before every -i
-i udp://…                                   # input 0, the relay — unchanged
-i /data/overlays/logo.png                   # input 1
-filter_complex "[0:v:0]bwdif=…,scale=…[bs];
                 [1:v]format=rgba,scale=iw*0.12:-2[o1];
                 [bs][o1]overlay=x=…:y=…:eof_action=repeat[v1];
                 [v1]drawtext=textfile=…[v2];
                 [v2]format=yuv420p[vout]"
-map "[vout]" -map 0:a -c:a copy
```

Four load-bearing details:

- **Image inputs go after the relay input**, so the relay stays `0:` and
  `-map 0:a -c:a copy` is untouched. Audio still arrives bit-identical; the
  invariant survives verbatim.
- **No `-loop 1`, no `-shortest`.** A single-frame PNG plus
  `overlay=…:eof_action=repeat` holds the logo forever, and the process still
  ends when the relay ends. `-loop 1` without `-shortest` would keep the encoder
  alive after the ingest died.
- **An empty overlay set must produce byte-identical argv to today.** `-vf` stays
  `-vf`. The 1,117-line `rendition_test.go` is the regression net.
- `format=yuv420p` is pinned after the last overlay, or an RGBA logo over a
  limited-range source shifts colour depending on which conversion `overlay`
  auto-inserts.

### Text: use `textfile=`, never `text=`

| kind | mechanism | cost | changes without restart? |
|---|---|---|---|
| image | second `-i`, `overlay` | ~1–2% CPU | no |
| static text | `drawtext=textfile=…:expansion=none` | ~2–5% at 1080p | no |
| clock | `drawtext` with `%{localtime…}` | same | self-updating |
| feed (viewer count) | `textfile=…:reload=1` + a Go writer | same + a file read per frame | **yes** |

`drawtext`'s `text=` option is the worst escaping surface in FFmpeg — and the
existing `lavfiEscaper` does **not** escape `%`, which `drawtext` expands. A
channel name containing an apostrophe becomes a stream that will not start.
`textfile=` eliminates the entire class.

Feed text is rewritten by a goroutine using **write-temp-then-`os.Rename` in the
same directory**. A half-written file read by `reload=1` renders garbage on air.

**The rule that protects the restart model:** the overlay's *shape* (id, kind,
anchor, percentages, font, image path plus the image file's size and mtime,
static text) goes into `renditionSig`. The feed file's *contents* do not.
Otherwise every viewer-count tick restarts the encode and every destination on
it.

### `drawtext` may not exist in the build

It requires `--enable-libfreetype`. `detect.go` currently parses `-encoders` only
— nothing knows whether this FFmpeg has `drawtext`. It must gain `-filters`
parsing, and a text overlay on a build without it must fail **at validation time
with a clear message**, not at process start.

Fonts: embed one open font (~200 KB) via `go:embed` and materialise it into
`<DataDir>/overlays/` on first run. Minimal containers have neither fonts nor
fontconfig, and "channel name" has to work out of the box.

## The per-destination angle, priced honestly

**It costs exactly one encode per distinct branding.**

- Twitch with a sponsor card + YouTube with a different card + a clean archive =
  **two encodes and one passthrough**, not one encode and three overlays.
- Adding the *first* overlay to an existing rendition costs **no new process** —
  a few percent CPU on an encode that was already running. Present that case as
  cheap, because it is.
- The expensive case is *differentiating*. Cloning a 1080p60 rendition so two
  platforms can carry different cards costs a second full encode: roughly 1.5–3
  cores on x264 `veryfast`, or ~zero CPU on NVENC — but one of the **3–8
  concurrent NVENC sessions** a consumer card allows. That ceiling is a hard
  FFmpeg start failure, and the existing encoder probe will not catch it because
  it probes once, not per session.

**Keeping it visible.** Reuse the product's existing vocabulary:

1. `DestinationCard` renders `"passthrough · copy"` today. An overlaid
   destination renders `"1080p60 · overlaid"` with a distinct chip — the
   copy/encode distinction stays the first thing on the card.
2. Attaching an overlay to a rendition shared by K destinations says so, with
   the list, **before** saving. Shared branding must never be a surprise.
3. "Give this destination its own branding" is an explicit **Branch this
   rendition** button, never a side effect of editing an overlay. The confirm
   prices it with the Mpix/s estimator `RenditionsPage` already computes.
4. That confirm carries an acknowledgement modelled on `ExpertAckReencode` —
   stored, not one-shot, for the same reason the expert flag is.

## Test plan

The template already exists: `TestAspectFiltersPlaceThePictureCorrectly`
(`rendition_test.go:900`) renders one frame to `-f rawvideo -pix_fmt rgb24
pipe:1` and counts pixels.

1. **Position as a bounding box.** Black source, solid-magenta PNG. Dump one
   frame, compute min/max x and y of magenta, assert the rect for each of 9
   anchors × 2 canvas shapes × 2 margins within ±1 px. Catches a swapped
   `W`/`main_w`, a margin on the wrong axis, or rounding drift — none of which a
   string comparison catches.
2. **Scale invariance.** The same overlay row at 1920×1080 and 1080×1920: assert
   measured width / canvas width equals `width_pct` in both. **This is the single
   test that proves the per-destination story works.**
3. **Text without OCR.** `drawtext` with `box=1:boxcolor=white` on black: measure
   the white bbox and assert its height scales linearly with `font_size_pct`
   across three sizes. A zero-area result catches a missing font.
4. **Reload without restart.** Run 3 s with `reload=1`, atomically replace the
   file mid-run with text of a different width, sample frame ~10 and ~80, assert
   the bbox changed and the process never restarted.
5. **framemd5** on the *raw* chain (not through H.264, which is not
   bit-reproducible): the same chain twice is identical; overlay-present differs
   from overlay-absent; two renditions sharing an overlay produce identical
   overlay regions. This makes "the clean feed is actually clean" a measurement.
6. **The passthrough guarantee as a golden test.** `RenditionArgs` with no
   overlays must be byte-identical to today, and every destination kind must
   still emit `-c:v copy` and never a video `-filter_complex`.
7. **VAAPI ordering:** `hwupload` last, every overlay before it, `-vaapi_device`
   before every `-i`, and `-i` count equals 1 + images.
8. **Path confinement**, mirroring the slate's table test.
9. **Hostile text** containing `: , ' \ % [ ] ; "` — build it, run it, assert
   exit 0 and a non-empty glyph bbox. With `textfile=` this passes trivially,
   which is the point of choosing it.

## Risks

1. **`-vf` → `-filter_complex` is surgery on the most safety-critical arg
   builder**, guarded by ~40 KB of golden expectations. Mitigated only by the
   byte-identical no-overlay test.
2. **Stream renumbering.** Adding `-i` makes the logo input 1. `api/expert.go`
   refuses a second `-i` on *destinations* precisely because it renumbers
   `[0:a:N]`. Renditions have no routing graph so it is safe — **write that down
   in the code**, or someone will later "fix" it wrongly.
3. **`drawtext` absent from the build.** A rendition that started yesterday must
   not stop starting today.
4. **`reload=1` reads the file every frame** — 60 opens/second inside the encode
   loop. Document a 1 Hz refresh ceiling.
5. **Editing an overlay restarts the encode**, and therefore every destination on
   that tier. Nudging a logo 10 px while live drops them for a second or two.
   Only feed *content* is hot; geometry is not. The UI must say so.
6. **NVENC session limits** turn per-destination branding into a hard start
   failure on consumer GPUs, invisible to the one-shot probe.
7. **Scope creep toward a graphics engine.** The v1 line: static image, static
   text, clock, one externally-fed text file. No animation, no browser sources,
   no alert graphics.

## Effort

| | days |
|---|---|
| `overlay.go` + `rendition.go` restructure + string tests | 3 |
| `detect.go` filter probe + embedded font | 1 |
| DB model, migrations, validation, path confinement | 2 |
| Engine wiring, signature, feed-text writer, lifecycle | 2 |
| API + one-frame preview endpoint | 1.5 |
| UI: editor, preview, cost/ack framing | 3.5 |
| Measurement tests | 2 |
| Docs | 1 |
| **Total** | **≈16** |

**The 6-day v0.5 worth considering:** image watermark only, one overlay per
rendition, percentage geometry, no text, no dynamic data, no preview. It closes
the most-requested item and it exercises the whole `-filter_complex`
restructure — which is the part that must be right before anything else is built
on it.

---

## See also

- [ROADMAP](README.md)
- [../RENDITIONS.md](../RENDITIONS.md) — where overlays attach
- [UNREACHABLE-FEATURES.md](UNREACHABLE-FEATURES.md) — the UI drift this work
  would otherwise extend
