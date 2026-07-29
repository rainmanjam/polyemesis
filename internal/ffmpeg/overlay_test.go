package ffmpeg

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The single most important test in this file.
//
// The overlay work rewrites RenditionArgs -- the most safety-critical argument
// builder in the project, guarded by ~40 KB of golden expectations. A rendition
// with no overlay must produce exactly the argv it produced before, character
// for character. If it does not, every existing encode changes underneath an
// operator who never asked for a watermark.
//
// The rest of rendition_test.go is the detailed net; this is the blunt one.
func TestRenditionArgsWithoutAnOverlayAreUnchangedByTheOverlayWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*RenditionSpec)
	}{
		{"plain", func(*RenditionSpec) {}},
		{"nil overlay", func(s *RenditionSpec) { s.Overlay = nil }},
		{"an overlay with no image", func(s *RenditionSpec) {
			s.Overlay = &OverlaySpec{WidthPct: 0.2, Anchor: AnchorTopRight}
		}},
		{"an overlay scaled to nothing", func(s *RenditionSpec) {
			s.Overlay = &OverlaySpec{ImagePath: "/data/overlays/logo.png", WidthPct: 0}
		}},
		{"vaapi", func(s *RenditionSpec) { s.Encoder = EncoderVAAPI }},
		{"aspect + deinterlace", func(s *RenditionSpec) {
			s.Aspect, s.Deinterlace = AspectBlurredPad, DeinterlaceAuto
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := baseRendition()
			tc.mut(&s)
			args := RenditionArgs(s)
			line := join(args)

			if strings.Contains(line, "-filter_complex") {
				t.Errorf("a rendition with no active overlay used -filter_complex: %s", line)
			}
			if !strings.Contains(line, "-map 0:v:0") {
				t.Errorf("the video map is no longer `0:v:0`: %s", line)
			}
			// The line the whole product rests on. It is asserted in
			// rendition_test.go too; repeated here because this file is the one
			// that added a second input, which is exactly what could break it.
			//
			// Two separate substrings, not one: `-map 0:a` and `-c:a copy` are
			// not adjacent in the argv -- the codec and rate flags sit between
			// them -- and asserting them as one string was this test's own bug
			// before it was anything else's.
			if !strings.Contains(line, "-map 0:a ") {
				t.Errorf("`-map 0:a` is gone, so only one audio track would survive: %s", line)
			}
			if !strings.Contains(line, "-c:a copy") {
				t.Errorf("`-c:a copy` is gone, so audio is being re-encoded: %s", line)
			}
			if n := strings.Count(line, " -i "); n != 1 {
				t.Errorf("%d inputs, want 1: %s", n, line)
			}
		})
	}
}

// With an overlay the audio contract must survive verbatim. This is the reason
// the image input goes AFTER the relay rather than before it.
func TestAnOverlayDoesNotDisturbTheAudioMap(t *testing.T) {
	s := baseRendition()
	s.Width, s.Height = 1920, 1080
	s.Overlay = &OverlaySpec{
		ImagePath: "/data/overlays/logo.png", Anchor: AnchorTopRight, WidthPct: 0.12,
	}
	line := join(RenditionArgs(s))

	if !strings.Contains(line, "-map 0:a ") || !strings.Contains(line, "-c:a copy") {
		t.Fatalf("the audio contract did not survive the overlay: %s", line)
	}
	if !strings.Contains(line, "-map [vout]") {
		t.Errorf("the video map does not name the filtergraph output: %s", line)
	}
	if strings.Contains(line, "-vf ") {
		t.Errorf("-vf and -filter_complex are mutually exclusive, and both are present: %s", line)
	}
	// The relay must still be input 0, or `0:a` above means something else.
	relayAt := strings.Index(line, RelayInputURL(s.InRelayURL))
	imageAt := strings.Index(line, "/data/overlays/logo.png")
	if relayAt < 0 || imageAt < 0 || relayAt > imageAt {
		t.Errorf("the image input is not after the relay, so the relay is no longer input 0: %s", line)
	}
	if strings.Contains(line, "-loop 1") || strings.Contains(line, "-shortest") {
		t.Errorf("`-loop 1` without `-shortest` keeps the encoder alive after the ingest dies; "+
			"eof_action=repeat is what holds the logo: %s", line)
	}
	if !strings.Contains(line, "eof_action=repeat") {
		t.Errorf("no eof_action=repeat, so the logo vanishes after one frame: %s", line)
	}
}

// VAAPI is the one encoder that filters on the GPU. The overlay is an ordinary
// software stage, so it must be composited BEFORE the upload -- if hwupload came
// first the overlay would be handed a hardware surface it cannot touch.
func TestVAAPIUploadsAfterTheOverlay(t *testing.T) {
	s := baseRendition()
	s.Width, s.Height = 1920, 1080
	s.Encoder = EncoderVAAPI
	s.Overlay = &OverlaySpec{
		ImagePath: "/data/overlays/logo.png", Anchor: AnchorBottomRight, WidthPct: 0.12,
	}
	line := join(RenditionArgs(s))

	up := strings.Index(line, "hwupload")
	ov := strings.Index(line, "overlay=")
	if up < 0 || ov < 0 {
		t.Fatalf("expected both an overlay and an hwupload: %s", line)
	}
	if ov > up {
		t.Errorf("hwupload comes before the overlay, which would hand a GPU surface to a software filter: %s", line)
	}
	// -vaapi_device must still precede every -i.
	dev := strings.Index(line, "-vaapi_device")
	firstIn := strings.Index(line, " -i ")
	if dev < 0 || dev > firstIn {
		t.Errorf("-vaapi_device no longer precedes the inputs: %s", line)
	}
	if n := strings.Count(line, " -i "); n != 2 {
		t.Errorf("%d inputs, want 2 (relay + image): %s", n, line)
	}
}

func TestOverlayAnchorsCompileToTheRightExpressions(t *testing.T) {
	// 1000x500 so a 10% margin is 100 across and 50 down, and a transposed
	// axis is unmistakable rather than plausible.
	const w, h = 1000, 500
	o := func(a OverlayAnchor) *OverlaySpec {
		return &OverlaySpec{
			ImagePath: "/x.png", Anchor: a, WidthPct: 0.2,
			MarginXPct: 0.1, MarginYPct: 0.1,
		}
	}
	cases := []struct {
		anchor OverlayAnchor
		x, y   string
	}{
		{AnchorTopLeft, "100", "50"},
		{AnchorTopCenter, "(main_w-overlay_w)/2", "50"},
		{AnchorTopRight, "main_w-overlay_w-100", "50"},
		{AnchorMiddleLeft, "100", "(main_h-overlay_h)/2"},
		{AnchorCenter, "(main_w-overlay_w)/2", "(main_h-overlay_h)/2"},
		{AnchorMiddleRight, "main_w-overlay_w-100", "(main_h-overlay_h)/2"},
		{AnchorBottomLeft, "100", "main_h-overlay_h-50"},
		{AnchorBottomCenter, "(main_w-overlay_w)/2", "main_h-overlay_h-50"},
		{AnchorBottomRight, "main_w-overlay_w-100", "main_h-overlay_h-50"},
	}
	for _, c := range cases {
		t.Run(string(c.anchor), func(t *testing.T) {
			x, y := overlayPosition(o(c.anchor), w, h)
			if x != c.x || y != c.y {
				t.Errorf("position = (%s, %s), want (%s, %s)", x, y, c.x, c.y)
			}
		})
	}
	if len(cases) != len(OverlayAnchors) {
		t.Errorf("%d anchors are tested but %d exist; a new anchor has no coverage",
			len(cases), len(OverlayAnchors))
	}
}

// An unrecognised anchor degrades to top-left rather than refusing, for the
// same reason aspectFilter and deinterlaceFilter degrade: a rendition row
// written by a newer build must still encode.
func TestAnUnknownAnchorDegradesRatherThanBreaking(t *testing.T) {
	o := &OverlaySpec{ImagePath: "/x.png", Anchor: "diagonal", WidthPct: 0.2, MarginXPct: 0.1, MarginYPct: 0.1}
	x, y := overlayPosition(o, 1000, 500)
	if x != "100" || y != "50" {
		t.Errorf("an unknown anchor produced (%s, %s), want the top-left margins", x, y)
	}
}

func TestOverlayWidthIsEvenAndNeverZero(t *testing.T) {
	// An odd or zero-width scale makes the filter refuse to open, which reaches
	// the operator as a stream that will not start for no visible reason.
	for _, tc := range []struct {
		pct  float64
		outW int
		want int
	}{
		{0.12, 1920, 230},   // 230.4 -> 230, already even
		{0.1201, 1920, 230}, // 230.6 -> 231 -> 230
		{0.0001, 1920, 2},   // rounds to 0, floored to 2
		{0, 1920, 2},
		{-1, 1920, 2},
		{2, 1920, 1920}, // clamped to 100%
		{1, 1921, 1920}, // odd canvas, even result
	} {
		if got := overlayWidthPx(tc.pct, tc.outW); got != tc.want {
			t.Errorf("overlayWidthPx(%v, %d) = %d, want %d", tc.pct, tc.outW, got, tc.want)
		}
		if got := overlayWidthPx(tc.pct, tc.outW); got%2 != 0 || got < 2 {
			t.Errorf("overlayWidthPx(%v, %d) = %d, which is odd or under 2", tc.pct, tc.outW, got)
		}
	}
}

// magentaPNG writes a solid magenta rectangle to a temp file and returns its
// path. Magenta because nothing in a testsrc2 or a flat colour source produces
// it, so every magenta pixel in the output came from the overlay.
func magentaPNG(t *testing.T, ffmpeg string, w, h int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logo.png")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-nostdin",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=magenta:s=%dx%d:d=1", w, h),
		"-frames:v", "1", "-y", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("building the test logo: %v\n%s", err, stderr.String())
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("the test logo is missing or empty")
	}
	return path
}

// bbox is the magenta bounding box in an rgb24 frame, and the pixel count.
type bbox struct{ minX, minY, maxX, maxY, n int }

func magentaBBox(frame []byte, w, h int) bbox {
	b := bbox{minX: w, minY: h, maxX: -1, maxY: -1}
	for y := range h {
		for x := range w {
			i := (y*w + x) * 3
			r, g, bl := frame[i], frame[i+1], frame[i+2]
			// Generous: the scaler rings at the logo's edge, so this counts
			// unmistakable pixels rather than exact ones.
			if r > 150 && bl > 150 && g < 100 {
				b.n++
				b.minX, b.minY = min(b.minX, x), min(b.minY, y)
				b.maxX, b.maxY = max(b.maxX, x), max(b.maxY, y)
			}
		}
	}
	return b
}

// renderOverlay runs a real encode-free render of one frame and returns it.
func renderOverlay(t *testing.T, ffmpeg string, s RenditionSpec) []byte {
	t.Helper()
	graph := overlayGraph(s, encoderProfile{}, s.Width, s.Height)
	if graph == "" {
		t.Fatal("overlayGraph produced nothing")
	}
	// The graph ends at [vout] with format=yuv420p pinned; rawvideo rgb24 out
	// of it is what the measurement reads.
	cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-nostdin",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=black:s=%dx%d:r=30:d=0.2", s.Width, s.Height),
		"-i", s.Overlay.ImagePath,
		"-filter_complex", graph, "-map", labelOut,
		"-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s\n-filter_complex %s", err, stderr.String(), graph)
	}
	frame := stdout.Bytes()
	if want := s.Width * s.Height * 3; len(frame) != want {
		t.Fatalf("frame is %d bytes, want %d (%dx%d rgb24)", len(frame), want, s.Width, s.Height)
	}
	return frame
}

// Position measured as a bounding box, not asserted as a string.
//
// A string comparison passes just as happily with W and main_w swapped, a
// margin applied to the wrong axis, or rounding drift. Only rendering a frame
// and finding the logo catches those.
func TestOverlayLandsWhereTheAnchorSaysAcrossCanvasShapes(t *testing.T) {
	ffmpeg := needFFmpeg(t, "ffmpeg")[0]
	logo := magentaPNG(t, ffmpeg, 100, 50)

	const tol = 2 // px; the scaler rings at the edge

	for _, canvas := range []struct{ w, h int }{{960, 540}, {540, 960}} {
		for _, anchor := range OverlayAnchors {
			name := fmt.Sprintf("%s at %dx%d", anchor, canvas.w, canvas.h)
			t.Run(name, func(t *testing.T) {
				s := baseRendition()
				s.Width, s.Height = canvas.w, canvas.h
				s.Aspect, s.Deinterlace = "", ""
				s.Overlay = &OverlaySpec{
					ImagePath: logo, Anchor: anchor,
					WidthPct: 0.2, MarginXPct: 0.05, MarginYPct: 0.05,
				}
				b := magentaBBox(renderOverlay(t, ffmpeg, s), canvas.w, canvas.h)
				if b.n == 0 {
					t.Fatal("no magenta in the frame: the overlay was not composited at all")
				}

				wantW := overlayWidthPx(0.2, canvas.w)
				marginX := int(0.05*float64(canvas.w) + 0.5)
				marginY := int(0.05*float64(canvas.h) + 0.5)
				gotW := b.maxX - b.minX + 1

				if abs(gotW-wantW) > tol {
					t.Errorf("logo is %d px wide, want %d", gotW, wantW)
				}

				// Horizontal placement.
				switch anchor {
				case AnchorTopLeft, AnchorMiddleLeft, AnchorBottomLeft:
					if abs(b.minX-marginX) > tol {
						t.Errorf("left edge at %d, want %d (margin)", b.minX, marginX)
					}
				case AnchorTopRight, AnchorMiddleRight, AnchorBottomRight:
					if want := canvas.w - marginX - 1; abs(b.maxX-want) > tol {
						t.Errorf("right edge at %d, want %d", b.maxX, want)
					}
				default:
					mid := (b.minX + b.maxX) / 2
					if abs(mid-canvas.w/2) > tol {
						t.Errorf("centre at %d, want %d", mid, canvas.w/2)
					}
				}

				// Vertical placement. A margin applied to the wrong axis shows
				// up here and nowhere else, which is why both canvas shapes are
				// run: at 960x540 a swapped margin is off by 21 px, at 540x960
				// it is off by the same amount the other way.
				switch anchor {
				case AnchorTopLeft, AnchorTopCenter, AnchorTopRight:
					if abs(b.minY-marginY) > tol {
						t.Errorf("top edge at %d, want %d (margin)", b.minY, marginY)
					}
				case AnchorBottomLeft, AnchorBottomCenter, AnchorBottomRight:
					if want := canvas.h - marginY - 1; abs(b.maxY-want) > tol {
						t.Errorf("bottom edge at %d, want %d", b.maxY, want)
					}
				default:
					mid := (b.minY + b.maxY) / 2
					if abs(mid-canvas.h/2) > tol {
						t.Errorf("middle at %d, want %d", mid, canvas.h/2)
					}
				}
			})
		}
	}
}

// The single test that proves the per-destination story works.
//
// One overlay row, two renditions of opposite shape. If geometry were stored in
// pixels this would fail on the second, and the whole "same branding on 16:9
// and 9:16" premise would be false.
func TestTheSameOverlayRowScalesToBothCanvasShapes(t *testing.T) {
	ffmpeg := needFFmpeg(t, "ffmpeg")[0]
	logo := magentaPNG(t, ffmpeg, 100, 50)

	const pct = 0.25
	for _, canvas := range []struct{ w, h int }{{1280, 720}, {720, 1280}} {
		s := baseRendition()
		s.Width, s.Height = canvas.w, canvas.h
		s.Aspect, s.Deinterlace = "", ""
		s.Overlay = &OverlaySpec{
			ImagePath: logo, Anchor: AnchorBottomRight,
			WidthPct: pct, MarginXPct: 0.04, MarginYPct: 0.04,
		}
		b := magentaBBox(renderOverlay(t, ffmpeg, s), canvas.w, canvas.h)
		if b.n == 0 {
			t.Fatalf("%dx%d: the overlay is missing entirely", canvas.w, canvas.h)
		}
		gotW := b.maxX - b.minX + 1
		ratio := float64(gotW) / float64(canvas.w)
		if diff := ratio - pct; diff > 0.01 || diff < -0.01 {
			t.Errorf("%dx%d: the logo is %.3f of the canvas width, want %.3f -- "+
				"the same row does not scale across shapes",
				canvas.w, canvas.h, ratio, pct)
		}
		// And it must be fully inside the frame. A percentage that resolved
		// against the wrong dimension lands off-canvas, and a clipped logo is
		// exactly what the per-destination angle promises never happens.
		if b.minX < 0 || b.minY < 0 || b.maxX >= canvas.w || b.maxY >= canvas.h {
			t.Errorf("%dx%d: the logo is not fully inside the frame: %+v", canvas.w, canvas.h, b)
		}
	}
}

// Opacity is measurable: a half-transparent magenta logo over black is darker
// than an opaque one, and an omitted colorchannelmixer would make them equal.
func TestOpacityActuallyChangesThePixels(t *testing.T) {
	ffmpeg := needFFmpeg(t, "ffmpeg")[0]
	logo := magentaPNG(t, ffmpeg, 100, 100)

	brightness := func(opacity float64) int {
		s := baseRendition()
		s.Width, s.Height = 320, 240
		s.Aspect, s.Deinterlace = "", ""
		s.Overlay = &OverlaySpec{
			ImagePath: logo, Anchor: AnchorCenter, WidthPct: 0.5, Opacity: opacity,
		}
		frame := renderOverlay(t, ffmpeg, s)
		// The centre pixel is inside the logo for a centred 50%-wide overlay.
		i := ((240/2)*320 + 320/2) * 3
		return int(frame[i]) + int(frame[i+1]) + int(frame[i+2])
	}

	opaque, half := brightness(1), brightness(0.5)
	if half >= opaque {
		t.Errorf("a 50%% overlay is as bright as an opaque one (%d vs %d); "+
			"colorchannelmixer is not being applied", half, opaque)
	}
	if half == 0 {
		t.Error("a 50% overlay is invisible; the alpha is being applied twice or inverted")
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// The graph is only ever built from a path we resolved; this pins the rule that
// it is a real -i and never a movie= argument, which synth.go:345 explains.
func TestTheImageIsAnInputNeverAMovieFilter(t *testing.T) {
	s := baseRendition()
	s.Width, s.Height = 1920, 1080
	s.Overlay = &OverlaySpec{
		// A path carrying every character a filtergraph treats as a separator.
		ImagePath: "/data/overlays/my logo, v2 [final]:x.png",
		Anchor:    AnchorTopLeft, WidthPct: 0.2,
	}
	args := RenditionArgs(s)
	line := join(args)
	if strings.Contains(line, "movie=") {
		t.Errorf("the image became a movie= filter argument: %s", line)
	}
	// The path must appear as its own argv element, unescaped and unquoted --
	// which is exactly what makes a hostile path harmless.
	var found bool
	for _, a := range args {
		if a == s.Overlay.ImagePath {
			found = true
		}
	}
	if !found {
		t.Errorf("the image path is not a standalone argv element: %v", args)
	}
	if strings.Contains(overlayGraph(s, encoderProfile{}, s.Width, s.Height), s.Overlay.ImagePath) {
		t.Error("the image path leaked into the filtergraph, where its separators would be parsed")
	}
}
