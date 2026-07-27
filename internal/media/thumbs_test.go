package media

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// ------------------------------------------------------------------- poster

func TestPosterSecondsAvoidsTheBlackFrameEveryBroadcastOpensWith(t *testing.T) {
	tests := []struct {
		name string
		spec PosterSpec
		want float64
	}{
		{"a tenth of the way in", PosterSpec{DurationSeconds: 3600}, 360},
		{"an explicit moment wins", PosterSpec{DurationSeconds: 3600, AtSeconds: 12}, 12},
		{"unknown duration falls back", PosterSpec{}, DefaultPosterSeconds},
		{"a short clip still gets a poster from inside itself", PosterSpec{DurationSeconds: 4}, 0.4},
		{"a negative duration is unknown", PosterSpec{DurationSeconds: -5}, DefaultPosterSeconds},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.PosterSeconds(); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("PosterSeconds = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPosterArgsSeeksBeforeTheInputSoAnHourLongFileIsNotDecodedInFull(t *testing.T) {
	args := PosterArgs(PosterSpec{Input: "in.mkv", Output: "poster.jpg", DurationSeconds: 3600})

	ss, i := argIndex(args, "-ss"), argIndex(args, "-i")
	if ss < 0 || i < 0 || ss > i {
		t.Fatalf("-ss must precede -i to be an input seek: %v", args)
	}
	mustArg(t, args, "-ss", "360")
	mustArg(t, args, "-frames:v", "1")
	mustArg(t, args, "-f", "image2")
	mustArg(t, args, "-q:v", "3")
	for _, want := range []string{"-an", "-sn", "-dn"} {
		if !hasArg(args, want) {
			t.Fatalf("%s is missing: %v", want, args)
		}
	}
}

func TestPosterArgsChoosesARepresentativeFrameUnlessToldNotTo(t *testing.T) {
	tests := []struct {
		name   string
		frames int
		want   string
	}{
		{"default picks the best of 100", 0, "thumbnail=100,scale=-2:360"},
		{"an explicit count", 30, "thumbnail=30,scale=-2:360"},
		{"a count below two is not a choice", 1, "thumbnail=2,scale=-2:360"},
		{"absurd counts are clamped", 100000, "thumbnail=500,scale=-2:360"},
		{"negative means grab whatever is there", -1, "scale=-2:360"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := PosterArgs(PosterSpec{Input: "in.mkv", Output: "p.jpg", SmartFrames: tc.frames})
			mustArg(t, args, "-vf", tc.want)
		})
	}
}

func TestJPEGQualityStaysInsideFFmpegsScale(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"unset", 0, DefaultJPEGQuality},
		{"best", 1, 1},
		{"worst", 31, 31},
		{"below the floor", -5, DefaultJPEGQuality},
		{"above the ceiling", 99, 31},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := jpegQuality(tc.in); got != tc.want {
				t.Fatalf("jpegQuality(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// ------------------------------------------------------------- contact sheet

func TestContactSheetIntervalSpansTheWholeRecording(t *testing.T) {
	tests := []struct {
		name string
		spec ContactSheetSpec
		want float64
	}{
		{"the default grid over an hour", ContactSheetSpec{DurationSeconds: 3600}, 180},
		{"a bigger grid is finer", ContactSheetSpec{DurationSeconds: 3600, Cols: 10, Rows: 10}, 36},
		{"unknown duration falls back", ContactSheetSpec{}, DefaultContactInterval},
		{"a short recording", ContactSheetSpec{DurationSeconds: 20}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.Interval(); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("Interval = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestContactSheetArgsTilesOneSheetAcrossTheRecording(t *testing.T) {
	args := ContactSheetArgs(ContactSheetSpec{Input: "in.mkv", Output: "c.jpg", DurationSeconds: 3600})

	vf, ok := argAfter(args, "-vf")
	if !ok {
		t.Fatal("no -vf")
	}
	if !strings.HasPrefix(vf, "fps=1/180,") {
		t.Fatalf("-vf = %q, want a 180s spacing", vf)
	}
	if !strings.Contains(vf, "tile=5x4:margin=4:padding=2:color=black") {
		t.Fatalf("-vf = %q, missing the tile grid", vf)
	}
	// Half an interval in, so tile one is the middle of the first slice rather
	// than the slate every broadcast opens with.
	mustArg(t, args, "-ss", "90")
	mustArg(t, args, "-frames:v", "1")
}

func TestContactSheetNormalizedClampsAGridTypoRatherThanAllocatingAGigapixel(t *testing.T) {
	got := ContactSheetSpec{Cols: 100000, Rows: -3, TileWidth: 99999, TileHeight: 0}.Normalized()

	if got.Cols != MaxTileGrid {
		t.Fatalf("Cols = %d, want %d", got.Cols, MaxTileGrid)
	}
	if got.Rows != DefaultContactRows {
		t.Fatalf("Rows = %d, want %d", got.Rows, DefaultContactRows)
	}
	if got.Margin != DefaultContactMargin || got.Padding != DefaultContactPadding {
		t.Fatalf("gutter = %d/%d, want the defaults", got.Margin, got.Padding)
	}
	if none := (ContactSheetSpec{Margin: -1, Padding: -1}).Normalized(); none.Margin != 0 || none.Padding != 0 {
		t.Fatalf("a negative gutter should mean none, got %d/%d", none.Margin, none.Padding)
	}
	if got.TileWidth != MaxTileDimension {
		t.Fatalf("TileWidth = %d, want %d", got.TileWidth, MaxTileDimension)
	}
	if got.TileHeight != DefaultContactTileHigh {
		t.Fatalf("TileHeight = %d, want %d", got.TileHeight, DefaultContactTileHigh)
	}
}

// --------------------------------------------------------------- sprite sheet

func TestSpriteIntervalWidensRatherThanTruncatingALongRecording(t *testing.T) {
	tests := []struct {
		name string
		spec SpriteSpec
		want float64
	}{
		{"an hour at the default spacing", SpriteSpec{DurationSeconds: 3600}, 5},
		{"an explicit spacing", SpriteSpec{DurationSeconds: 3600, IntervalSeconds: 10}, 10},
		{"unknown duration keeps the request", SpriteSpec{IntervalSeconds: 2}, 2},
		// Six hours at 5s would want 4320 thumbnails; the interval widens so the
		// preview still covers the whole recording.
		{"six hours widens", SpriteSpec{DurationSeconds: 21600}, 18},
		{"a sub-half-second request is floored", SpriteSpec{IntervalSeconds: 0.01}, MinSpriteInterval},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.Interval(); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("Interval = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSpriteFramesAndSheetsAgreeWithTheGrid(t *testing.T) {
	tests := []struct {
		name       string
		spec       SpriteSpec
		wantFrames int
		wantSheets int
	}{
		{"exactly one sheet", SpriteSpec{DurationSeconds: 500}, 100, 1},
		{"one frame into a second sheet", SpriteSpec{DurationSeconds: 505}, 101, 2},
		{"a partial first sheet", SpriteSpec{DurationSeconds: 12}, 3, 1},
		{"unknown duration has no sheets", SpriteSpec{}, 0, 0},
		{"a tiny grid", SpriteSpec{DurationSeconds: 25, Cols: 2, Rows: 1}, 5, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.Frames(); got != tc.wantFrames {
				t.Fatalf("Frames = %d, want %d", got, tc.wantFrames)
			}
			if got := tc.spec.Sheets(); got != tc.wantSheets {
				t.Fatalf("Sheets = %d, want %d", got, tc.wantSheets)
			}
		})
	}
}

// The sheets and the VTT are one artefact: any gutter between tiles offsets
// every rectangle after the first, and the viewer sees a sliver of the
// neighbouring frame for the whole recording.
func TestSpriteArgsPinsTheGridGutterToZero(t *testing.T) {
	args := SpriteArgs(SpriteSpec{Input: "in.mkv", OutputPattern: "sprite-%03d.jpg", DurationSeconds: 600})

	vf, _ := argAfter(args, "-vf")
	if !strings.Contains(vf, "tile=10x10:margin=0:padding=0:color=black") {
		t.Fatalf("-vf = %q, want a gutterless 10x10 grid", vf)
	}
	if !strings.Contains(vf, "scale=160:90:force_original_aspect_ratio=decrease") {
		t.Fatalf("-vf = %q, tiles are not forced to an exact size", vf)
	}
	// No -frames:v: the muxer writes as many sheets as the recording fills.
	if hasArg(args, "-frames:v") {
		t.Fatalf("-frames:v would truncate the sheet run: %v", args)
	}
	if args[len(args)-1] != "sprite-%03d.jpg" {
		t.Fatalf("output pattern is not last: %v", args)
	}
}

func TestSheetNameNumbersFromOneTheWayTheImageMuxerDoes(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		n       int
		want    string
	}{
		{"the first sheet", "/rec/media/r/sprite-%03d.jpg", 1, "sprite-001.jpg"},
		{"a later sheet", "/rec/media/r/sprite-%03d.jpg", 42, "sprite-042.jpg"},
		{"only the base name, so the vtt is relocatable", "/a/b/sprite-%d.jpg", 7, "sprite-7.jpg"},
		{"a pattern with no verb is left alone", "/a/b/sprite.jpg", 3, "sprite.jpg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SheetName(tc.pattern, tc.n); got != tc.want {
				t.Fatalf("SheetName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVTTTimeUsesTheOnlyFormThatSurvivesAnHour(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want string
	}{
		{"zero", 0, "00:00:00.000"},
		{"seconds", 5.5, "00:00:05.500"},
		{"minutes", 125, "00:02:05.000"},
		{"past an hour", 3725.25, "01:02:05.250"},
		{"many hours", 36000, "10:00:00.000"},
		{"negative is clamped", -4, "00:00:00.000"},
		{"NaN is clamped", math.NaN(), "00:00:00.000"},
		{"rounding", 1.9999, "00:00:02.000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := vttTime(tc.in); got != tc.want {
				t.Fatalf("vttTime(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The whole scrub preview is this arithmetic. Every cue's rectangle has to land
// exactly on its tile or the player draws two half frames.
func TestVTTAddressesEachThumbnailByItsExactRectangle(t *testing.T) {
	s := SpriteSpec{
		OutputPattern:   "/rec/media/r/sprite-%03d.jpg",
		DurationSeconds: 30,
		IntervalSeconds: 10,
		Cols:            2, Rows: 2,
		TileWidth: 160, TileHeight: 90,
	}
	got := s.VTT()

	want := strings.Join([]string{
		"WEBVTT",
		"",
		"00:00:00.000 --> 00:00:10.000",
		"sprite-001.jpg#xywh=0,0,160,90",
		"",
		"00:00:10.000 --> 00:00:20.000",
		"sprite-001.jpg#xywh=160,0,160,90",
		"",
		"00:00:20.000 --> 00:00:30.000",
		"sprite-001.jpg#xywh=0,90,160,90",
		"",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("VTT =\n%s\nwant\n%s", got, want)
	}
}

func TestVTTRollsOntoTheNextSheetWhenTheGridFills(t *testing.T) {
	s := SpriteSpec{
		OutputPattern:   "sprite-%03d.jpg",
		DurationSeconds: 25,
		IntervalSeconds: 5,
		Cols:            2, Rows: 2,
		TileWidth: 160, TileHeight: 90,
	}
	got := s.VTT()

	// Frame 4 (0-based) is the first of the second sheet, back at the origin.
	if !strings.Contains(got, "sprite-002.jpg#xywh=0,0,160,90") {
		t.Fatalf("VTT does not roll onto sheet 2:\n%s", got)
	}
	if n := strings.Count(got, "-->"); n != 5 {
		t.Fatalf("VTT has %d cues, want 5", n)
	}
	// The last cue stops at the real end rather than running past it.
	if !strings.Contains(got, "00:00:20.000 --> 00:00:25.000") {
		t.Fatalf("last cue does not end at the recording's end:\n%s", got)
	}
}

func TestVTTTrimsTheLastCueToTheRecordingsRealEnd(t *testing.T) {
	s := SpriteSpec{OutputPattern: "s-%d.jpg", DurationSeconds: 12, IntervalSeconds: 5}
	got := s.VTT()
	if !strings.Contains(got, "00:00:10.000 --> 00:00:12.000") {
		t.Fatalf("last cue was not trimmed to 12s:\n%s", got)
	}
}

// A VTT with no cues is not a degraded preview, it is a file that makes the
// player draw nothing while pretending a preview exists.
func TestVTTIsEmptyRatherThanCuelessWhenTheDurationIsUnknown(t *testing.T) {
	if got := (SpriteSpec{OutputPattern: "s-%d.jpg"}).VTT(); got != "" {
		t.Fatalf("VTT = %q, want empty", got)
	}
}

func TestVTTCueCountMatchesTheSheetsFFmpegWillHaveWritten(t *testing.T) {
	for _, seconds := range []float64{1, 5, 5.001, 499, 500, 501, 3600} {
		s := SpriteSpec{OutputPattern: "s-%03d.jpg", DurationSeconds: seconds}
		cues := strings.Count(s.VTT(), "-->")
		if cues != s.Frames() {
			t.Fatalf("%.3fs: %d cues but Frames() = %d", seconds, cues, s.Frames())
		}
		// The highest sheet the VTT names must exist in the run FFmpeg writes.
		last := SheetName(s.OutputPattern, s.Sheets())
		if s.Frames() > 0 && !strings.Contains(s.VTT(), last) {
			t.Fatalf("%.3fs: VTT never names the last sheet %q", seconds, last)
		}
	}
}

// ----------------------------------------------------------------- one pass

func TestThumbnailArgsBuildsEveryArtefactFromOneDecode(t *testing.T) {
	args := ThumbnailArgs(ThumbnailSpec{
		Input:           "/rec/rec-1.mkv",
		DurationSeconds: 600,
		Poster:          PosterSpec{Output: "poster.jpg"},
		ContactSheet:    ContactSheetSpec{Output: "contact.jpg"},
		Sprites:         SpriteSpec{OutputPattern: "sprite-%03d.jpg"},
	})

	// One input, one decode, three outputs. Three passes would be three times
	// the CPU stolen from a live stream for the same bytes.
	if n := countArg(args, "-i"); n != 1 {
		t.Fatalf("-i appears %d times, want 1: %v", n, args)
	}
	fc, ok := argAfter(args, "-filter_complex")
	if !ok {
		t.Fatalf("no -filter_complex: %v", args)
	}
	if !strings.HasPrefix(fc, "[0:v]split=3[po][co][so];") {
		t.Fatalf("filter graph does not split one decode three ways: %q", fc)
	}
	for _, label := range []string{"[pout]", "[cout]", "[sout]"} {
		if !strings.Contains(fc, label) {
			t.Fatalf("filter graph is missing %s: %q", label, fc)
		}
		if !hasArg(args, label) {
			t.Fatalf("%s is never mapped to an output: %v", label, args)
		}
	}
	if args[len(args)-1] != "sprite-%03d.jpg" {
		t.Fatalf("sprite output is not last: %v", args)
	}
}

func TestThumbnailArgsFallsBackToTheDedicatedBuilderForASingleArtefact(t *testing.T) {
	tests := []struct {
		name string
		spec ThumbnailSpec
		want []string
	}{
		{
			"poster only", ThumbnailSpec{Input: "in.mkv", DurationSeconds: 600,
				Poster: PosterSpec{Output: "p.jpg"}},
			PosterArgs(PosterSpec{Input: "in.mkv", DurationSeconds: 600, Output: "p.jpg"}),
		},
		{
			"contact sheet only", ThumbnailSpec{Input: "in.mkv", DurationSeconds: 600,
				ContactSheet: ContactSheetSpec{Output: "c.jpg"}},
			ContactSheetArgs(ContactSheetSpec{Input: "in.mkv", DurationSeconds: 600, Output: "c.jpg"}),
		},
		{
			"sprites only", ThumbnailSpec{Input: "in.mkv", DurationSeconds: 600,
				Sprites: SpriteSpec{OutputPattern: "s-%03d.jpg"}},
			SpriteArgs(SpriteSpec{Input: "in.mkv", DurationSeconds: 600, OutputPattern: "s-%03d.jpg"}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ThumbnailArgs(tc.spec)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("ThumbnailArgs =\n%v\nwant\n%v", got, tc.want)
			}
			// A single artefact can use a real input seek, which a shared decode
			// cannot.
			if tc.name == "poster only" && !hasArg(got, "-ss") {
				t.Fatalf("a lone poster gave up its input seek: %v", got)
			}
		})
	}
}

func TestThumbnailArgsTrimsInsideTheGraphRatherThanSeekingTheWholeProcess(t *testing.T) {
	fc, _ := argAfter(ThumbnailArgs(ThumbnailSpec{
		Input:           "in.mkv",
		DurationSeconds: 600,
		Poster:          PosterSpec{Output: "p.jpg"},
		ContactSheet:    ContactSheetSpec{Output: "c.jpg"},
	}), "-filter_complex")

	// An input seek belongs to the whole process, and the contact sheet has to
	// start at zero.
	if !strings.Contains(fc, "[po]trim=start=60,setpts=PTS-STARTPTS,") {
		t.Fatalf("poster branch does not trim in-graph: %q", fc)
	}
	if !strings.Contains(fc, "split=2[po][co]") {
		t.Fatalf("graph splits the wrong number of ways: %q", fc)
	}
}

func TestThumbnailArgsIsNilWhenNothingWasAskedFor(t *testing.T) {
	if got := ThumbnailArgs(ThumbnailSpec{Input: "in.mkv"}); got != nil {
		t.Fatalf("ThumbnailArgs = %v, want nil", got)
	}
}

func TestThumbnailSpecNormalizedPushesTheDurationDownToEveryArtefact(t *testing.T) {
	got := ThumbnailSpec{
		Input:           "in.mkv",
		DurationSeconds: 1200,
		Poster:          PosterSpec{Output: "p.jpg"},
		ContactSheet:    ContactSheetSpec{Output: "c.jpg", DurationSeconds: 99},
		Sprites:         SpriteSpec{OutputPattern: "s.jpg"},
	}.Normalized()

	if got.Poster.DurationSeconds != 1200 || got.Sprites.DurationSeconds != 1200 {
		t.Fatalf("duration did not reach every sub-spec: %+v", got)
	}
	// An explicitly set sub-duration is not overwritten.
	if got.ContactSheet.DurationSeconds != 99 {
		t.Fatalf("an explicit sub-duration was overwritten: %v", got.ContactSheet.DurationSeconds)
	}
	for _, in := range []string{got.Poster.Input, got.ContactSheet.Input, got.Sprites.Input} {
		if in != "in.mkv" {
			t.Fatalf("input did not reach every sub-spec: %+v", got)
		}
	}
}

func TestThumbnailArgsQuotesNoUnsanitisedColourIntoTheFilterGraph(t *testing.T) {
	fc, _ := argAfter(ThumbnailArgs(ThumbnailSpec{
		Input:           "in.mkv",
		DurationSeconds: 60,
		ContactSheet:    ContactSheetSpec{Output: "c.jpg", Color: "red,scale=2:2"},
		Sprites:         SpriteSpec{OutputPattern: "s-%d.jpg", Color: "blue:1"},
	}), "-filter_complex")

	if strings.Contains(fc, "scale=2:2") {
		t.Fatalf("a colour re-cut the filter graph: %q", fc)
	}
	if n := strings.Count(fc, "color=black"); n != 2 {
		t.Fatalf("both colours should have fallen back to black: %q", fc)
	}
}

// A regression guard on the one number a viewer would actually notice.
func TestSpriteGeometryIsSelfConsistentAcrossGridShapes(t *testing.T) {
	for _, cols := range []int{1, 3, 10} {
		for _, rows := range []int{1, 4, 10} {
			s := SpriteSpec{
				OutputPattern: "s-%03d.jpg", DurationSeconds: 300, IntervalSeconds: 5,
				Cols: cols, Rows: rows, TileWidth: 160, TileHeight: 90,
			}
			vtt := s.VTT()
			maxX := (cols - 1) * 160
			maxY := (rows - 1) * 90
			for _, line := range strings.Split(vtt, "\n") {
				_, frag, ok := strings.Cut(line, "#xywh=")
				if !ok {
					continue
				}
				var x, y, w, h int
				if _, err := fmt.Sscanf(frag, "%d,%d,%d,%d", &x, &y, &w, &h); err != nil {
					t.Fatalf("unparseable fragment %q", frag)
				}
				if x < 0 || x > maxX || y < 0 || y > maxY {
					t.Fatalf("%dx%d: rectangle %s falls outside the sheet", cols, rows, frag)
				}
				if w != 160 || h != 90 {
					t.Fatalf("%dx%d: rectangle %s is not one tile", cols, rows, frag)
				}
			}
		}
	}
}
