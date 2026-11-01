package ffmpeg

import (
	"strings"
	"testing"
)

// captionBurnSpec is a live-caption burn-in as the engine builds it: the text is
// a FILE the captioner rewrites, not a literal.
func captionBurnSpec() *TextSpec {
	return &TextSpec{
		TextFile: "/data/captions/burn:source-1.txt",
		FontFile: "/data/fonts/Inter-Regular.ttf",
		Anchor:   AnchorBottomCenter,
		SizePct:  0.05,
		Box:      true,
	}
}

// TestLiveCaptionBurnInRereadsItsTextFileOnEveryFrame is the live half of #8's
// burn-in. FFmpeg's subtitles filter reads its whole file once, when the filter
// starts (measured against the shipped image's FFmpeg 8.1.3: a cue appended to
// the file after the encode started was never drawn), so it cannot burn in
// captions that are still being spoken. drawtext with textfile and reload=1
// re-reads the file for every frame (measured: an atomic rename mid-encode was
// drawn), so the burn-in names the file the captioner rewrites, with the path
// escaped for the two levels a filtergraph unescapes, and never takes it as a
// template.
//
// MUTATION: drawtextFilter ignores TextFile. Observed: FAIL, no drawtext at all
// (a spec with no literal text is not drawn).
func TestLiveCaptionBurnInRereadsItsTextFileOnEveryFrame(t *testing.T) {
	got := drawtextFilter(captionBurnSpec(), frameSize{W: 1280, H: 720})
	if !strings.Contains(got, `:textfile=/data/captions/burn\\:source-1.txt`) {
		t.Fatalf("the caption file is not named, or its colon is not escaped twice: %s", got)
	}
	if !strings.Contains(got, ":reload=1") {
		t.Errorf("reload=1 missing, so the file is read once and the burned caption never changes: %s", got)
	}
	if strings.Contains(got, ":text=") {
		t.Errorf("a literal text option beside textfile: %s", got)
	}
	if !strings.HasSuffix(got, ":expansion=none") {
		t.Errorf("expansion=none missing, so a caption is evaluated as a template: %s", got)
	}
}

// TestBurnedCaptionsAreDrawnOnBothGraphPathsOverTheOperatorsText checks that a
// rendition with caption burn-in draws it on the -vf path and on the
// filter_complex path (an image overlay), after the operator's own text, so a
// caption sits on top.
//
// MUTATION: the filter_complex path leaves Captions out. Observed: FAIL on that
// path.
func TestBurnedCaptionsAreDrawnOnBothGraphPathsOverTheOperatorsText(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec RenditionSpec
	}{
		{"the -vf path", RenditionSpec{Width: 1280, Height: 720, Text: textSpec(), Captions: captionBurnSpec()}},
		{"the filter_complex path", RenditionSpec{
			Width: 1280, Height: 720, Text: textSpec(), Captions: captionBurnSpec(),
			Overlay: &OverlaySpec{ImagePath: "/data/logo.png", WidthPct: 0.2, Opacity: 1},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			if g := overlayGraph(tc.spec, encoderProfile{}, tc.spec.outputSize()); g != "" {
				got = g
			} else {
				got = videoFilter(tc.spec, encoderProfile{})
			}
			op, capt := strings.Index(got, ":text="), strings.Index(got, ":textfile=")
			if capt < 0 {
				t.Fatalf("no caption burn-in in %q", got)
			}
			if op < 0 || op > capt {
				t.Errorf("the caption is not drawn after the operator's text: %s", got)
			}
		})
	}
	// Without captions the argv is what it was.
	plain := RenditionSpec{Width: 1280, Height: 720}
	if got := videoFilter(plain, encoderProfile{}); strings.Contains(got, "drawtext") {
		t.Errorf("a rendition without burn-in draws text: %s", got)
	}
}
