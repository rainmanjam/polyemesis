package clipper

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The unit tests above prove the arithmetic against synthetic keyframe indexes.
// This file proves the assumptions underneath that arithmetic against a real
// FFmpeg: that a packet-level probe really does report the keyframes of a file
// with a known GOP, that a stream copy really does start where we said it
// would, that a re-encoded head really does splice onto a copied tail, and that
// a clip spanning two files really is continuous.
//
// If any of those stops holding, every clip this product ships is either short,
// long, or grey for the first second — and nothing else in the suite would
// notice.
//
// Skipped without FFmpeg, and in -short: it encodes real video.

// synthGOP is the fixed GOP the fixtures below are built with. One second at
// 30fps, with scene detection disabled so the keyframes land on exact seconds
// and the expected answers are arithmetic rather than a guess.
const synthGOP = time.Second

func integrationTools(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	if testing.Short() {
		t.Skip("encodes real video")
	}
	var err error
	if ffmpeg, err = exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if ffprobe, err = exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	return ffmpeg, ffprobe
}

// synthSegment writes one fixture: ten seconds of video with a one-second GOP
// and TWO audio tracks, because a clipper that loses tracks off a multitrack
// master has failed at the only thing that makes this product different.
func synthSegment(t *testing.T, ffmpeg, path string, seconds int) {
	t.Helper()
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=f=440:r=48000",
		"-f", "lavfi", "-i", "sine=f=880:r=48000",
		"-t", strconv.Itoa(seconds),
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		// A fixed one-second GOP: no scene-cut keyframes, no adaptive interval.
		"-g", "30", "-keyint_min", "30", "-sc_threshold", "0",
		"-c:a", "aac",
		"-f", "matroska", path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not produce a fixture (%v): %s", err, out)
	}
}

func TestARealProbeFindsTheKeyframesOfAKnownGOP(t *testing.T) {
	ffmpeg, ffprobe := integrationTools(t)
	path := filepath.Join(t.TempDir(), "seg0.mkv")
	synthSegment(t, ffmpeg, path, 10)

	kf, err := FFprobe{Bin: ffprobe}.Keyframes(context.Background(), path, 0, 0)
	if err != nil {
		t.Fatalf("Keyframes: %v", err)
	}
	if kf.Len() < 9 {
		t.Fatalf("found %d keyframes in ten seconds of one-second GOP: %v", kf.Len(), kf.Times())
	}
	for i, at := range kf.Times() {
		want := time.Duration(i) * synthGOP
		if diff := at - want; diff > 40*time.Millisecond || diff < -40*time.Millisecond {
			t.Fatalf("keyframe %d at %s, want about %s (all: %v)", i, at, want, kf.Times())
		}
	}
}

// A fast cut snaps back to a keyframe, and the drift it reports has to be the
// drift the file actually has. A plan that says "0.4s earlier" and produces a
// clip 0.9s long is worse than one that says nothing.
func TestARealFastCutStartsWhereThePlanSaidAndDecodes(t *testing.T) {
	ffmpeg, ffprobe := integrationTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "seg0.mkv")
	synthSegment(t, ffmpeg, src, 10)

	out := filepath.Join(dir, "fast.mkv")
	c := New(testLog(), ffmpeg, ffprobe)
	tl, err := NewTimeline([]Segment{{Path: src, Duration: 10 * time.Second}})
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}

	res, err := c.Cut(context.Background(), tl, Request{
		In: 3400 * time.Millisecond, Out: 7400 * time.Millisecond, OutPath: out,
	}, nil)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if res.Plan.In != 3*time.Second {
		t.Errorf("snapped to %s, want the keyframe at 3s", res.Plan.In)
	}
	if res.Plan.InDrift != -400*time.Millisecond {
		t.Errorf("reported drift %s, want -400ms", res.Plan.InDrift)
	}
	assertDuration(t, ffprobe, out, 4400*time.Millisecond)
	assertAudioTracks(t, ffprobe, out, 2)
	assertFirstFrameDecodes(t, ffmpeg, out)
}

// Precise re-encodes only the leading partial GOP and copies the rest, so the
// clip is exactly as long as it was asked to be — and the seam between the two
// halves has to be a seam a decoder never notices.
func TestARealPreciseCutHitsTheRequestedInPointAndDecodes(t *testing.T) {
	ffmpeg, ffprobe := integrationTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "seg0.mkv")
	synthSegment(t, ffmpeg, src, 10)

	out := filepath.Join(dir, "precise.mkv")
	c := New(testLog(), ffmpeg, ffprobe)
	tl, err := NewTimeline([]Segment{{Path: src, Duration: 10 * time.Second}})
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}

	res, err := c.Cut(context.Background(), tl, Request{
		In: 3400 * time.Millisecond, Out: 7400 * time.Millisecond,
		Mode: ModePrecise, OutPath: out,
	}, nil)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if res.Plan.InDrift != 0 {
		t.Errorf("a precise cut drifted by %s", res.Plan.InDrift)
	}
	if res.Plan.HeadDuration != 600*time.Millisecond {
		t.Errorf("re-encoded %s, want the 600ms in front of the 4s keyframe", res.Plan.HeadDuration)
	}
	// Only a fraction of a four-second clip paid for an encode. That ratio is
	// the entire argument for offering the mode.
	if got := res.Plan.LosslessFraction(); got < 0.8 {
		t.Errorf("lossless fraction = %v, want most of the clip copied", got)
	}
	assertDuration(t, ffprobe, out, 4*time.Second)
	assertFirstFrameDecodes(t, ffmpeg, out)
	assertDecodesThroughout(t, ffmpeg, out)
}

// The recorder writes hourly files. A clip across the boundary is concatenated
// first, and the result has to be one continuous piece rather than two.
func TestARealCutSpanningTwoSegmentsIsContinuous(t *testing.T) {
	ffmpeg, ffprobe := integrationTools(t)
	dir := t.TempDir()
	first := filepath.Join(dir, "seg0.mkv")
	second := filepath.Join(dir, "seg1.mkv")
	synthSegment(t, ffmpeg, first, 10)
	synthSegment(t, ffmpeg, second, 10)

	out := filepath.Join(dir, "spanning.mkv")
	c := New(testLog(), ffmpeg, ffprobe)
	tl, err := NewTimeline([]Segment{
		{Path: first, Start: 0, Duration: 10 * time.Second},
		{Path: second, Start: 10 * time.Second, Duration: 10 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}

	res, err := c.Cut(context.Background(), tl, Request{
		In: 9 * time.Second, Out: 12 * time.Second, OutPath: out,
	}, nil)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if len(res.Plan.Sources) != 2 || !res.Plan.Concat {
		t.Fatalf("the cut read %d files, concat=%v", len(res.Plan.Sources), res.Plan.Concat)
	}
	assertDuration(t, ffprobe, out, 3*time.Second)
	assertAudioTracks(t, ffprobe, out, 2)
	assertDecodesThroughout(t, ffmpeg, out)
}

// The hardest combination there is: an exact in-point inside the first file, a
// re-encoded head, a copied tail that starts in the first file and ends in the
// second, and a join over all of it. Every seek in this one is on the output
// side, because the concat demuxer cannot seek.
func TestARealPreciseCutSpanningTwoSegments(t *testing.T) {
	ffmpeg, ffprobe := integrationTools(t)
	dir := t.TempDir()
	first := filepath.Join(dir, "seg0.mkv")
	second := filepath.Join(dir, "seg1.mkv")
	synthSegment(t, ffmpeg, first, 10)
	synthSegment(t, ffmpeg, second, 10)

	out := filepath.Join(dir, "spanning-precise.mkv")
	c := New(testLog(), ffmpeg, ffprobe)
	tl, err := NewTimeline([]Segment{
		{Path: first, Start: 0, Duration: 10 * time.Second},
		{Path: second, Start: 10 * time.Second, Duration: 10 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}

	res, err := c.Cut(context.Background(), tl, Request{
		In: 8400 * time.Millisecond, Out: 11400 * time.Millisecond,
		Mode: ModePrecise, OutPath: out,
	}, nil)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	if !res.Plan.Concat || res.Plan.InDrift != 0 {
		t.Fatalf("concat=%v drift=%s, want a concat with no drift", res.Plan.Concat, res.Plan.InDrift)
	}
	if res.Plan.HeadDuration != 600*time.Millisecond {
		t.Errorf("re-encoded %s, want 600ms", res.Plan.HeadDuration)
	}
	assertDuration(t, ffprobe, out, 3*time.Second)
	assertAudioTracks(t, ffprobe, out, 2)
	assertDecodesThroughout(t, ffmpeg, out)
}

// "A clip of a multitrack master should be able to be just the mic."
func TestARealCutCanKeepOneTrackOutOfSeveral(t *testing.T) {
	ffmpeg, ffprobe := integrationTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "seg0.mkv")
	synthSegment(t, ffmpeg, src, 10)

	out := filepath.Join(dir, "mic.mkv")
	c := New(testLog(), ffmpeg, ffprobe)
	tl, err := NewTimeline([]Segment{{Path: src, Duration: 10 * time.Second}})
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}

	if _, err := c.Cut(context.Background(), tl, Request{
		In: 2 * time.Second, Out: 5 * time.Second, OutPath: out,
		Audio: AudioSelection{Mode: AudioTracks, Tracks: []int{1}},
	}, nil); err != nil {
		t.Fatalf("Cut: %v", err)
	}
	assertAudioTracks(t, ffprobe, out, 1)
	assertFirstFrameDecodes(t, ffmpeg, out)
}

func assertDuration(t *testing.T, ffprobe, path string, want time.Duration) {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe rejected the clip: %v", err)
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("unreadable duration %q: %v", out, err)
	}
	got := time.Duration(secs * float64(time.Second))
	// A frame and a half of slack: a container rounds, and an audio track can
	// run a packet past the last video frame.
	if diff := got - want; diff > 120*time.Millisecond || diff < -120*time.Millisecond {
		t.Fatalf("the clip runs %s, want about %s", got, want)
	}
}

func assertAudioTracks(t *testing.T, ffprobe, path string, want int) {
	t.Helper()
	out, err := exec.Command(ffprobe, "-v", "error",
		"-select_streams", "a", "-show_entries", "stream=index", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe rejected the clip: %v", err)
	}
	got := len(strings.Fields(strings.TrimSpace(string(out))))
	if got != want {
		t.Fatalf("the clip carries %d audio tracks, want %d", got, want)
	}
}

// A cut that lands mid-GOP still probes fine; it is the decode that gives it
// away.
func assertFirstFrameDecodes(t *testing.T, ffmpeg, path string) {
	t.Helper()
	cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "error",
		"-i", path, "-frames:v", "1", "-f", "rawvideo", "-y", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the first frame of the clip did not decode: %v\n%s", err, truncate(string(out), 400))
	}
}

// The seam between a re-encoded head and a copied tail is where a smart cut
// goes wrong, and it is invisible until something decodes past it.
func assertDecodesThroughout(t *testing.T, ffmpeg, path string) {
	t.Helper()
	cmd := exec.Command(ffmpeg, "-hide_banner", "-v", "error",
		"-xerror", "-i", path, "-f", "null", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the clip did not decode end to end: %v\n%s", err, truncate(string(out), 600))
	}
}
