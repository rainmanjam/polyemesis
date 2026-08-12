package ffmpeg

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// buildRawStream muxes one raw elementary stream of a known length and frame
// rate, and returns its path.
//
// NO CONTAINER, which is the entire point: `-f h264` writes the Annex-B
// bitstream and nothing else, so there is nowhere in the file for a duration to
// be written down. ffprobe reports format.duration as N/A for every file this
// builds, which is the property #118 refused on and #218 counts through.
//
// ONE SKIP SITE for the whole file, the same rule buildSample states: a per-test
// skip is a free pass, and an FFmpeg without libx264 would silently stop
// exercising this gate several tests at a time and print ok.
func buildRawStream(t *testing.T, path, format, encoder string, rate, seconds string) string {
	t.Helper()
	ffmpegBin := needFFmpeg(t, "ffmpeg")[0]
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=" + rate,
		"-c:v", encoder, "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-t", seconds, "-f", format, "-y", path}
	if out, err := exec.Command(ffmpegBin, args...).CombinedOutput(); err != nil {
		t.Skipf("this FFmpeg cannot write a raw %s stream (%v: %s)", format, err, out)
	}
	return path
}

func bothBins(t *testing.T) Bins {
	t.Helper()
	b := needFFmpeg(t, "ffmpeg", "ffprobe")
	return Bins{FFmpeg: b[0], FFprobe: b[1]}
}

// A RAW ELEMENTARY STREAM IS ADMITTED WITH A COUNTED LENGTH, which is #218.
//
// Before this, all three of these formats were on selfContainedFormats -- an
// operator handed a .h264 dump by an encoder has a real file -- and all three
// were then refused by the duration branch, because a bitstream with no
// container has nowhere to declare a length. The remedy offered was "re-save it
// as MP4 or MPEG-TS", which is real but is manual work the product can do.
//
// THE FIXTURES ARE NOT 25 FPS, and that is deliberate rather than incidental.
// See TestACountedLengthIsTheRealOneAndNotTheDemuxersAssumption for what a
// 25 fps fixture would have failed to catch.
func TestARawElementaryStreamIsAcceptedWithACountedDuration(t *testing.T) {
	bins := bothBins(t)
	dir := t.TempDir()

	cases := []struct {
		name    string
		format  string
		encoder string
		rate    string
	}{
		{"dump.h264", "h264", "libx264", "30"},
		{"dump.hevc", "hevc", "libx265", "30"},
		{"dump.m2v", "mpeg2video", "mpeg2video", "30"},
	}
	var exercised int
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := buildRawStream(t, filepath.Join(dir, tc.name), tc.format, tc.encoder, tc.rate, "2")

			res, err := ProbeFile(context.Background(), bins, path)
			if err != nil {
				t.Fatalf("a raw %s dump was refused: %v\n"+
					"#218: a stream with no container to declare a length is not a "+
					"stream whose length cannot be established -- ProbeFile counts it",
					tc.format, err)
			}
			exercised++
			if res.Video == nil {
				t.Fatalf("no video stream reported for a raw %s dump", tc.format)
			}
			// The value, not merely its existence. A branch that accepted the
			// file and left DurationSeconds at zero would pass an
			// "it was accepted" assertion and hand the normalise worker the
			// unbounded disk estimate this whole path exists to avoid.
			if math.Abs(res.DurationSeconds-2) > 0.25 {
				t.Errorf("DurationSeconds = %v, want about 2", res.DurationSeconds)
			}
			// And the PROVENANCE. This is the field that stops a counted
			// length being laundered into the same standing as a declared one.
			if res.DurationSource != DurationCounted {
				t.Errorf("DurationSource = %q, want %q: a length nothing declared "+
					"and polyemesis counted must say so",
					res.DurationSource, DurationCounted)
			}
		})
	}
	// The aggregate, for the same reason the unsupported-format table carries
	// one: three t.Skip calls would report ok having asserted nothing.
	if exercised == 0 {
		t.Fatal("no raw elementary stream reached the assertions, so this test proved nothing")
	}
	t.Logf("counted the length of %d/%d raw elementary streams", exercised, len(cases))
}

// THE COUNTED LENGTH IS THE REAL ONE, not the demuxer's assumption -- and this
// is the test that separates a derivation from a plausible wrong number.
//
// #218 proposed `ffprobe -count_frames` or a timed `ffmpeg -f null -` pass.
// There is an obvious third option that looks far cheaper than either:
// nb_read_packets / avg_frame_rate, which counts packets without decoding and
// costs 0.15s over a 192 MB file against 2.9s for the decode. IT IS WRONG, and
// wrong in a way no 25 fps fixture can show: ffprobe reports avg_frame_rate as
// 25/1 for EVERY raw H.264 and HEVC stream regardless of the real rate, because
// that is the raw demuxer's hardcoded fallback and not a reading of anything.
//
// Measured on 17-second fixtures with FFmpeg 8.1.2, that arithmetic gives:
//
//	30 fps -> 20.400s      50 fps -> 34.000s      60 fps -> 40.800s
//
// So the fixtures here are 30 fps and 50 fps and the tolerance is tight enough
// to exclude the wrong answer by a wide margin. A test built on 25 fps media
// would pass against the broken implementation and against a correct one alike,
// which is the vacuous guard this repository keeps having to delete.
//
// The same reasoning rules out r_frame_rate, which is exactly 2x the true rate
// for H.264 and exactly 1x for HEVC -- a halving rule that depends on the codec
// is not a rule to carry -- and packet or frame timestamps, which ffprobe
// reports as N/A at every level for these files.
func TestACountedLengthIsTheRealOneAndNotTheDemuxersAssumption(t *testing.T) {
	bins := bothBins(t)
	dir := t.TempDir()

	// Both rates are chosen so that the demuxer's 25/1 assumption produces an
	// answer OUTSIDE the tolerance: at 30 fps a 3s file has 90 frames, which
	// the assumption reads as 3.6s; at 50 fps it has 150, read as 6.0s.
	cases := []struct {
		name        string
		format      string
		encoder     string
		rate        string
		wrongAnswer float64
	}{
		{"r30.h264", "h264", "libx264", "30", 3.6},
		{"r50.hevc", "hevc", "libx265", "50", 6.0},
	}
	var exercised int
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := buildRawStream(t, filepath.Join(dir, tc.name), tc.format, tc.encoder, tc.rate, "3")
			res, err := ProbeFile(context.Background(), bins, path)
			if err != nil {
				t.Fatalf("ProbeFile refused a raw %s dump: %v", tc.format, err)
			}
			exercised++
			if math.Abs(res.DurationSeconds-3) > 0.25 {
				t.Errorf("DurationSeconds = %v, want about 3", res.DurationSeconds)
			}
			// Stated separately and in its own words, so a failure says WHICH
			// wrong implementation produced it rather than only that a number
			// missed.
			if math.Abs(res.DurationSeconds-tc.wrongAnswer) < 0.25 {
				t.Errorf("DurationSeconds = %v, which is %v -- the answer you get by "+
					"dividing the packet count by ffprobe's avg_frame_rate. That field "+
					"is 25/1 for every raw %s stream whatever its real rate (%s fps "+
					"here); it is the demuxer's fallback, not a reading",
					res.DurationSeconds, tc.wrongAnswer, tc.format, tc.rate)
			}
		})
	}
	if exercised == 0 {
		t.Fatal("no non-25fps raw stream reached the assertions, so this test proved nothing")
	}
}

// A COUNTED LENGTH AND A DECLARED ONE ARE DISTINGUISHABLE, differentially.
//
// The anti-laundering guard, and it is written as a DIFFERENCE on purpose. An
// implementation that stamped every result DurationCounted would satisfy the
// raw arm alone; one that stamped every result DurationDeclared would satisfy
// the container arm alone; one that set nothing would satisfy neither. Only a
// ProbeResult that actually distinguishes the two passes both.
//
// The same source content is used for both arms, so the two ProbeResults differ
// in exactly the thing under test and in nothing else.
func TestACountedLengthIsNotReportedAsADeclaredOne(t *testing.T) {
	bins := bothBins(t)
	dir := t.TempDir()
	ffmpegBin := bins.FFmpeg

	raw := buildRawStream(t, filepath.Join(dir, "same.h264"), "h264", "libx264", "30", "2")

	// The identical bitstream, put in a container that writes a duration down.
	// FATAL RATHER THAN SKIPPED, unlike buildRawStream's encoder check. This is
	// `-c copy` into the mpegts muxer: no encoder is involved, so an FFmpeg that
	// just wrote the raw stream can always do it. A skip here would be a free
	// pass for the whole differential.
	wrapped := filepath.Join(dir, "same.ts")
	if out, err := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error",
		"-f", "h264", "-i", raw, "-c", "copy", "-f", "mpegts", "-y", wrapped).CombinedOutput(); err != nil {
		t.Fatalf("remuxing the raw stream to MPEG-TS failed (%v: %s)", err, out)
	}

	rawRes, err := ProbeFile(context.Background(), bins, raw)
	if err != nil {
		t.Fatalf("ProbeFile on the raw stream: %v", err)
	}
	wrappedRes, err := ProbeFile(context.Background(), bins, wrapped)
	if err != nil {
		t.Fatalf("ProbeFile on the remuxed stream: %v", err)
	}

	if rawRes.DurationSource != DurationCounted {
		t.Errorf("the raw stream's DurationSource = %q, want %q",
			rawRes.DurationSource, DurationCounted)
	}
	if wrappedRes.DurationSource != DurationDeclared {
		t.Errorf("the remuxed stream's DurationSource = %q, want %q: MPEG-TS writes a "+
			"duration down and ProbeFile must report that it read one rather than "+
			"counting it", wrappedRes.DurationSource, DurationDeclared)
	}
	if rawRes.DurationSource == wrappedRes.DurationSource {
		t.Fatalf("both files reported DurationSource=%q, so the field does not "+
			"distinguish a counted length from a declared one and carries no "+
			"information at all", rawRes.DurationSource)
	}
	// The numbers agree; the standing of the two claims does not. That is the
	// whole reason the provenance is a separate field rather than being
	// inferrable from the value.
	if math.Abs(rawRes.DurationSeconds-wrappedRes.DurationSeconds) > 0.25 {
		t.Errorf("the same bitstream measured %v counted and %v declared",
			rawRes.DurationSeconds, wrappedRes.DurationSeconds)
	}
}

// AN INSTALL WITH NO FFMPEG STILL REFUSES, which is #118's guarantee and #218
// must not spend it.
//
// The count is what makes these files acceptable, so an install that cannot
// count must give the answer it gave before -- a refusal at the door, with the
// remedy -- rather than accepting a file the normalise worker will then refuse
// forever. Both halves are asserted: the sentinel, and that Refused() names it,
// because Refused is what decides whether the caller treats this as a verdict
// about the FILE or as a fault of the SERVER, and those delete the upload and
// keep it respectively.
func TestWithoutAnFFmpegARawStreamIsRefusedRatherThanAccepted(t *testing.T) {
	bins := bothBins(t)
	path := buildRawStream(t, filepath.Join(t.TempDir(), "dump.h264"),
		"h264", "libx264", "30", "1")

	_, err := ProbeFile(context.Background(), Bins{FFprobe: bins.FFprobe}, path)
	if err == nil {
		t.Fatal("a raw stream was ACCEPTED by an install with no ffmpeg to count it. " +
			"Its length is still unknown, and the normalise worker will refuse it " +
			"permanently -- accepted at the door and unusable forever is the state " +
			"#118 closed")
	}
	if !errors.Is(err, ErrNoDuration) {
		t.Errorf("error = %v, want ErrNoDuration", err)
	}
	if !Refused(err) {
		t.Errorf("Refused(%v) = false, so the upload handler would treat this as a "+
			"fault of the server and store the file unchecked", err)
	}
	// The remedy survives. It is the only thing an operator can act on here.
	if !strings.Contains(err.Error(), "MP4") {
		t.Errorf("the refusal no longer names a remedy: %v", err)
	}
}

// A COUNT THAT WAS CUT SHORT IS NOT A VERDICT ABOUT THE FILE.
//
// ErrNoDuration is in Refused, and Refused is what tells internal/api "this is
// about the operator's bytes" -- which deletes the completed upload. So a count
// killed by a client disconnect or a deadline must NOT come back as
// ErrNoDuration, or a remote caller acquires a way to make the server destroy a
// file it accepted, by hanging up at the right moment. That is the same
// fail-open rule the interruption arm in probeUpload already enforces, and this
// pins that the new branch obeys it.
//
// A stand-in ffmpeg rather than a large fixture, because the alternative is a
// wall-clock budget: "a real count of a big enough file will not finish in N
// seconds" measures the machine, and on a slow CI runner it measures it wrongly
// in whichever direction ends the test. A script that sleeps until it is killed
// has no budget in it at all.
//
// POSIX-only, exactly like TestProbeFileReturnsWhenItsContextIsCancelled two
// files over, which stands in for a probe binary the same way and for the same
// reason. The branch it pins is not platform-specific; the stand-in is.
func TestACountThatWasCutShortIsNotAVerdictAboutTheFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binary this needs is a POSIX shell script")
	}
	bins := bothBins(t)
	dir := t.TempDir()
	path := buildRawStream(t, filepath.Join(dir, "dump.h264"), "h264", "libx264", "30", "1")

	// Real ffprobe, so the header read succeeds and execution genuinely reaches
	// the counting branch; a stand-in ffmpeg, so the count is the only slow
	// thing and the cancellation lands inside it.
	// `exec`, unlike the WaitDelay test's script, which deliberately leaves a
	// grandchild holding the pipe. That behaviour has its own test; paying its
	// five-second WaitDelay again here would be five seconds spent on a
	// question this test is not asking.
	slow := filepath.Join(dir, "slow-ffmpeg")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatalf("write stand-in ffmpeg: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := ProbeFile(ctx, Bins{FFprobe: bins.FFprobe, FFmpeg: slow}, path)
	if err == nil {
		t.Fatal("a cancelled count reported success")
	}
	if Refused(err) {
		t.Fatalf("Refused(%v) = true. A count that was cut short is not a verdict "+
			"about the file, and internal/api DELETES the operator's completed "+
			"upload for a verdict -- so this hands a remote caller a way to destroy "+
			"an accepted file by disconnecting at the right moment", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the context error in the chain so the caller can "+
			"recognise an interruption", err)
	}
}

// THE COUNT ONLY RUNS FOR A FORMAT NAME THAT PINS A SINGLE DEMUXER.
//
// format_name is the comma-joined list of names ONE demuxer registers under, so
// "mov,mp4,m4a,3gp,3g2,mj2" is one demuxer with six names and there is no way to
// know which of the six FFmpeg's -f will accept. Guessing there means running
// FFmpeg with an unpinned or wrong demuxer over bytes the allowlist has already
// ruled on, which is the second, unguarded format detection ProbeFile's
// allowlist exists to prevent.
//
// The ffmpeg path handed in DOES NOT EXIST, which is what makes this
// differential rather than decorative: with the check, the error names the
// demuxer count and nothing is ever forked; without it, the error is a fork
// failure. An assertion that merely required "an error" would pass either way.
func TestAMultiNameFormatIsNotGuessedAtForTheCount(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-ffmpeg")
	_, err := countDuration(context.Background(), missing,
		"mov,mp4,m4a,3gp,3g2,mj2", filepath.Join(t.TempDir(), "x.mp4"))
	if err == nil {
		t.Fatal("a six-name format was handed to -f without complaint")
	}
	if !strings.Contains(err.Error(), "demuxer") {
		t.Errorf("error = %v, want one naming the demuxer count. An error mentioning "+
			"fork/exec means the guess was made and FFmpeg was run anyway", err)
	}
	// The single-name case must still be attempted, or the guard above would be
	// satisfied by a function that refuses everything.
	_, err = countDuration(context.Background(), missing, "h264",
		filepath.Join(t.TempDir(), "x.h264"))
	if err == nil {
		t.Fatal("a nonexistent ffmpeg reported a duration")
	}
	if strings.Contains(err.Error(), "demuxer") {
		t.Errorf("a single-name format was refused as a multi-demuxer one: %v", err)
	}
}

// THE DEMUXER PIN IS IN THE ARGV, AND IT IS AN INPUT OPTION.
//
// `-f` means "input format" before `-i` and "output format" after it. The same
// token in the wrong position pins nothing, runs FFmpeg's own detection over
// the file instead, and would still produce a correct duration for every honest
// fixture in this package -- so no end-to-end test can see the difference. This
// is why the argv is built by a function of its own.
func TestTheCountPinsTheDemuxerAsAnInputOption(t *testing.T) {
	args := CountDurationArgs("h264", "/tmp/dump.h264")

	inputAt := -1
	for i, a := range args {
		if a == "-i" {
			inputAt = i
			break
		}
	}
	if inputAt < 0 {
		t.Fatalf("no -i in %v", args)
	}
	pinned := false
	for i := 0; i+1 < inputAt; i++ {
		if args[i] == "-f" && args[i+1] == "h264" {
			pinned = true
		}
	}
	if !pinned {
		t.Errorf("the demuxer is not pinned before -i in %v.\n"+
			"After -i, -f names the OUTPUT format and FFmpeg detects the input "+
			"itself -- a second, unguarded format detection over bytes ProbeFile's "+
			"allowlist has already ruled on", args)
	}
	// The protocol pin, for the same reason ProbeFile and build.go carry it.
	whitelisted := false
	for i := 0; i+1 < inputAt; i++ {
		if args[i] == "-protocol_whitelist" && args[i+1] == "file" {
			whitelisted = true
		}
	}
	if !whitelisted {
		t.Errorf("-protocol_whitelist file is not pinned before -i in %v", args)
	}
	// And the clock must be the machine-readable one on stdout, not the status
	// line on stderr whose format is not a contract.
	if !strings.Contains(strings.Join(args, " "), "-progress pipe:1") {
		t.Errorf("the count does not ask for -progress pipe:1: %v", args)
	}
}

// COUNTING COSTS NOTHING FOR A FILE THAT DECLARES ITS OWN LENGTH.
//
// The branch is keyed on the missing duration, so every container ever accepted
// must reach the same result by the same route and must not pay for a decode it
// does not need. Asserted WITHOUT A WALL-CLOCK BUDGET: the same file is probed
// with an ffmpeg that cannot possibly run -- a path that does not exist -- and
// if the count were reached at all, it would fail.
//
// That is a stronger statement than a stopwatch and it costs no time: a budget
// would have to be loose enough for the slowest CI runner, which makes it loose
// enough to miss a decode of a short fixture.
func TestAFileThatDeclaresItsLengthIsNeverCounted(t *testing.T) {
	bins := bothBins(t)
	dir := t.TempDir()
	sample := buildSample(t, filepath.Join(dir, "declared.mkv"), "-t", "1")

	res, err := ProbeFile(context.Background(),
		Bins{FFprobe: bins.FFprobe, FFmpeg: filepath.Join(dir, "no-such-ffmpeg")}, sample)
	if err != nil {
		t.Fatalf("a container that declares its own duration was refused: %v.\n"+
			"The ffmpeg path handed in does not exist, so this means the count was "+
			"reached for a file that had already answered", err)
	}
	if res.DurationSource != DurationDeclared {
		t.Errorf("DurationSource = %q, want %q", res.DurationSource, DurationDeclared)
	}
	if res.DurationSeconds <= 0 {
		t.Errorf("DurationSeconds = %v for a container that declares one", res.DurationSeconds)
	}
}

// A TRUNCATED FINAL PROGRESS BLOCK CANNOT PULL THE COUNTED LENGTH BACKWARDS.
//
// FFmpeg's -progress counters are cumulative, so "the last block" is the
// obvious reading and it is wrong in one direction that matters. ParseProgress
// emits on every terminator, and a run killed mid-write -- or a stdout stream
// the cap has dropped bytes from the end of -- leaves a final block carrying
// the PREVIOUS block's counters with only the terminator fresh. Read as the
// last block, that is a duration shorter than the file.
//
// Short is the dangerous direction: this number becomes estimateBytes' -fs cap
// on the normalise encode, so an under-reading truncates an operator's
// legitimate media rather than merely mis-labelling it.
//
// SYNTHETIC INPUT ON PURPOSE. FFmpeg emits a progress block about twice a
// second of WALL time, so no fixture small enough for a unit test produces more
// than one, and against a single block "furthest" and "last" are the same
// function -- the reason this behaviour is a function of its own rather than a
// closure. Measured: with a real 2-second fixture, replacing the furthest-block
// rule with a first-block one changed no test in this package.
func TestATruncatedFinalProgressBlockCannotShortenTheCount(t *testing.T) {
	// Three complete blocks. The third is the shape a killed or capped stream
	// leaves: `progress=end` arrives while out_time_us still holds the value
	// from the block before, because the fresh one never made it.
	stream := strings.Join([]string{
		"frame=25", "out_time_us=1000000", "progress=continue",
		"frame=50", "out_time_us=2000000", "progress=continue",
		"frame=50", "progress=end",
		"",
	}, "\n")

	got, err := furthestOutTimeMS(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("furthestOutTimeMS: %v", err)
	}
	if got != 2000 {
		t.Errorf("furthestOutTimeMS = %d ms, want 2000. Reading the LAST block "+
			"instead of the furthest one takes the answer from a terminator whose "+
			"counters never arrived", got)
	}

	// The control: an ordinary stream that ends cleanly must give the same
	// answer by the same route, or the rule above is satisfied by a function
	// that ignores the end of every stream.
	clean := "frame=25\nout_time_us=1000000\nprogress=continue\n" +
		"frame=50\nout_time_us=2000000\nprogress=end\n"
	got, err = furthestOutTimeMS(strings.NewReader(clean))
	if err != nil {
		t.Fatalf("furthestOutTimeMS: %v", err)
	}
	if got != 2000 {
		t.Errorf("furthestOutTimeMS on a clean stream = %d ms, want 2000", got)
	}

	// And a stream that never reported a time at all reports zero rather than
	// something, which is what CountDurationSeconds turns into a refusal.
	got, err = furthestOutTimeMS(strings.NewReader("frame=0\nprogress=end\n"))
	if err != nil {
		t.Fatalf("furthestOutTimeMS: %v", err)
	}
	if got != 0 {
		t.Errorf("furthestOutTimeMS on a stream with no time = %d ms, want 0", got)
	}
}

// THE ZERO VALUE OF DurationSource IS "UNKNOWN", NOT "DECLARED".
//
// Small, and it is the property the whole design rests on. Every ProbeResult
// built anywhere other than ParseProbe -- by Probe for a live relay, by a test,
// by a caller that has not been written yet -- gets the zero value, and if that
// zero value meant "the container said so" then every such result would be
// making the strongest of the three claims by default. Nothing would ever
// notice, because the number beside it would look the same.
func TestAnUnpopulatedProbeResultClaimsNothingAboutItsDuration(t *testing.T) {
	var res ProbeResult
	if res.DurationSource != DurationUnknown {
		t.Fatalf("the zero DurationSource is %q; it must be DurationUnknown, or every "+
			"result nobody populated silently claims to have been read from a "+
			"container", res.DurationSource)
	}
	if DurationUnknown == DurationDeclared || DurationUnknown == DurationCounted {
		t.Fatal("DurationUnknown is not distinct from the two positive answers")
	}
	if DurationDeclared == DurationCounted {
		t.Fatal("DurationDeclared and DurationCounted are the same value, so the field " +
			"cannot distinguish what it exists to distinguish")
	}
	// A live relay has no duration and must say so rather than saying nothing
	// that could be read as "zero seconds, declared".
	live, err := ParseProbe([]byte(`{"streams":[],"format":{"format_name":"mpegts"}}`))
	if err != nil {
		t.Fatalf("ParseProbe: %v", err)
	}
	if live.DurationSource != DurationUnknown {
		t.Errorf("an input with no duration reported DurationSource=%q", live.DurationSource)
	}
}
