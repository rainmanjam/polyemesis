package ffmpeg

import (
	"fmt"
	"math"
	"strings"
)

// Image overlays on renditions.
//
// An overlay forces a video re-encode, and polyemesis's central promise is that
// video is copied and never touched. That is why overlays live on RENDITIONS,
// where re-encoding is already the contract, and not on destinations, which do
// `-c:v copy` and have no mechanism by which a copied bitstream acquires a logo.
//
// v0.5 is deliberately one still image per rendition. No text, no clock, no
// externally-fed data. That scope still exercises the whole `-vf` ->
// `-filter_complex` restructure, which is the part that has to be right before
// anything else is built on it.

// OverlayAnchor names the corner, edge or centre the image is pinned to.
type OverlayAnchor string

const (
	AnchorTopLeft      OverlayAnchor = "top-left"
	AnchorTopCenter    OverlayAnchor = "top-center"
	AnchorTopRight     OverlayAnchor = "top-right"
	AnchorMiddleLeft   OverlayAnchor = "middle-left"
	AnchorCenter       OverlayAnchor = "center"
	AnchorMiddleRight  OverlayAnchor = "middle-right"
	AnchorBottomLeft   OverlayAnchor = "bottom-left"
	AnchorBottomCenter OverlayAnchor = "bottom-center"
	AnchorBottomRight  OverlayAnchor = "bottom-right"
)

// OverlayAnchors is every anchor, in reading order. Exported so the UI and the
// validator share one list rather than two that drift.
var OverlayAnchors = []OverlayAnchor{
	AnchorTopLeft, AnchorTopCenter, AnchorTopRight,
	AnchorMiddleLeft, AnchorCenter, AnchorMiddleRight,
	AnchorBottomLeft, AnchorBottomCenter, AnchorBottomRight,
}

// OverlaySpec is one image watermark.
//
// Geometry is percentage-based and that is non-negotiable. The whole
// per-destination angle is the same overlay attached to a 1920x1080 and a
// 1080x1920 rendition; pixel geometry that looks right on the first lands
// off-canvas on the second, and nothing would report it.
type OverlaySpec struct {
	// ImagePath is an absolute path, resolved from the data directory by the
	// caller. A plain path, never a `movie=` filter argument -- see synth.go:345
	// for the rule: paths routinely contain the characters a filtergraph treats
	// as separators.
	ImagePath string
	Anchor    OverlayAnchor
	// WidthPct is the image's width as a fraction of the output width, 0-1.
	WidthPct float64
	// MarginXPct and MarginYPct are the gap from the anchored edges, as
	// fractions of the output width and height. Ignored on a centred axis.
	MarginXPct float64
	MarginYPct float64
	// Opacity is 0-1. 1 means the image's own alpha is used unchanged, and the
	// colourchannelmixer stage is omitted entirely.
	Opacity float64
}

// Active reports whether this overlay should be compiled into the graph.
func (o *OverlaySpec) Active() bool {
	return o != nil && strings.TrimSpace(o.ImagePath) != "" && o.WidthPct > 0
}

// The label names used inside -filter_complex. Fixed rather than generated
// because there is exactly one overlay in v0.5, and a fixed name is one fewer
// thing for a golden test to have to reproduce.
const (
	labelBase    = "[bs]"
	labelOverlay = "[ov]"
	labelOut     = "[vout]"
)

// overlayGraph renders the -filter_complex value for a rendition carrying an
// image overlay, or "" when there is none.
//
// out is the rendition's output size in pixels. Both dimensions must be
// positive: see db.RenditionOverlay.problems for why that is a validation rule
// rather than something handled here. It arrives as a frameSize rather than a
// height so that `overlayGraph(s, prof, s.Height, s.Width)` — which used to
// compile, and would have sized the logo off the wrong edge of a live stream
// with no error to show for it — is no longer a call this function accepts.
// See frameSize.
//
//	[0:v:0]<main chain>[bs];
//	[1:v]format=rgba,scale=230:-2[ov];
//	[bs][ov]overlay=x=..:y=..:eof_action=repeat,format=yuv420p[vout]
//
// Four details are load-bearing:
//
//   - The image is input 1, AFTER the relay, so the relay stays `0:` and
//     `-map 0:a -c:a copy` is untouched. Audio still arrives bit-identical and
//     the product's differentiator survives verbatim.
//   - No `-loop 1` and no `-shortest`. A single-frame PNG plus
//     `eof_action=repeat` holds the logo forever, and the process still ends
//     when the relay ends. `-loop 1` without `-shortest` would keep the encoder
//     alive after the ingest died.
//   - `format=yuv420p` is pinned after the overlay. Without it an RGBA logo over
//     a limited-range source shifts colour depending on which conversion
//     `overlay` happens to auto-insert.
//   - For VAAPI the overlay goes BEFORE the `format=nv12,hwupload` tail, which
//     stays last. The overlay is an ordinary software stage; see videoFilter.
func overlayGraph(s RenditionSpec, prof encoderProfile, out frameSize) string {
	o := s.Overlay
	if !o.Active() || !out.sized() {
		return ""
	}

	// includeText=false: text is drawn AFTER the composite, below, so a caption
	// sits on top of the logo rather than under it.
	main := videoFilterChain(s, encoderProfile{}, false) // the VAAPI tail is added last, below
	var b strings.Builder

	b.WriteString("[0:v:0]")
	if main == "" {
		// `null` rather than an empty chain: a filtergraph link needs a filter
		// between its labels, and an empty one is a parse error rather than a
		// pass-through.
		b.WriteString("null")
	} else {
		b.WriteString(main)
	}
	b.WriteString(labelBase + ";")

	// The image chain. format=rgba first so a logo saved without an alpha
	// channel still composites, and so colourchannelmixer has an alpha channel
	// to scale.
	//
	// The whole frame goes to overlayWidthPx, not out.W: which axis a WidthPct
	// is a fraction of is that function's business, and picking the field here
	// would put the choice back at a call site where it can be picked wrongly.
	fmt.Fprintf(&b, "[1:v]format=rgba,scale=%d:-2", overlayWidthPx(o.WidthPct, out))
	if op := clamp01(o.Opacity); op > 0 && op < 1 {
		fmt.Fprintf(&b, ",colorchannelmixer=aa=%s", trimFloat(op))
	}
	b.WriteString(labelOverlay + ";")

	// The composite.
	x, y := overlayPosition(o, out)
	fmt.Fprintf(&b, "%s%soverlay=x=%s:y=%s:eof_action=repeat", labelBase, labelOverlay, x, y)
	// Text after the composite, so it draws over the logo.
	if dt := drawtextFilter(s.Text, out); dt != "" {
		b.WriteString("," + dt)
	}
	b.WriteString(",format=yuv420p")
	if prof.vaapi {
		b.WriteString(",format=nv12,hwupload")
	}
	b.WriteString(labelOut)

	return b.String()
}

// overlayWidthPx converts the stored percentage into pixels.
//
// Computed here rather than expressed as a filter variable on purpose. The
// obvious filter-side answer, `scale2ref`, is deprecated in FFmpeg 8.1.2 and
// says so on every start; its documented replacement -- a two-input
// `scale=rw:rh` -- works, but the reference input would have to come from a
// `split` of the main chain, costing a frame copy per frame to measure a number
// this function already knows.
//
// Minimum 2, and even: an odd or zero-width overlay makes the scale filter
// refuse to open, and a watermark scaled to nothing is a stream that will not
// start for a reason the operator cannot see.
//
// It takes the whole frame and reads out.W itself. WidthPct is a fraction of
// the output WIDTH — see OverlaySpec.WidthPct — and that is a fact about this
// function, so it is stated once here rather than re-decided by whoever writes
// the next call. This line is the last place an image overlay can pick up the
// wrong axis, and it is covered by TestFilterGeometryNamesTheRightAxisAtANonSquareSize.
func overlayWidthPx(pct float64, out frameSize) int {
	px := int(math.Round(clamp01(pct) * float64(out.W)))
	if px < 2 {
		return 2
	}
	return px &^ 1
}

// overlayPosition renders the x and y expressions for the anchor.
//
// The centred axes are expressions rather than numbers because the overlay's
// scaled HEIGHT is derived by FFmpeg (`-2`), so its value is not known here.
// The edge-anchored axes use `main_w`/`main_h` for the same reason it costs
// nothing to: if the main chain's rounding lands a pixel off the requested
// size, the margin stays correct against what was actually produced.
func overlayPosition(o *OverlaySpec, out frameSize) (string, string) {
	// marginsPx rather than two marginPx calls: this function no longer names
	// either dimension, so it has no way to measure the horizontal margin down
	// the frame. See frameSize.marginsPx, which is where that pairing lives for
	// both this function and textPosition.
	m := out.marginsPx(o.MarginXPct, o.MarginYPct)

	var x, y string
	switch o.Anchor {
	case AnchorTopCenter, AnchorCenter, AnchorBottomCenter:
		x = "(main_w-overlay_w)/2"
	case AnchorTopRight, AnchorMiddleRight, AnchorBottomRight:
		x = fmt.Sprintf("main_w-overlay_w-%d", m.X)
	default:
		// Every left anchor, and an unrecognised one.
		//
		// Degrading to top-left rather than refusing is the same choice
		// aspectFilter and deinterlaceFilter already make: a rendition row
		// written by a newer build must still encode, and a stream that does not
		// start is a worse answer than a logo in the wrong corner.
		x = fmt.Sprintf("%d", m.X)
	}
	switch o.Anchor {
	case AnchorMiddleLeft, AnchorCenter, AnchorMiddleRight:
		y = "(main_h-overlay_h)/2"
	case AnchorBottomLeft, AnchorBottomCenter, AnchorBottomRight:
		y = fmt.Sprintf("main_h-overlay_h-%d", m.Y)
	default:
		y = fmt.Sprintf("%d", m.Y)
	}
	return x, y
}

// marginPx is the one genuinely axis-agnostic piece of this arithmetic: a
// percentage of a span, floored at zero because a negative margin would render
// as a filter expression FFmpeg cannot parse.
//
// It takes a bare span rather than a frameSize deliberately — it has no opinion
// about which axis it is measuring, and pretending otherwise would only move
// the question somewhere it can be answered wrongly. Deciding WHICH span goes
// with which percentage is frameSize.marginsPx's job, and that is the only
// caller in the non-test code.
func marginPx(pct float64, span int) int {
	px := int(math.Round(clamp01(pct) * float64(span)))
	if px < 0 {
		return 0
	}
	return px
}

func clamp01(v float64) float64 {
	switch {
	case v < 0 || math.IsNaN(v):
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// trimFloat formats a 0-1 factor without a trailing run of zeroes, so the
// generated filter string is stable and readable in a golden test.
func trimFloat(v float64) string {
	s := fmt.Sprintf("%.4f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
