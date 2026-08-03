package playlistmedia

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// TestEveryProfileFlagCarriesTheValueWeMeant is the profile check, and the only
// one: every item must agree on codec, timebase, resolution and channel layout
// or the concat demuxer errors or produces garbage, and the profile is fixed
// rather than derived because matching the live encoder would move the target
// on every settings change and leave every existing derivative stale with
// nothing saying so.
//
// A substring check over the joined argv used to sit above this one and was
// deleted as duplication: Contains("-ac") would still pass if the argv said
// `-ac 6`, and a six-channel item in a stereo playlist is precisely the
// mismatch this package exists to prevent. Every mutation that test could
// catch, this one catches strictly better -- each flag is checked as a PAIR and
// checked to appear exactly once, because a second `-r` later in the argv would
// silently win.
func TestEveryProfileFlagCarriesTheValueWeMeant(t *testing.T) {
	args := normaliseArgs("/in.mov", "/out.ts")
	for _, tc := range []struct{ flag, value string }{
		{"-c:v", "libx264"},
		{"-pix_fmt", "yuv420p"},
		{"-r", "30"},
		{"-c:a", "aac"},
		{"-ac", "2"},
		{"-ar", "48000"},
		{"-b:a", "192k"},
		{"-g", "60"},
		{"-keyint_min", "60"},
		{"-sc_threshold", "0"},
		{"-flags", "+cgop"},
		{"-maxrate", "6000k"},
		{"-f", "mpegts"},
		{"-preset", "veryfast"},
		// Stated, not derived. x264 would pick both from the preset, the pixel
		// format and the resolution, which is a value an FFmpeg upgrade could
		// move without anyone reviewing a line of this repo.
		{"-profile:v", "high"},
		{"-level", "4.0"},
	} {
		got, n := pairValue(args, tc.flag)
		switch {
		case n == 0:
			t.Errorf("profile is missing %s: %v", tc.flag, args)
		case n > 1:
			t.Errorf("%s appears %d times; the last one wins and nobody would notice: %v",
				tc.flag, n, args)
		case got != tc.value:
			t.Errorf("%s is %q, want %q", tc.flag, got, tc.value)
		}
	}
}

// The scale is a fit-and-pad, not a stretch, and setsar=1 is part of the
// profile: two files can agree on 1920x1080 and still disagree on sample aspect
// ratio, which the concat demuxer counts as a mismatch.
func TestTheProfileLetterboxesRatherThanStretchesAndPinsTheSampleAspect(t *testing.T) {
	vf, n := pairValue(normaliseArgs("/in.mov", "/out.ts"), "-vf")
	if n != 1 {
		t.Fatalf("expected exactly one -vf, got %d", n)
	}
	for _, want := range []string{
		"scale=1920:1080:force_original_aspect_ratio=decrease",
		"pad=1920:1080:",
		"setsar=1",
	} {
		if !strings.Contains(vf, want) {
			t.Errorf("filter chain is missing %q: %s", want, vf)
		}
	}
}

func TestTheOutputIsTheLastArgumentAndTheInputIsNamedOnce(t *testing.T) {
	args := normaliseArgs("/in.mov", "/out.ts")
	if got := args[len(args)-1]; got != "/out.ts" {
		t.Errorf("output should be the final argument, got %q", got)
	}
	if in, n := pairValue(args, "-i"); n != 1 || in != "/in.mov" {
		t.Errorf("expected one -i /in.mov, got %d occurrences of %q", n, in)
	}
}

// A source with no audio is not a source that gets a video-only derivative. The
// concat demuxer matches streams by position, so one silent item breaks the
// whole set rather than merely playing without sound — the two argv builders
// must therefore agree on every encoding flag and differ only in where the
// audio comes from.
func TestTheSilentProfileEncodesIdenticallyToTheAudioOne(t *testing.T) {
	audio := normaliseArgs("/in.mov", "/out.ts")
	silent := normaliseSilentArgs("/in.mov", "/out.ts")

	if in, n := pairValue(silent, "-map"); n != 2 || in != "0:v:0" {
		t.Errorf("silent profile should map the source's video and nothing else from it: %v", silent)
	}
	if !contains(silent, "1:a:0") || !contains(silent, "-shortest") {
		t.Errorf("silent profile should take stereo silence from lavfi and stop with the picture: %v", silent)
	}
	if !containsSubstring(silent, "anullsrc=channel_layout=stereo:sample_rate=48000") {
		t.Errorf("synthesised silence must already be at the profile's rate and layout: %v", silent)
	}

	// Everything from the stream-selection flags onward is the profile proper.
	if got, want := tailFrom(silent, "-sn"), tailFrom(audio, "-sn"); got != want {
		t.Errorf("the two profiles have drifted apart:\n silent: %s\n  audio: %s", got, want)
	}
}

// TestTheHardwareProbeIsNotConsulted is a guard on a decision rather than on
// output: a later reader looking to speed this up would reach for the GPU, and
// the GPU is the one resource a live encoder cannot share.
func TestTheHardwareProbeIsNotConsulted(t *testing.T) {
	joined := strings.Join(normaliseArgs("/in.mov", "/out.ts"), " ")
	for _, hw := range []string{
		"nvenc", "videotoolbox", "qsv", "vaapi", "v4l2m2m", "amf", "-hwaccel",
	} {
		if strings.Contains(joined, hw) {
			t.Errorf("profile reaches for hardware (%q); a normalisation that races the "+
				"live encoders for a GPU trades a stream for a file", hw)
		}
	}
}

// ------------------------------------------------------------------ the paths

func TestTheDerivativeIsKeyedOnTheUploadAndNothingElse(t *testing.T) {
	// The same upload named twice in a playlist -- at position 2 and again at
	// position 5 -- is one derivative and therefore one transcode.
	first := DerivativePath("/data", "show-1a2b3c4d.mp4")
	again := DerivativePath("/data", "show-1a2b3c4d.mp4")
	if first != again {
		t.Fatalf("the same upload produced two derivative paths: %q and %q", first, again)
	}
	if want := filepath.Join("/data", Dir, "show-1a2b3c4d.mp4.ts"); first != want {
		t.Errorf("DerivativePath = %q, want %q", first, want)
	}
	// The extension is kept rather than replaced: stripping it would map two
	// different uploads onto one file and the loser would play the winner's.
	if a, b := DerivativePath("/d", "show-1a2b.mp4"), DerivativePath("/d", "show-1a2b.mkv"); a == b {
		t.Errorf("two uploads collapsed onto one derivative: %q", a)
	}
}

// The derivative directory is NOT uploads/. uploads.Store.List reports every
// file it finds there as an operator upload, so a derivative written beside its
// source would show up in the media library as a file the operator supplied and
// be selectable as a playlist item in its own right.
func TestDerivativesDoNotLandInTheUploadsDirectory(t *testing.T) {
	got := DerivativePath("/data", "show.mp4")
	if strings.Contains(got, string(os.PathSeparator)+uploads.Dir+string(os.PathSeparator)) {
		t.Errorf("derivative %q is inside the uploads directory", got)
	}
	if !strings.HasPrefix(got, filepath.Join("/data", Dir)+string(os.PathSeparator)) {
		t.Errorf("derivative %q is not under %q", got, DerivativeDir("/data"))
	}
}

func TestADerivativePathCannotEscapeItsDirectory(t *testing.T) {
	for _, name := range []string{"../../etc/passwd", "a/b", "..", "."} {
		got := DerivativePath("/data", name)
		base := DerivativeDir("/data") + string(os.PathSeparator)
		if !strings.HasPrefix(got, base) {
			t.Errorf("DerivativePath(%q) = %q, which escapes %q", name, got, base)
		}
	}
}

func TestValidUploadNameRefusesAnythingThatIsNotABareFilename(t *testing.T) {
	for _, name := range []string{"show-1a2b.mp4", "a.ts", "UPPER_case-1.mkv"} {
		if !ValidUploadName(name) {
			t.Errorf("ValidUploadName(%q) = false, want true", name)
		}
	}
	// Both separators on every platform: os.PathSeparator is '/' on Linux, so a
	// check written with it accepts a backslash there and the same data
	// directory opened by a Windows build reads it as a path.
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b", "a\nb",
		strings.Repeat("x", 256)} {
		if ValidUploadName(name) {
			t.Errorf("ValidUploadName(%q) = true, want false", name)
		}
	}
}

// -------------------------------------------------------------- registration

type fakeRegistry struct {
	kinds  []jobs.Kind
	limits []int
	err    error
}

func (f *fakeRegistry) Register(kind jobs.Kind, limit int, _ jobs.Worker) error {
	f.kinds = append(f.kinds, kind)
	f.limits = append(f.limits, limit)
	return f.err
}

func TestRegisterWiresOneKindAtOneAtATime(t *testing.T) {
	var r fakeRegistry
	if err := newTestProcessor(t, t.TempDir()).Register(&r); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(r.kinds) != 1 || r.kinds[0] != KindNormalise {
		t.Fatalf("registered %v, want just %q", r.kinds, KindNormalise)
	}
	// One at a time. This is an FFmpeg run that takes every core it is given,
	// and the queue exists so heavy work yields to the live stream.
	if r.limits[0] != 1 || NormaliseLimit != 1 {
		t.Errorf("normalise limit is %d, want 1", r.limits[0])
	}
}

func TestRegisterReturnsTheQueuesError(t *testing.T) {
	boom := errors.New("kind already registered")
	if err := newTestProcessor(t, t.TempDir()).Register(&fakeRegistry{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("Register error = %v, want %v", err, boom)
	}
}

func TestTheJobFoldsRepeatsOfTheSameUploadOntoOneTarget(t *testing.T) {
	a, err := NewNormaliseJob(NormaliseParams{Upload: "show-1a2b.mp4"})
	if err != nil {
		t.Fatalf("NewNormaliseJob: %v", err)
	}
	b, _ := NewNormaliseJob(NormaliseParams{Upload: "show-1a2b.mp4"})
	if a.Target != b.Target || !a.Unique {
		t.Errorf("the same upload must dedupe in the queue: %q vs %q, unique=%v",
			a.Target, b.Target, a.Unique)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("the job the queue would store is invalid: %v", err)
	}
	if _, err := NewNormaliseJob(NormaliseParams{Upload: "../etc/passwd"}); err == nil {
		t.Error("a job was built for an upload name that is a path")
	}
}

// ------------------------------------------------------------------ the worker

func TestRunNormaliseWritesThroughAPartialAndPublishes(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")

	var sawPartial string
	p := newTestProcessor(t, dir, WithExecer(func(_ context.Context, cmd media.Command, _ media.Sink) error {
		sawPartial = cmd.Args[len(cmd.Args)-1]
		return os.WriteFile(sawPartial, []byte("normalised"), 0o644)
	}))

	rep := &recorder{}
	if err := p.RunNormalise(context.Background(), normaliseJob(t, "clip-1a2b.mp4"), rep); err != nil {
		t.Fatalf("RunNormalise: %v", err)
	}
	final := DerivativePath(dir, "clip-1a2b.mp4")
	if !strings.HasSuffix(sawPartial, PartialSuffix) || strings.TrimSuffix(sawPartial, PartialSuffix) != final {
		t.Errorf("FFmpeg wrote to %q, want %q", sawPartial, final+PartialSuffix)
	}
	if _, err := os.Stat(sawPartial); !os.IsNotExist(err) {
		t.Errorf("the .partial survived publication: %v", err)
	}
	info, err := os.Stat(final)
	if err != nil {
		t.Fatalf("derivative was not published: %v", err)
	}
	// Unix only, and the skip is the honest answer rather than a convenience.
	//
	// A FileMode is discarded by the syscall layer on Windows: every writable
	// file reports 0666, so this read `-rw-rw-rw-, want 0600` on the matrix
	// while asserting nothing at all. internal/tlsx hit the same wall and
	// answered it by checking the PROPERTY through internal/fsperm, which
	// applies a real ACL -- but that is not the right answer here. fsperm is
	// for material that is secret in itself (TLS keys, the ACME cache, the
	// secrets directory). A derivative is a transcode of an upload, and
	// uploads.Save uses a bare os.Chmod(0600) under an 0755 directory with no
	// ACL of its own. Calling CheckPrivate here would assert a property the
	// upload it came from does not have, and would fail on Windows for a
	// reason that has nothing to do with this file.
	//
	// So: pin the mode where the mode means something, and say plainly that on
	// Windows a derivative is exactly as protected as the upload beside it --
	// which is the media tree's existing property, not a gap this test should
	// pretend to close.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("derivative mode is %v, want 0600 — nothing outside this process reads it", perm)
		}
	}
	res, ok := rep.result.(NormaliseResult)
	if !ok || res.Path != final || res.Bytes == 0 || res.Reused {
		t.Errorf("result = %+v, want the published path and its size", rep.result)
	}
}

// A failed encode has already lost. A .partial left behind is then a
// disk-space problem the next attempt inherits, on the very path whose whole
// concern is that derivatives cost disk.
func TestAFailedEncodeLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")

	p := newTestProcessor(t, dir, WithExecer(func(_ context.Context, cmd media.Command, _ media.Sink) error {
		out := cmd.Args[len(cmd.Args)-1]
		if err := os.WriteFile(out, []byte("half a file"), 0o644); err != nil {
			return err
		}
		return errors.New("ffmpeg failed: exit status 1")
	}))
	if err := p.RunNormalise(context.Background(), normaliseJob(t, "clip-1a2b.mp4"), &recorder{}); err == nil {
		t.Fatal("a failed encode reported success")
	}
	entries, _ := os.ReadDir(DerivativeDir(dir))
	if len(entries) != 0 {
		t.Errorf("a failed encode left %d file(s) behind: %v", len(entries), entries)
	}
}

// The same upload used twice in a playlist normalises ONCE. The path is keyed
// on the upload, so all the worker has to do is not redo work whose output is
// already there -- an hour of CPU per repeat, otherwise.
func TestAnUploadThatIsAlreadyNormalisedIsNotNormalisedAgain(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")
	final := DerivativePath(dir, "clip-1a2b.mp4")
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(final, []byte("already normalised"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := newTestProcessor(t, dir, WithExecer(func(_ context.Context, _ media.Command, _ media.Sink) error {
		t.Error("FFmpeg was run for an upload that was already normalised")
		return nil
	}))
	rep := &recorder{}
	if err := p.RunNormalise(context.Background(), normaliseJob(t, "clip-1a2b.mp4"), rep); err != nil {
		t.Fatalf("RunNormalise: %v", err)
	}
	res, ok := rep.result.(NormaliseResult)
	if !ok || !res.Reused {
		t.Errorf("result = %+v, want Reused", rep.result)
	}
	// A zero-byte file is not a derivative; it is the wreckage of something.
	if err := os.WriteFile(final, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var ran bool
	p2 := newTestProcessor(t, dir, WithExecer(func(_ context.Context, cmd media.Command, _ media.Sink) error {
		ran = true
		return os.WriteFile(cmd.Args[len(cmd.Args)-1], []byte("normalised"), 0o644)
	}))
	if err := p2.RunNormalise(context.Background(), normaliseJob(t, "clip-1a2b.mp4"), &recorder{}); err != nil {
		t.Fatalf("RunNormalise: %v", err)
	}
	if !ran {
		t.Error("an empty derivative was treated as finished work")
	}
}

func TestASourceWithNoAudioStillGetsTheProfilesStereoTrack(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "slate-1a2b.mp4", "source bytes")

	var args []string
	p := newTestProcessor(t, dir,
		WithStreamProber(streams(true, false)),
		WithExecer(func(_ context.Context, cmd media.Command, _ media.Sink) error {
			args = cmd.Args
			return os.WriteFile(cmd.Args[len(cmd.Args)-1], []byte("normalised"), 0o644)
		}))
	rep := &recorder{}
	if err := p.RunNormalise(context.Background(), normaliseJob(t, "slate-1a2b.mp4"), rep); err != nil {
		t.Fatalf("RunNormalise: %v", err)
	}
	if !containsSubstring(args, "anullsrc") {
		t.Errorf("a video-only source was not given silence: %v", args)
	}
	if got, _ := pairValue(args, "-ac"); got != "2" {
		t.Errorf("synthesised audio is not stereo: %v", args)
	}
	if res, _ := rep.result.(NormaliseResult); !res.Silent {
		t.Error("the result does not record that the item is silent")
	}
}

// An audio-only upload is reachable: uploads.allowedExt accepts .wav, .flac,
// .aac, .mp3 and .m4a. Without a video probe FFmpeg exits with "Stream map
// '0:v:0' matches no streams", which comes back unclassified and therefore
// RETRYABLE, and the queue burns every attempt on a job that can never succeed
// while contending with the live stream each time.
//
// Refused rather than given a synthesised black picture: silence under a real
// video keeps a picture on air, but black under an audio file PUTS a black
// screen on air, which is what a slate exists to avoid.
func TestAnAudioOnlyUploadIsRefusedPermanently(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "podcast-1a2b.mp3", "source bytes")

	p := newTestProcessor(t, dir,
		WithStreamProber(streams(false, true)),
		WithExecer(func(context.Context, media.Command, media.Sink) error {
			t.Error("FFmpeg was started for a source with no video track")
			return nil
		}))

	err := p.RunNormalise(context.Background(), normaliseJob(t, "podcast-1a2b.mp3"), &recorder{})
	if err == nil {
		t.Fatal("an audio-only upload was accepted as a playlist item")
	}
	if !jobs.IsPermanent(err) {
		t.Fatalf("error %v is retryable; the queue would burn every attempt on it", err)
	}
	// The operator has to be able to act on it, which means knowing which file.
	if !strings.Contains(err.Error(), "podcast-1a2b.mp3") || !strings.Contains(err.Error(), "video") {
		t.Errorf("error %q does not name the upload and what is wrong with it", err)
	}
}

// A file FFprobe cannot parse will not become parseable on the next attempt.
// Its own words are preserved, because "this upload is broken" is only
// actionable if the operator can see how.
func TestAProbeThatCannotReadTheFileFailsPermanently(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "truncated-1a2b.mp4", "not media at all")

	p := newTestProcessor(t, dir,
		WithStreamProber(func(context.Context, string, string) (bool, error) {
			return false, jobs.Permanent(errors.New(
				"ffprobe could not read truncated-1a2b.mp4: moov atom not found"))
		}),
		WithExecer(func(context.Context, media.Command, media.Sink) error {
			t.Error("FFmpeg was started on a file FFprobe could not read")
			return nil
		}))

	err := p.RunNormalise(context.Background(), normaliseJob(t, "truncated-1a2b.mp4"), &recorder{})
	if err == nil || !jobs.IsPermanent(err) {
		t.Fatalf("error = %v, want a permanent failure", err)
	}
	if !strings.Contains(err.Error(), "moov atom not found") {
		t.Errorf("error %q dropped what FFprobe actually said", err)
	}
}

// ------------------------------------------------------------ the disk guard

func TestADerivativeThatWouldExhaustTheDiskIsRefusedBeforeItIsWritten(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")

	p := newTestProcessor(t, dir,
		WithFreeSpace(func(string) (uint64, error) { return DefaultMinFreeBytes + 1<<20, nil }),
		WithExecer(func(context.Context, media.Command, media.Sink) error {
			t.Error("FFmpeg was started on a volume that could not hold the output")
			return nil
		}))

	// An hour at the profile's capped bitrate is far more than the megabyte
	// above the reserve.
	job := normaliseJob(t, "clip-1a2b.mp4")
	job.Params = []byte(`{"upload":"clip-1a2b.mp4","durationMs":3600000}`)
	err := p.RunNormalise(context.Background(), job, &recorder{})
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("error = %v, want ErrNoSpace", err)
	}
	entries, _ := os.ReadDir(DerivativeDir(dir))
	if len(entries) != 0 {
		t.Errorf("the refused job still wrote %d file(s)", len(entries))
	}
}

// FAIL CLOSED. The one case where you cannot tell how much room is left is not
// the case to start writing gigabytes -- the same direction uploads.Save takes.
func TestAFreeSpaceCheckThatCannotAnswerRefusesTheJob(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")
	p := newTestProcessor(t, dir,
		WithFreeSpace(func(string) (uint64, error) { return 0, errors.New("statfs: permission denied") }),
		WithExecer(func(context.Context, media.Command, media.Sink) error {
			t.Error("FFmpeg was started although free space was unknown")
			return nil
		}))
	if err := p.RunNormalise(context.Background(), normaliseJob(t, "clip-1a2b.mp4"), &recorder{}); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("error = %v, want ErrNoSpace", err)
	}
}

func TestTheDiskEstimateIsAnUpperBoundWhenTheDurationIsKnown(t *testing.T) {
	// One minute at 6000 + 192 kbit/s is about 46 MB.
	got, bounded := estimateBytes(60_000, 1)
	if !bounded {
		t.Error("a known duration should be reported as a bound")
	}
	if want := int64(6192) * 60_000 / 8; got < want {
		t.Errorf("estimate %d is below the profile's own bitrate floor %d", got, want)
	}
	// Without one, the source's size is a guess and says so, because a short
	// low-resolution source normalises LARGER than it arrived.
	if got, bounded := estimateBytes(0, 4096); bounded || got != 4096 {
		t.Errorf("estimateBytes(0, 4096) = (%d, %v), want (4096, false)", got, bounded)
	}
}

// An absurd duration overflows the int64 multiply in estimateBytes. The guard
// still fails closed, but it fails closed while quoting a negative number of
// megabytes, and an error a reader cannot act on is the failure mode this repo
// keeps having to fix. Bounded at the params, before the arithmetic.
func TestAnAbsurdDurationIsRefusedBeforeItReachesTheEstimate(t *testing.T) {
	for _, ms := range []int64{-1, MaxDurationMS + 1, 1 << 62} {
		if err := (NormaliseParams{Upload: "clip-1a2b.mp4", DurationMS: ms}).Validate(); err == nil {
			t.Errorf("duration %d ms was accepted", ms)
		}
	}
	for _, ms := range []int64{0, 1, MaxDurationMS} {
		if err := (NormaliseParams{Upload: "clip-1a2b.mp4", DurationMS: ms}).Validate(); err != nil {
			t.Errorf("duration %d ms was refused: %v", ms, err)
		}
	}
	// The bound is what keeps the estimate positive, which is what keeps the
	// refusal message readable.
	if got, _ := estimateBytes(MaxDurationMS, 0); got <= 0 {
		t.Errorf("estimate at the maximum duration is %d; it has overflowed", got)
	}
}

func TestTheGuardIsOnByDefault(t *testing.T) {
	if got := (Config{}).Normalized().MinFreeBytes; got != DefaultMinFreeBytes {
		t.Errorf("MinFreeBytes = %d, want the %d-byte default; there is no 'off' for a "+
			"reserve that protects the database and the recorder", got, DefaultMinFreeBytes)
	}
}

// ---------------------------------------------------- refusals a retry cannot fix

func TestFailuresARetryCannotFixArePermanent(t *testing.T) {
	dir := t.TempDir()
	writeUpload(t, dir, "clip-1a2b.mp4", "source bytes")
	never := WithExecer(func(context.Context, media.Command, media.Sink) error {
		t.Error("FFmpeg was started for a job that should have been refused")
		return nil
	})

	cases := []struct {
		name string
		p    *Processor
		job  jobs.Job
	}{
		{"an upload name that is a path", newTestProcessor(t, dir, never),
			jobs.Job{Kind: KindNormalise, Params: []byte(`{"upload":"../../etc/passwd"}`)}},
		{"an upload that is not on disk", newTestProcessor(t, dir, never),
			normaliseJob(t, "gone-9999.mp4")},
		{"params that will not parse", newTestProcessor(t, dir, never),
			jobs.Job{Kind: KindNormalise, Params: []byte(`{`)}},
		{"no params at all", newTestProcessor(t, dir, never),
			jobs.Job{Kind: KindNormalise}},
		{"no FFmpeg on the machine", New(nil, Config{DataDir: dir, Uploads: mustStore(t, dir)}, never),
			normaliseJob(t, "clip-1a2b.mp4")},
		{"no upload store", New(nil, Config{FFmpeg: "ffmpeg", FFprobe: "ffprobe", DataDir: dir}, never),
			normaliseJob(t, "clip-1a2b.mp4")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.RunNormalise(context.Background(), tc.job, &recorder{})
			if err == nil {
				t.Fatal("expected a failure")
			}
			if !jobs.IsPermanent(err) {
				t.Errorf("error %v is retryable; it would burn every attempt", err)
			}
		})
	}
}

// ------------------------------------------------------------------- helpers

// The processor resolves upload names through *uploads.Store rather than
// re-deriving the confinement rule that package already owns.
var _ Resolver = (*uploads.Store)(nil)

type recorder struct {
	lines    []string
	result   any
	progress float64
}

func (r *recorder) Progress(f float64)      { r.progress = f }
func (r *recorder) Logf(f string, a ...any) { r.lines = append(r.lines, fmt.Sprintf(f, a...)) }
func (r *recorder) SetResult(v any)         { r.result = v }

func mustStore(t *testing.T, dataDir string) *uploads.Store {
	t.Helper()
	s, err := uploads.New(dataDir)
	if err != nil {
		t.Fatalf("uploads.New: %v", err)
	}
	return s
}

// newTestProcessor is fully wired but touches nothing real unless a test says
// so: no FFmpeg is run, and the disk always looks empty enough.
func newTestProcessor(t *testing.T, dataDir string, opts ...Option) *Processor {
	t.Helper()
	base := []Option{
		WithExecer(func(context.Context, media.Command, media.Sink) error { return nil }),
		WithStreamProber(streams(true, true)),
		WithFreeSpace(func(string) (uint64, error) { return 1 << 60, nil }),
	}
	return New(nil, Config{
		FFmpeg:  "ffmpeg",
		FFprobe: "ffprobe",
		DataDir: dataDir,
		Uploads: mustStore(t, dataDir),
	}, append(base, opts...)...)
}

// streams is a StreamProber for a source with the given tracks.
func streams(video, audio bool) StreamProber {
	return func(_ context.Context, _, kind string) (bool, error) {
		if kind == "v" {
			return video, nil
		}
		return audio, nil
	}
}

func writeUpload(t *testing.T, dataDir, name, content string) string {
	t.Helper()
	path := filepath.Join(dataDir, uploads.Dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func normaliseJob(t *testing.T, upload string) jobs.Job {
	t.Helper()
	job, err := NewNormaliseJob(NormaliseParams{Upload: upload})
	if err != nil {
		t.Fatalf("NewNormaliseJob(%q): %v", upload, err)
	}
	return job
}

// pairValue returns the value following the first occurrence of flag, and how
// many times the flag appears at all.
func pairValue(args []string, flag string) (value string, count int) {
	for i, a := range args {
		if a != flag {
			continue
		}
		count++
		if count == 1 && i+1 < len(args) {
			value = args[i+1]
		}
	}
	return value, count
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func containsSubstring(args []string, want string) bool {
	for _, a := range args {
		if strings.Contains(a, want) {
			return true
		}
	}
	return false
}

// tailFrom joins everything from the first occurrence of marker onward.
func tailFrom(args []string, marker string) string {
	for i, a := range args {
		if a == marker {
			return strings.Join(args[i:], " ")
		}
	}
	return ""
}
