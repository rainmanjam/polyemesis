package ffmpeg

import (
	"strings"
	"testing"
)

// Every filter this package emits, asserted down to its actual pixel numbers,
// at a size where the width and the height are different.
//
// This is the test that was missing. Nothing in the shipped encoder was ever
// transposed -- the defect was that neither of these was rejected by anything:
//
//	overlayGraph(s, prof, s.Height, s.Width)
//	drawtextFilter(s.Text, s.Height, s.Width)
//
// Both compiled, because the size travelled as two bare ints. Both would have
// passed `go test ./internal/ffmpeg/`, because the string tests around them ran
// at 1280x720 and 640x200 and then asserted on substrings that name no pixel
// count at all -- `expansion=none`, `drawtext=`, the ORDER of two stages. A
// substring assertion is blind to which number went into which axis, and a
// SQUARE test case is blind to it even when the numbers are asserted, since
// every value is its own transpose. What a transposition produces is a logo
// scaled off the wrong edge and a caption placed past the bottom of the frame,
// with no error anywhere for the operator to search for.
//
// frameSize makes the swapped call a compile error, which is the real device,
// and it now reaches further than the outermost signatures: frameSize.wh()
// emits every width:height pair, frameSize.marginsPx() is the only place a
// margin percentage meets a dimension, and overlayWidthPx and textSizePx each
// take the whole frame and pick their own axis. This test is the second half.
// It covers what the compiler still cannot: the handful of lines that read
// out.W and out.H by name, where both are plain ints once you hold the struct.
// Those lines are frameSize.wh, frameSize.marginsPx, RenditionSpec.outputSize,
// blurProxySize, overlayWidthPx, textSizePx, and the two asymmetric min()
// expressions in cropFitFilter. Every one of them is exercised below, and
// swapping the two fields in any one of them fails a case.
//
// frameSize.sized is the one field-reading line the cases below do NOT pin:
// `f.W > 0 && f.H > 0` written as `f.W > 0 && f.W > 0` is not a transposition
// that produces a wrong picture at any size these tests use, only a guard that
// stops rejecting a height-less spec. TestRenditionArgsAspectNeedsBothDimensions
// covers that instead.

// The vertical rendition dual-format exists for. Chosen so that no number
// asserted below equals its own transpose -- see the control test at the
// bottom, which enforces exactly that property rather than trusting it.
var (
	portrait  = frameSize{W: 1080, H: 1920}
	landscape = frameSize{W: 1920, H: 1080}
)

// geometryRendition is a rendition stripped to its geometry: no frame-rate
// filter, no deinterlace, nothing in the chain that is not a pixel count. The
// percentages are the ones whose products differ per axis at 1080x1920 --
// 20% of the width is 216 and of the height 384, a 5% margin is 54 across and
// 96 down, and 5% type is 96 tall and would be 54 if it were measured across.
func geometryRendition(out frameSize) RenditionSpec {
	return RenditionSpec{
		InRelayURL:  "udp://127.0.0.1:20000",
		OutRelayURL: "udp://127.0.0.1:20010",
		Width:       out.W,
		Height:      out.H,
		VideoKbps:   6000,
	}
}

func withOverlay(s RenditionSpec) RenditionSpec {
	s.Overlay = &OverlaySpec{
		ImagePath: "/data/logo.png", Anchor: AnchorBottomRight,
		WidthPct: 0.2, MarginXPct: 0.05, MarginYPct: 0.05, Opacity: 1,
	}
	return s
}

func withText(s RenditionSpec) RenditionSpec {
	s.Text = &TextSpec{
		Text: "POLYEMESIS", FontFile: "/data/fonts/Inter-Regular.ttf",
		Anchor: AnchorBottomLeft, SizePct: 0.05,
		Color: "white", MarginXPct: 0.05, MarginYPct: 0.05,
	}
	return s
}

// geometryCase is one emitted filter string and the pixel numbers that have to
// be in it. flag is the argv flag the string is read from, because an overlay
// is the one thing that moves this builder from -vf to -filter_complex.
type geometryCase struct {
	name  string
	flag  string
	spec  func(RenditionSpec) RenditionSpec
	wants []string
}

func geometryCases() []geometryCase {
	return []geometryCase{{
		name: "a plain scale states both sides in order",
		flag: "-vf",
		spec: func(s RenditionSpec) RenditionSpec { return s },
		// scale's arguments are width:height. Transposed this reads
		// scale=1920:1080, which FFmpeg accepts happily and which produces a
		// landscape file where a platform was promised a vertical one.
		wants: []string{"scale=1080:1920"},
	}, {
		name: "a centre crop derives both sides from the target ratio",
		flag: "-vf",
		spec: func(s RenditionSpec) RenditionSpec { s.Aspect = AspectCrop; return s },
		// The two min() expressions are NOT symmetric: the width expression
		// carries w/h and the height expression h/w. Swapping them inverts the
		// target ratio, so a 16:9 source cropped for a 9:16 rendition keeps the
		// wrong axis and the subject leaves the frame.
		wants: []string{
			`crop=2*floor(min(iw\,ih*1080/1920)/2):2*floor(min(ih\,iw*1920/1080)/2)`,
			"scale=1080:1920,setsar=1",
		},
	}, {
		name: "a letterbox fits inside and pads out to the same numbers",
		flag: "-vf",
		spec: func(s RenditionSpec) RenditionSpec { s.Aspect = AspectPad; return s },
		wants: []string{
			"scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2",
			"pad=1080:1920:",
		},
	}, {
		name: "the blurred background is built at a per-axis proxy size",
		flag: "-vf",
		spec: func(s RenditionSpec) RenditionSpec { s.Aspect = AspectBlurredPad; return s },
		// An eighth of each side, floored at 32 and rounded even: 1080 -> 134
		// and 1920 -> 240. Transposed the background is built 240x134 and then
		// stretched across a 1080x1920 canvas, which is the "lazy" look the
		// whole mode exists to avoid.
		wants: []string{
			"scale=134:240:force_original_aspect_ratio=increase:force_divisible_by=2",
			"crop=134:240,gblur=sigma=4,scale=1080:1920,setsar=1[bg]",
			"[fgsrc]scale=1080:1920:force_original_aspect_ratio=decrease:force_divisible_by=2[fg]",
		},
	}, {
		name: "text is sized off the height and margined off both",
		flag: "-vf",
		spec: withText,
		// fontsize is a fraction of the HEIGHT (5% of 1920 = 96) while the x
		// margin is a fraction of the WIDTH (5% of 1080 = 54). Transposed, the
		// caption is 54 px tall on a 1920-tall canvas -- legible enough to look
		// deliberate and much too small to read on a phone.
		wants: []string{":fontsize=96:fontcolor=white:x=54:y=h-text_h-96:"},
	}, {
		name: "an image overlay is sized off the width and anchored off both",
		flag: "-filter_complex",
		spec: func(s RenditionSpec) RenditionSpec { return withText(withOverlay(s)) },
		// WidthPct is a fraction of the WIDTH (20% of 1080 = 216); the bottom
		// margin is a fraction of the HEIGHT (96) and the right margin of the
		// width (54). All three change under a transposition and none of them
		// produces an error.
		wants: []string{
			"[1:v]format=rgba,scale=216:-2[ov];",
			"overlay=x=main_w-overlay_w-54:y=main_h-overlay_h-96:eof_action=repeat",
			":fontsize=96:fontcolor=white:x=54:y=h-text_h-96:",
		},
	}}
}

// emittedFilter is the filter string RenditionArgs actually puts on the command
// line for this spec. Read from the argv rather than by calling the builders
// directly, so the assertions cover the call sites in RenditionArgs and
// videoFilterChain -- which is where both transpositions lived.
func emittedFilter(t *testing.T, c geometryCase, out frameSize) string {
	t.Helper()
	v, ok := argsAfter(RenditionArgs(c.spec(geometryRendition(out))), c.flag)
	if !ok {
		t.Fatalf("%s: no %s in the argv at %dx%d", c.name, c.flag, out.W, out.H)
	}
	return v
}

func TestFilterGeometryNamesTheRightAxisAtANonSquareSize(t *testing.T) {
	for _, c := range geometryCases() {
		t.Run(c.name, func(t *testing.T) {
			got := emittedFilter(t, c, portrait)
			for _, want := range c.wants {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q\nin %s %s\n\nat %dx%d. A width where a height belongs "+
						"is not an error FFmpeg reports -- it is a rendition that encodes, "+
						"streams, and looks wrong on air.",
						want, c.flag, got, portrait.W, portrait.H)
				}
			}
		})
	}
}

// The positive control for the test above.
//
// Every assertion up there is a substring match, and a substring match is
// exactly the kind of assertion that goes quietly hollow: weaken a `want` to
// "drawtext=" or "scale=" and the test still passes on a build that has the
// axes swapped end to end. So this runs the SAME cases against a spec whose
// width and height are exchanged and requires every asserted fragment to be
// ABSENT.
//
// That is the property the assertions need and the reason a square test size
// would have been worthless: at 1280x1280 every fragment above survives its own
// transposition, so the suite would pass over a completely transposed encoder.
// If this test fails, the assertion it names does not actually pin an axis, and
// the fix is the assertion rather than this control.
func TestTheGeometryAssertionsWouldFailOnATransposedBuild(t *testing.T) {
	if portrait.W == portrait.H {
		t.Fatal("the geometry tests run at a square size, where no assertion can see a transposition")
	}
	for _, c := range geometryCases() {
		t.Run(c.name, func(t *testing.T) {
			got := emittedFilter(t, c, landscape)
			for _, want := range c.wants {
				if strings.Contains(got, want) {
					t.Errorf("%q also appears at %dx%d\nin %s %s\n\nso it pins no axis and would "+
						"pass over a transposed width and height",
						want, landscape.W, landscape.H, c.flag, got)
				}
			}
		})
	}
}
