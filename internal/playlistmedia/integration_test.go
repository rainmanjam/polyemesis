package playlistmedia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The unit tests prove the argv says what we meant. This file proves the argv
// MEANS what we think it means to a real FFmpeg, which is a different claim and
// the only one that actually protects the product: a profile that matches on
// paper and disagrees on sample aspect ratio, frame rate or channel count
// passes every string comparison and fails at the join, on air.
//
// Skipped without FFmpeg, and in -short: it encodes real files.

func tools(t *testing.T) (ffmpegBin, ffprobeBin string) {
	t.Helper()
	if testing.Short() {
		t.Skip("encodes real files")
	}
	var err error
	if ffmpegBin, err = exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if ffprobeBin, err = exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	return ffmpegBin, ffprobeBin
}

func runFFmpeg(t *testing.T, bin string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s\nfailed: %v\n%s", bin, strings.Join(args, " "), err, stderr.String())
	}
}

// probeStream is the part of ffprobe's answer that the concat demuxer cares
// about. Every field here is a way for two items to be incompatible.
type probeStream struct {
	CodecName     string `json:"codec_name"`
	CodecType     string `json:"codec_type"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	PixFmt        string `json:"pix_fmt"`
	SampleAspect  string `json:"sample_aspect_ratio"`
	AvgFrameRate  string `json:"avg_frame_rate"`
	TimeBase      string `json:"time_base"`
	Channels      int    `json:"channels"`
	ChannelLayout string `json:"channel_layout"`
	SampleRate    string `json:"sample_rate"`
}

type probeResult struct {
	Streams []probeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func probeFile(t *testing.T, ffprobeBin, path string) probeResult {
	t.Helper()
	out, err := exec.Command(ffprobeBin, "-hide_banner", "-v", "error",
		"-print_format", "json", "-show_streams", "-show_format", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var res probeResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("parsing ffprobe output for %s: %v", path, err)
	}
	return res
}

func (r probeResult) stream(kind string) probeStream {
	for _, s := range r.Streams {
		if s.CodecType == kind {
			return s
		}
	}
	return probeStream{}
}

// synthesiseSources writes two uploads shaped like the mismatch that makes this
// package necessary: one 4:3 25 fps clip with a mono 44.1 kHz track, and one
// widescreen 24 fps clip with NO audio at all.
func synthesiseSources(t *testing.T, ffmpegBin, dataDir string) (withAudio, silent string) {
	t.Helper()
	dir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	withAudio, silent = "talk-1a2b3c4d.mp4", "slate-5e6f7a8b.mp4"

	runFFmpeg(t, ffmpegBin, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x480:rate=25:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=3",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "1", "-ar", "44100",
		filepath.Join(dir, withAudio))

	runFFmpeg(t, ffmpegBin, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=2",
		"-map", "0:v", "-an",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		filepath.Join(dir, silent))

	return withAudio, silent
}

// TestNormalisedItemsAgreeWithEachOtherAndReallyConcatenate is the evidence the
// argv tests cannot give: two deliberately incompatible uploads go in, and what
// comes out is two files a real concat demuxer will splice with -c copy.
func TestNormalisedItemsAgreeWithEachOtherAndReallyConcatenate(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dataDir := t.TempDir()
	withAudio, silent := synthesiseSources(t, ffmpegBin, dataDir)

	// The premise: before normalisation these two disagree on everything that
	// matters. If this ever stops being true the test below proves nothing.
	srcA := probeFile(t, ffprobeBin, filepath.Join(dataDir, "uploads", withAudio))
	srcB := probeFile(t, ffprobeBin, filepath.Join(dataDir, "uploads", silent))
	if srcA.stream("video").Width == srcB.stream("video").Width {
		t.Fatal("the two sources were supposed to disagree on resolution")
	}
	if srcB.stream("audio").CodecType != "" {
		t.Fatal("the silent source was supposed to have no audio track")
	}

	p := New(nil, Config{
		FFmpeg:  ffmpegBin,
		FFprobe: ffprobeBin,
		DataDir: dataDir,
		Uploads: mustStore(t, dataDir),
	},
		// The free-space guard is exercised in the unit tests; here it would
		// only make the result depend on how full the machine happens to be.
		WithFreeSpace(func(string) (uint64, error) { return 1 << 60, nil }))

	for _, upload := range []string{withAudio, silent} {
		rep := &recorder{}
		if err := p.RunNormalise(context.Background(), normaliseJob(t, upload), rep); err != nil {
			t.Fatalf("normalising %s: %v\n%s", upload, err, strings.Join(rep.lines, "\n"))
		}
	}

	// Every item, including the one whose source had no audio at all, carries
	// the full profile.
	var derivatives []string
	for _, upload := range []string{withAudio, silent} {
		path := DerivativePath(dataDir, upload)
		derivatives = append(derivatives, path)
		got := probeFile(t, ffprobeBin, path)
		v, a := got.stream("video"), got.stream("audio")
		for _, c := range []struct {
			what      string
			got, want any
		}{
			{"video codec", v.CodecName, "h264"},
			{"width", v.Width, NormaliseWidth},
			{"height", v.Height, NormaliseHeight},
			{"pixel format", v.PixFmt, NormalisePixFmt},
			{"sample aspect ratio", v.SampleAspect, "1:1"},
			{"frame rate", v.AvgFrameRate, strconv.Itoa(NormaliseFPS) + "/1"},
			// MPEG-TS timestamps are 90 kHz by definition, which is what makes
			// the container choice the timebase decision.
			{"video timebase", v.TimeBase, "1/90000"},
			{"audio codec", a.CodecName, "aac"},
			{"channels", a.Channels, NormaliseChannels},
			{"sample rate", a.SampleRate, strconv.Itoa(NormaliseSampleRate)},
		} {
			if fmt.Sprint(c.got) != fmt.Sprint(c.want) {
				t.Errorf("%s: %s is %v, want %v", filepath.Base(path), c.what, c.got, c.want)
			}
		}
	}
	if t.Failed() {
		return
	}

	// And the claim that actually matters: the concat demuxer accepts the set
	// with a stream copy. A mismatch anywhere above shows up here as an error
	// or as a file whose duration is not the sum of its parts.
	list := filepath.Join(t.TempDir(), "playlist.txt")
	var b strings.Builder
	for _, d := range derivatives {
		fmt.Fprintf(&b, "file '%s'\n", d)
	}
	if err := os.WriteFile(list, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	joined := filepath.Join(t.TempDir(), "joined.ts")
	runFFmpeg(t, ffmpegBin, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "concat", "-safe", "0", "-i", list, "-c", "copy", "-f", "mpegts", joined)

	out := probeFile(t, ffprobeBin, joined)
	secs, err := strconv.ParseFloat(out.Format.Duration, 64)
	if err != nil {
		t.Fatalf("joined file has no duration: %v", err)
	}
	// 3 s + 2 s, with a little slack for the last GOP.
	if secs < 4.5 || secs > 5.6 {
		t.Errorf("joined playlist is %.2fs, want about 5s — the items did not both play", secs)
	}
	if got := out.stream("video"); got.Width != NormaliseWidth || got.Height != NormaliseHeight {
		t.Errorf("joined playlist is %dx%d", got.Width, got.Height)
	}
	if got := out.stream("audio"); got.Channels != NormaliseChannels {
		t.Errorf("joined playlist has %d audio channels, want %d", got.Channels, NormaliseChannels)
	}
}

// The audio-stream probe decides which of the two argv builders runs, so it is
// worth proving against real files rather than only against a fake.
func TestTheAudioProbeAnswersForRealFiles(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dataDir := t.TempDir()
	withAudio, silent := synthesiseSources(t, ffmpegBin, dataDir)

	p := New(nil, Config{FFmpeg: ffmpegBin, FFprobe: ffprobeBin, DataDir: dataDir})
	for _, tc := range []struct {
		name string
		want bool
	}{{withAudio, true}, {silent, false}} {
		got, err := p.probeAudio(context.Background(), filepath.Join(dataDir, "uploads", tc.name))
		if err != nil {
			t.Fatalf("probing %s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("probeAudio(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
