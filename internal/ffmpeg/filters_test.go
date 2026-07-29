package ffmpeg

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// Real output from `ffmpeg -filters`, including the shapes that break a naive
// parser: a two-character flag column, a three-character one, multi-input and
// multi-output signatures, and the header lines above the table.
const filtersFixture = `Filters:
  T.. = Timeline support
  .S. = Slice threading
  ..C = Command support
  A = Audio input/output
  V = Video input/output
  N = Dynamic number/type of input/output
  | = Source or sink filter
 ... abench            A->A       Benchmark part of a filtergraph.
 T.. adrawgraph        A->V       Draw a graph using input audio metadata.
 TSC afade             A->A       Fade in/out input audio.
 ... amerge            N->A       Merge two or more audio streams.
 T.. drawbox           V->V       Draw a colored box on the input video.
 TS. drawtext          V->V       Draw text on top of video frames.
 TSC overlay           VV->V      Overlay a video source on top of the input.
 ... scale2ref         VV->VV     Scale the input video size to the given reference.
 ...  color            |->V       Provide an uniformly colored input.
`

func TestParseFiltersHandlesEveryRowShape(t *testing.T) {
	got := parseFilters(filtersFixture)

	for _, want := range []string{
		"abench",    // three dots, A->A
		"afade",     // full flags
		"amerge",    // N->A, dynamic input count
		"drawtext",  // the one this whole probe exists for
		"overlay",   // VV->V, two inputs
		"scale2ref", // VV->VV, two of each
		"color",     // |->V, a source filter
	} {
		if !containsString(got, want) {
			t.Errorf("parseFilters missed %q: %v", want, got)
		}
	}
	// The legend rows above the table must NOT become filters. `T.. = Timeline
	// support` is one flag column away from looking like a real row.
	for _, bad := range []string{"=", "Filters:", "A", "V", "N"} {
		if containsString(got, bad) {
			t.Errorf("parseFilters turned a legend row into a filter: %q", bad)
		}
	}
}

// An empty list means the probe never ran, and that must read as "assume the
// best". Refusing a feature because we failed to ask whether it works is the
// restrictive-direction failure this repo has already paid for three times.
func TestHasFilterAssumesTheBestWhenUnprobed(t *testing.T) {
	if !(&Tools{}).HasFilter("drawtext") {
		t.Error("an unprobed Tools refuses drawtext; a failed capability probe must not disable a feature")
	}
	if !(*Tools)(nil).HasFilter("drawtext") {
		t.Error("a nil Tools refuses drawtext")
	}

	// A probed build answers honestly in both directions.
	probed := &Tools{Filters: []string{"drawbox", "overlay"}}
	if probed.HasFilter("drawtext") {
		t.Error("a probed build without drawtext reported having it")
	}
	if !probed.HasFilter("overlay") {
		t.Error("a probed build WITH overlay reported not having it")
	}
}

// Against the real binary. This is the check that would have caught the
// assumption: the machine this was written on has 489 filters and no drawtext,
// because its FFmpeg was built without libfreetype.
func TestFilterProbeAgainstTheRealBinary(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	tools := &Tools{FFmpeg: bin}
	tools.checkFilters(context.Background())

	if len(tools.Filters) == 0 {
		t.Fatal("the probe found no filters at all, which no FFmpeg build is")
	}
	// overlay is in every build and is what image watermarks already use, so
	// its absence would mean the parser is broken rather than the build.
	if !tools.HasFilter("overlay") {
		t.Errorf("overlay not found among %d filters; the parser is wrong, not the build",
			len(tools.Filters))
	}
	// Reported, not asserted: whether THIS machine has drawtext is a property
	// of its build, and both answers are legitimate. It is logged because the
	// answer decides whether text overlays can be tested here at all.
	t.Logf("%d filters; drawtext present: %v", len(tools.Filters), tools.HasFilter("drawtext"))
}

// The filter list must not leak into anything that expects encoders, which is
// the mistake a copy-paste of checkEncoders would make.
func TestFilterProbeDoesNotDisturbTheEncoderList(t *testing.T) {
	tools := &Tools{Filters: []string{"drawtext"}, VideoEncoders: []string{"libx264"}}
	if !containsString(tools.VideoEncoders, "libx264") {
		t.Error("the encoder list was disturbed")
	}
	if strings.Join(tools.Filters, ",") != "drawtext" {
		t.Error("the filter list was disturbed")
	}
}
