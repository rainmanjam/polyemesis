package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func textSpec(mut ...func(*TextSpec)) *TextSpec {
	t := &TextSpec{
		Text:     "POLYEMESIS",
		FontFile: "/data/fonts/Inter-Regular.ttf",
		Anchor:   AnchorBottomLeft,
		SizePct:  0.05,
		Color:    "white",
	}
	for _, m := range mut {
		m(t)
	}
	return t
}

// expansion=none is the difference between "draw this string" and "evaluate
// this string", and drawtext's DEFAULT is to evaluate.
//
// Without it a '%' in a station name either breaks the stream or expands into
// something nobody typed, and `%{eif:...}` turns a database row into input for
// an expression evaluator. It is asserted on every path rather than once,
// because the two graph paths build the filter separately.
func TestDrawtextNeverInterpretsOperatorText(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec RenditionSpec
	}{
		{"the -vf path", RenditionSpec{Width: 1280, Height: 720, Text: textSpec()}},
		{"the filter_complex path", RenditionSpec{
			Width: 1280, Height: 720, Text: textSpec(),
			Overlay: &OverlaySpec{ImagePath: "/data/logo.png", WidthPct: 0.2, Opacity: 1},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			if g := overlayGraph(tc.spec, encoderProfile{}, tc.spec.Width, tc.spec.Height); g != "" {
				got = g
			} else {
				got = videoFilter(tc.spec, encoderProfile{})
			}
			if !strings.Contains(got, "drawtext=") {
				t.Fatalf("no drawtext in %q", got)
			}
			if !strings.Contains(got, "expansion=none") {
				t.Errorf("expansion=none missing, so operator text is evaluated as a template: %s", got)
			}
		})
	}
}

// The font path and the text both land in a filter argument, where ':' and '\'
// are metacharacters. On Windows the path is C:\...\Inter-Regular.ttf and
// carries both, so an unescaped one is a parse error rather than a
// misplacement.
func TestDrawtextEscapesAColonInThePathAndAwkwardText(t *testing.T) {
	got := drawtextFilter(textSpec(func(s *TextSpec) {
		// A colon is legal in a POSIX filename, so this exercises the
		// drive-letter hazard on every platform rather than only on Windows.
		s.FontFile = "/srv/my:fonts/Inter-Regular.ttf"
		s.Text = `12:30 - Tom's "show"`
	}), 1280, 720)

	// An unescaped colon ends the fontfile option early and the next thing the
	// parser looks for is an option name.
	if strings.Contains(got, "my:fonts") {
		t.Errorf("a colon in the font path is unescaped, which is a parse error: %s", got)
	}
	// `\\:`, not `\:`. A filtergraph is unescaped TWICE, so a single backslash
	// is eaten by the first pass and the colon arrives bare at the second.
	// Measured against the real parser -- see escapeLavfiArg for the table, and
	// TestAFontPathContainingAColonOpens for the end-to-end proof. Asserting
	// the single-escaped form here is what let the Windows break through.
	if !strings.Contains(got, `my\\:fonts`) {
		t.Errorf("the colon was not escaped for BOTH unescape passes: %s", got)
	}
	if !strings.Contains(got, `12\\:30`) {
		t.Errorf("a colon in the text was not escaped for both passes: %s", got)
	}
	if !strings.Contains(got, `\\'`) {
		t.Errorf("an apostrophe in the text was not escaped for both passes: %s", got)
	}
}

// The Windows form, asserted through filterPath rather than through a literal.
//
// filepath.ToSlash is a NO-OP on POSIX -- deliberately, because a backslash is
// a legal character in a POSIX filename and rewriting it would rename the file
// (the bug internal/clipper's fileURL shipped). So this test cannot check the
// Windows rendering by feeding it a Windows-shaped string on Linux; what it CAN
// pin is that the two properties hold on whichever platform it runs on:
// separators come out as the filtergraph wants them, and colons are escaped.
func TestFilterPathLeavesNoUnescapedSeparatorHazard(t *testing.T) {
	got := filterPath(filepath.Join("srv", "poly fonts", "Inter-Regular.ttf"))

	// Whatever the platform, no bare backslash may survive: on Windows ToSlash
	// removed them, and on POSIX there were none to begin with. A `\\` here is
	// the exact shape that broke the Windows runner, because a filtergraph
	// unescapes twice and it collapses at the wrong moment.
	if strings.Contains(got, `\\`) {
		t.Errorf("filterPath produced a doubled backslash, which the filtergraph "+
			"unescapes twice and mis-parses: %s", got)
	}
	if strings.Contains(got, `:`) && !strings.Contains(got, `\:`) {
		t.Errorf("an unescaped colon survived: %s", got)
	}
}

func TestDrawtextIsAbsentWhenThereIsNothingToDraw(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec *TextSpec
	}{
		{"nil", nil},
		{"empty text", textSpec(func(s *TextSpec) { s.Text = "  " })},
		{"zero size", textSpec(func(s *TextSpec) { s.SizePct = 0 })},
		// No font is not "use the system default": the shipping image has no
		// system fonts, so that would be a stream that fails to start.
		{"no font", textSpec(func(s *TextSpec) { s.FontFile = "" })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := drawtextFilter(tc.spec, 1280, 720); got != "" {
				t.Errorf("drawtextFilter = %q, want empty", got)
			}
		})
	}
}

// The whole point of percentage geometry: the same spec on a landscape and a
// portrait rendition must produce type that reads at both sizes.
func TestTextSizeScalesWithTheRenditionHeight(t *testing.T) {
	spec := textSpec(func(s *TextSpec) { s.SizePct = 0.1 })
	if got := textSizePx(spec.SizePct, 1080); got != 108 {
		t.Errorf("10%% of 1080 = %d, want 108", got)
	}
	if got := textSizePx(spec.SizePct, 360); got != 36 {
		t.Errorf("10%% of 360 = %d, want 36", got)
	}
	// Floored rather than allowed to reach zero: drawtext accepts fontsize=0,
	// draws nothing, and exits 0, which looks exactly like the feature being
	// ignored.
	if got := textSizePx(0.0001, 100); got < MinTextSizePx {
		t.Errorf("a tiny percentage gave fontsize %d, below the %d floor", got, MinTextSizePx)
	}
}

// drawtext's frame variables are w/h and text_w/text_h. The overlay filter's
// are main_w/main_h and overlay_w/overlay_h. Using the wrong pair is a
// filtergraph error at start time.
func TestTextPositionUsesDrawtextsOwnVariables(t *testing.T) {
	got := drawtextFilter(textSpec(func(s *TextSpec) { s.Anchor = AnchorCenter }), 1280, 720)
	for _, bad := range []string{"main_w", "main_h", "overlay_w", "overlay_h"} {
		if strings.Contains(got, bad) {
			t.Errorf("drawtext uses %s, which it does not define: %s", bad, got)
		}
	}
	if !strings.Contains(got, "(w-text_w)/2") || !strings.Contains(got, "(h-text_h)/2") {
		t.Errorf("centre anchor did not centre: %s", got)
	}
}

// Text draws over the logo, not under it.
func TestTextIsDrawnAfterTheImageOverlay(t *testing.T) {
	g := overlayGraph(RenditionSpec{
		Width: 1280, Height: 720, Text: textSpec(),
		Overlay: &OverlaySpec{ImagePath: "/data/logo.png", WidthPct: 0.2, Opacity: 1},
	}, encoderProfile{}, 1280, 720)

	ov, dt := strings.Index(g, "overlay=x="), strings.Index(g, "drawtext=")
	if ov < 0 || dt < 0 {
		t.Fatalf("expected both stages: %s", g)
	}
	if dt < ov {
		t.Errorf("text is composited before the image, so the logo covers it: %s", g)
	}
}

// A box at full opacity hides the picture, so the opacity is what makes the
// feature usable. An operator colour already carrying an alpha must not get a
// second one appended -- FFmpeg rejects that.
func TestBoxOpacityFoldsIntoTheColourWithoutDoublingIt(t *testing.T) {
	got := drawtextFilter(textSpec(func(s *TextSpec) {
		s.Box, s.BoxColor, s.BoxOpacity = true, "black", 0.5
	}), 1280, 720)
	if !strings.Contains(got, "boxcolor=black@0.5") {
		t.Errorf("box opacity was not applied: %s", got)
	}

	got = drawtextFilter(textSpec(func(s *TextSpec) {
		s.Box, s.BoxColor, s.BoxOpacity = true, "black@0.25", 0.5
	}), 1280, 720)
	if strings.Contains(got, "@0.25@") || strings.Contains(got, "0.25@0.5") {
		t.Errorf("two alphas were stacked onto one colour, which FFmpeg rejects: %s", got)
	}
}

// A rendition with no text must produce the argv it always did. This is the
// same safety argument the image overlay work rested on.
func TestRenditionArgsAreUnchangedWhenThereIsNoText(t *testing.T) {
	base := RenditionSpec{
		InRelayURL: "udp://127.0.0.1:20001", OutRelayURL: "udp://127.0.0.1:20002",
		Width: 1280, Height: 720, Encoder: "libx264",
		VideoKbps: 3000, MaxrateKbps: 3000, BufsizeKbps: 6000,
	}
	withNil := strings.Join(RenditionArgs(base), " ")
	base.Text = &TextSpec{}
	withEmpty := strings.Join(RenditionArgs(base), " ")
	if withNil != withEmpty {
		t.Errorf("an empty TextSpec changed the arguments:\n nil: %s\n set: %s", withNil, withEmpty)
	}
	if strings.Contains(withNil, "drawtext") {
		t.Errorf("drawtext appeared with no text configured: %s", withNil)
	}
}

// THE measurement. Everything above inspects strings; this proves FFmpeg
// accepts the graph and that a '%' in operator text is drawn rather than
// expanded.
//
// Run under ./scripts/test-in-docker.sh, which is the fontless image that
// ships. A Homebrew FFmpeg on macOS has no drawtext at all and skips.
func TestDrawtextGraphRunsAndDoesNotExpandAPercentSign(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	tools := &Tools{FFmpeg: bin}
	tools.checkFilters(context.Background())
	if !tools.HasFilter("drawtext") {
		t.Skip("this FFmpeg has no drawtext (built without libfreetype); " +
			"run ./scripts/test-in-docker.sh to test against the image that ships")
	}

	dir := filepath.Join(t.TempDir(), FontsDirName)
	if err := EnsureFonts(dir); err != nil {
		t.Fatal(err)
	}
	font, err := FontPath(dir, DefaultFont)
	if err != nil {
		t.Fatal(err)
	}

	render := func(spec *TextSpec) int {
		out := filepath.Join(t.TempDir(), "f.png")
		graph := "color=c=black:s=640x200:d=1"
		if f := drawtextFilter(spec, 640, 200); f != "" {
			graph += "," + f
		}
		cmd := exec.Command(bin, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", graph, "-frames:v", "1", "-y", out)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the generated graph was rejected: %v\ngraph: %s\n%s", err, graph, b)
		}
		return nonBlackPixels(t, bin, out)
	}

	plain := render(&TextSpec{
		Text: "LIVE", FontFile: font, SizePct: 0.4, Color: "white", Anchor: AnchorTopLeft,
	})
	if plain < 300 {
		t.Fatalf("plain text drew %d pixels; it did not render", plain)
	}

	// '%' is drawtext's expansion character. With expansion=none it is a
	// glyph; without it, this either errors or draws something else entirely.
	pct := render(&TextSpec{
		Text: "100% LIVE", FontFile: font, SizePct: 0.4, Color: "white", Anchor: AnchorTopLeft,
	})
	if pct <= plain {
		t.Errorf("%q drew %d pixels and %q drew %d; the longer string must draw more, "+
			"so the %% was swallowed rather than rendered", "100% LIVE", pct, "LIVE", plain)
	}

	// The box must actually cover pixels, since it is the legibility mechanism.
	boxed := render(&TextSpec{
		Text: "LIVE", FontFile: font, SizePct: 0.4, Color: "white", Anchor: AnchorTopLeft,
		Box: true, BoxColor: "white", BoxOpacity: 1,
	})
	if boxed <= plain {
		t.Errorf("the box drew %d pixels against %d unboxed; it did not render", boxed, plain)
	}
	t.Logf("plain=%d withPercent=%d boxed=%d", plain, pct, boxed)
}

// The escaping, proven against the real parser rather than against a string.
//
// A colon is legal in a POSIX filename, which is what makes the Windows
// drive-letter hazard reproducible here: if the escaping is wrong, FFmpeg fails
// to parse the graph exactly as it did on the Windows runner. Without this the
// only place the bug could be caught was CI.
func TestAFontPathContainingAColonOpens(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	tools := &Tools{FFmpeg: bin}
	tools.checkFilters(context.Background())
	if !tools.HasFilter("drawtext") {
		t.Skip("this FFmpeg has no drawtext; run ./scripts/test-in-docker.sh")
	}
	if os.PathSeparator != '/' {
		t.Skip("a colon cannot appear in a Windows path component")
	}

	// A directory whose NAME contains a colon, holding a real font.
	dir := filepath.Join(t.TempDir(), "my:fonts")
	if err := EnsureFonts(dir); err != nil {
		t.Fatalf("EnsureFonts into a colon path: %v", err)
	}
	font, err := FontPath(dir, DefaultFont)
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "f.png")
	graph := "color=c=black:s=320x120:d=1," + drawtextFilter(&TextSpec{
		Text: "OK", FontFile: font, SizePct: 0.5, Color: "white",
	}, 320, 120)
	b, err := exec.Command(bin, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", graph, "-frames:v", "1", "-y", out).CombinedOutput()
	if err != nil {
		t.Fatalf("FFmpeg rejected a font path containing a colon: %v\ngraph: %s\n%s", err, graph, b)
	}
	if n := nonBlackPixels(t, bin, out); n < 100 {
		t.Errorf("the graph parsed but drew %d pixels; the font did not load", n)
	}
}

// A font written by EnsureFonts must survive the round trip into a real
// filtergraph on THIS platform, whatever its path separator is.
func TestTheEmbeddedFontPathSurvivesTheFilterGraph(t *testing.T) {
	dir := filepath.Join(t.TempDir(), FontsDirName)
	if err := EnsureFonts(dir); err != nil {
		t.Fatal(err)
	}
	font, err := FontPath(dir, DefaultFont)
	if err != nil {
		t.Fatal(err)
	}
	got := drawtextFilter(&TextSpec{
		Text: "x", FontFile: font, SizePct: 0.1, Color: "white",
	}, 1280, 720)

	// Every separator in the escaped path must be doubled on Windows and
	// untouched elsewhere; either way the raw path must not appear verbatim
	// when it contains a metacharacter.
	if strings.ContainsAny(font, `:\`) && strings.Contains(got, "fontfile="+font+":") {
		t.Errorf("the font path went in unescaped: %s", got)
	}
	if _, err := os.Stat(font); err != nil {
		t.Errorf("FontPath returned a path that does not exist: %v", err)
	}
}
