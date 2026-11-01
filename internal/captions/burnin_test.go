package captions

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// TestBurnInFilterEscapesItsPathForTheOptionAndThenForTheFiltergraph. FFmpeg
// reads a filter's argument twice. The filtergraph parser splits the graph on
// brackets, commas and semicolons and consumes one level of backslashes and
// quotes; then the filter's option parser splits its arguments on colons and
// consumes a second level. A path has to be escaped for the option first and the
// result escaped again for the graph. Escaping once, as this used to, lets the
// graph parser eat the backslash that was meant to protect a quote or a colon
// from the option parser: "it's" reached the filter as "its".
//
// MUTATION, internal/captions/burnin.go: put back the old single pass in
// filterOptionValue (one backslash before each of \ ' : [ ] , ; and space).
// Observed: FAIL -- the colon, quote and backslash cases here, and the FFmpeg
// round trip below, where FFmpeg looked for a file with the quote stripped.
func TestBurnInFilterEscapesItsPathForTheOptionAndThenForTheFiltergraph(t *testing.T) {
	cases := map[string]string{
		"/data/captions.srt":    "subtitles=filename=/data/captions.srt",
		"/data/a:b.srt":         `subtitles=filename=/data/a\\:b.srt`,
		"/data/it's, [odd].srt": `subtitles=filename=/data/it\\\'s\, \[odd\].srt`,
		"/data/a;b.srt":         `subtitles=filename=/data/a\;b.srt`,
		`/data/back\slash.srt`:  `subtitles=filename=/data/back\\\\slash.srt`,
	}
	for in, want := range cases {
		if got := SubtitlesFilter(in); got != want {
			t.Errorf("SubtitlesFilter(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFFmpegOpensAFileWhosePathNeedsBothEscapingLevels is the same escape read
// back by FFmpeg itself. The shipped image's FFmpeg has libass and the subtitles
// filter; a developer's FFmpeg may not, so the round trip goes through the movie
// source, which takes its filename through exactly the same two parsers. The
// file name uses only characters every OS allows; the temp directory supplies a
// drive colon and backslashes on Windows.
func TestFFmpegOpensAFileWhosePathNeedsBothEscapingLevels(t *testing.T) {
	ffmpeg := testenv.FFmpegBinary(t, "ffmpeg", "ffmpeg is needed to read the escaped filter argument back")
	dir := t.TempDir()
	path := filepath.Join(dir, "it's, [od;d] x.nut")
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=32x32:d=1", "-c:v", "ffv1", path)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generating the fixture failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	graph := "movie=filename=" + filterOptionValue(path) + "[v]"
	run := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-filter_complex", graph, "-map", "[v]", "-frames:v", "1", "-f", "null", "-")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("FFmpeg could not open %q through the escaped graph %q: %v\n%s", path, graph, err, out)
	}
}
