package engine

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A FAILOVER MUST NOT CHANGE THE AUDIO FORMAT A MULTI-TRACK DESTINATION DECODES.
//
// #931 fixed the single-track half of this. When a switch hands a destination
// audio at another sample rate or channel count -- the slate's 48 kHz stereo
// under a 44.1 kHz mono primary, or a backup encoder set up differently from
// the primary -- FFmpeg rebuilds the destination's filter graph from its argv,
// mid-run. A one-track graph now survives that. A MULTI-track one cannot: every
// track's leg ends in routing's trackAlign, `aresample=async=1:first_pts=0`,
// because amix sums by sample count and nothing else gives the legs a common
// origin. A rebuilt leg meets its first frame at T, the stream's runtime, pads
// T seconds of silence in front of it, and the muxer clamps all of it onto the
// current timestamp. The audio goes T seconds behind the video and stays there
// until the destination restarts.
//
// No filter inside the graph can prevent a rebuild: it is decided at the
// destination's INPUT, by the decoded format changing. So the fix is upstream,
// where the format is chosen. The slate is built at the primary's measured rate
// and channel count, and a backup or playlist feed re-encodes its audio to that
// format on the copy hop it already takes into the selector (video still
// copied). Across a switch the destination then decodes one format throughout,
// and the graph is never rebuilt.
//
// These tests drive the production command lines -- relayFeedArgs for the
// primary, conformedFeedArgs for the backup, SlateArgs over the engine's
// slateSpec for the slate -- with only their URLs pointed at files, through the
// production DestinationArgs, against a two-track compiled profile. The
// measurement is #931's: "hidden audio", how much longer the decoded samples
// last than the audio timestamps say.

// primaryMono441 is the primary every test here fails over from: two tracks of
// 44.1 kHz mono, which is neither the slate's default nor the backup's.
var primaryMono441 = []ffmpeg.AudioStream{
	{Index: 0, Codec: "aac", Channels: 1, Layout: "mono", SampleRate: 44100, Bitrate: 96000},
	{Index: 1, Codec: "aac", Channels: 1, Layout: "mono", SampleRate: 44100, Bitrate: 96000},
}

// conformEngine is an engine whose primary has been measured as primaryMono441.
func conformEngine() *Engine {
	e := &Engine{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.commitProbe(routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 1, Codec: "aac", Layout: "mono"},
		{Index: 1, Channels: 1, Codec: "aac", Layout: "mono"},
	}}, &ffmpeg.VideoStream{Width: 160, Height: 120, FrameRate: 15}, primaryMono441, db.IngestRTMP)
	return e
}

// The slate's audio is the primary's track 0, not SlateArgs' 48 kHz stereo
// default. Unmeasured, there is nothing to match and the default stands.
//
// MUTATION: drop the audio lines from slateSpec. Observed: FAIL here, and the
// real-FFmpeg slate case below reads ~40 s hidden.
func TestTheSlateIsBuiltAtThePrimarysMeasuredSampleRateAndChannelCount(t *testing.T) {
	e := conformEngine()
	spec, _ := e.slateSpec(db.DefaultSettings(), "udp://127.0.0.1:21005", 0)
	if spec.SampleRate != 44100 || spec.AudioChannels != 1 {
		t.Errorf("slate audio = %d Hz x %d ch, want the primary's 44100 Hz x 1 ch: a slate at another "+
			"format rebuilds every multi-track destination's graph at the switch", spec.SampleRate, spec.AudioChannels)
	}
	args := ffmpeg.SlateArgs(spec)
	for _, pair := range [][2]string{{"-ar", "44100"}, {"-ac", "1"}} {
		if i := slices.Index(args, pair[0]); i < 0 || args[i+1] != pair[1] {
			t.Errorf("SlateArgs is missing %s %s: %q", pair[0], pair[1], args)
		}
	}

	// A layout that was measured stays measured through the outage -- which is
	// exactly when the slate is built.
	e.clearProbed()
	if spec, _ := e.slateSpec(db.DefaultSettings(), "udp://127.0.0.1:21005", 0); spec.SampleRate != 44100 {
		t.Errorf("after the primary went quiet the slate fell back to %d Hz", spec.SampleRate)
	}

	e.invalidate()
	if spec, _ := e.slateSpec(db.DefaultSettings(), "udp://127.0.0.1:21005", 0); spec.SampleRate != 0 || spec.AudioChannels != 0 {
		t.Errorf("an unmeasured ingest gave the slate %d Hz x %d ch; want SlateArgs' defaults",
			spec.SampleRate, spec.AudioChannels)
	}
}

// A standby feed re-encodes each of the primary's tracks to that track's
// format, keeps the video copied, and with nothing measured is byte-for-byte
// the plain copy hop.
func TestAStandbyFeedReencodesEachTrackToThePrimarysFormatAndStillCopiesTheVideo(t *testing.T) {
	in, out := "udp://127.0.0.1:21001", "udp://127.0.0.1:21002"
	args := conformedFeedArgs(in, out, 12.5, append(primaryMono441,
		ffmpeg.AudioStream{Index: 2, Codec: "aac", Channels: 6, Layout: "5.1", SampleRate: 48000}))

	for _, pair := range [][2]string{
		{"-c", "copy"},
		{"-c:a:0", "aac"}, {"-ar:a:0", "44100"}, {"-ac:a:0", "1"}, {"-b:a:0", "96k"},
		{"-c:a:1", "aac"}, {"-ar:a:1", "44100"}, {"-ac:a:1", "1"},
		// No probed bitrate: a sensible default rather than FFmpeg's.
		{"-c:a:2", "aac"}, {"-ar:a:2", "48000"}, {"-ac:a:2", "6"}, {"-b:a:2", "128k"},
		{"-output_ts_offset", "12.500"},
	} {
		i := slices.Index(args, pair[0])
		if i < 0 || i+1 >= len(args) || args[i+1] != pair[1] {
			t.Errorf("missing %s %s in %q", pair[0], pair[1], args)
		}
	}
	// Per-stream overrides only take effect AFTER the blanket copy.
	if slices.Index(args, "-c:a:0") < slices.Index(args, "-c") {
		t.Errorf("-c:a:0 precedes -c copy and is overridden by it: %q", args)
	}
	if got := args[len(args)-1]; got != ffmpeg.RelayOutputURL(out) {
		t.Errorf("output target = %q, want the selector hub", got)
	}
	if slices.Contains(args, "-c:v") {
		t.Errorf("the standby feed encodes video: %q", args)
	}

	if got, want := conformedFeedArgs(in, out, 12.5, nil), relayFeedArgs(in, out, 12.5); !slices.Equal(got, want) {
		t.Errorf("with no measured primary the standby feed is\n%q\nwant the plain copy hop\n%q", got, want)
	}
}

// The wiring, through startFeed itself: the backup's process is conformed and
// the primary's is the untouched copy hop. Without this the two argv tests
// above would pass against a startFeed that never called conformedFeedArgs.
//
// MUTATION: build the backup with relayFeedArgs in startFeed. Observed: FAIL.
func TestStartFeedConformsTheBackupsAudioAndLeavesThePrimaryACopy(t *testing.T) {
	e := failoverEngine(t)
	s := failoverOnSettings()
	setSettings(e, s)
	e.reconcileSelector(s, wantSelector(s), "")
	if e.selectorHub() == nil {
		t.Fatal("the source selector did not start")
	}
	e.mu.Lock()
	e.commitProbe(e.source, nil, primaryMono441, db.IngestRTMP)
	e.mu.Unlock()
	bh, err := relay.New(e.log, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bh.Close() })
	e.mu.Lock()
	e.backup = &backupIngest{hub: bh}
	e.mu.Unlock()

	for _, tt := range []struct {
		kind    sourceKind
		conform bool
	}{{sourcePrimary, false}, {sourceBackup, true}} {
		e.selMu.Lock()
		f := e.startFeed(s, tt.kind, e.feedUpstreamSig(s, tt.kind, ""), "", time.Now())
		e.selMu.Unlock()
		if f == nil {
			t.Fatalf("no %s feed started", tt.kind)
		}
		args := f.proc.Args()
		e.teardownFeed(f)
		if got := slices.Contains(args, "-ar:a:0"); got != tt.conform {
			t.Errorf("%s feed: conformed = %v, want %v\n%q", tt.kind, got, tt.conform, args)
		}
		if tt.conform {
			if i := slices.Index(args, "-ar:a:0"); args[i+1] != "44100" {
				t.Errorf("%s feed resamples track 0 to %s, want the primary's 44100", tt.kind, args[i+1])
			}
		}
	}
}

// formatFixture writes one feed's worth of ingest: a small video and `tracks`
// AAC tracks of a 440 Hz tone at the given rate and channel count.
func formatFixture(t *testing.T, bin, path string, seconds, tracks, rate, channels int) {
	t.Helper()
	d := strconv.Itoa(seconds)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=15:duration=" + d}
	for i := 0; i < tracks; i++ {
		args = append(args, "-f", "lavfi", "-i",
			"sine=frequency=440:sample_rate="+strconv.Itoa(rate)+":duration="+d)
	}
	args = append(args, "-map", "0:v")
	for i := 0; i < tracks; i++ {
		args = append(args, "-map", strconv.Itoa(i+1)+":a")
	}
	args = append(args,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "15",
		"-c:a", "aac", "-ac", strconv.Itoa(channels), "-f", "mpegts", path)
	if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
		t.Fatalf("build fixture %s: %v\n%s", path, err, out)
	}
}

// runHop runs one production feed command with its input and output pointed at
// files and returns the output's bytes. in == "" leaves the input alone (the
// slate synthesises its own); limit > 0 bounds an input that never ends.
func runHop(t *testing.T, bin string, args []string, in, out string, limit int) []byte {
	t.Helper()
	if in != "" {
		args = swapArg(t, args, "-i", in)
	} else {
		args = append([]string(nil), args...)
	}
	if limit > 0 {
		f := slices.Index(args, "-f")
		for f >= 0 && args[f+1] != "mpegts" {
			n := slices.Index(args[f+1:], "-f")
			if n < 0 {
				f = -1
				break
			}
			f += 1 + n
		}
		if f < 0 {
			t.Fatalf("no -f mpegts on the feed command: %q", args)
		}
		args = slices.Insert(args, f, "-t", strconv.Itoa(limit))
	}
	args[len(args)-1] = out
	if b, err := exec.Command(bin, append([]string{"-y"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("feed %q: %v\n%s", args, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// hiddenSeconds is decoded audio duration minus the span its timestamps cover:
// routing's reinit_drift_test measurement, on the destination's first audio
// stream.
func hiddenSeconds(t *testing.T, ffmpegBin, ffprobeBin, path string) float64 {
	t.Helper()
	pcm, err := exec.Command(ffmpegBin, "-nostdin", "-v", "error", "-i", path,
		"-map", "0:a:0", "-ac", "1", "-ar", "48000", "-f", "s16le", "-").Output()
	if err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	decoded := float64(len(pcm)) / (2 * 48000)
	out, err := exec.Command(ffprobeBin, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "packet=pts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	lines := strings.Fields(string(out))
	if len(lines) < 2 {
		t.Fatalf("%s carries %d audio packets", path, len(lines))
	}
	first, _ := strconv.ParseFloat(strings.TrimSuffix(lines[0], ","), 64)
	last, _ := strconv.ParseFloat(strings.TrimSuffix(lines[len(lines)-1], ","), 64)
	return decoded - (last - first + 1024.0/48000)
}

// The round trip, both standby shapes: 20 s of the two-track 44.1 kHz mono
// primary, a 3 s outage, 10 s of the standby -- a ONE-track 48 kHz stereo
// backup encoder, or the slate -- then the primary back. Routed 0+1, so the
// destination's graph is the multi-track one with trackAlign on each leg.
//
// MUTATION: make conformedFeedArgs return relayFeedArgs unchanged, and drop the
// audio lines from slateSpec -- which is origin/main. Observed: FAIL, backup
// 39.89 s hidden and slate 39.98 s hidden: two rebuilds, onto the standby at
// 23 s and back onto the primary at 33 s, each padding from zero to where it
// starts, less the outage. With the fix: -0.02 s for both.
func TestAMultiTrackDestinationKeepsItsAudioOnTheVideoAcrossAFailoverOntoAStandbyOfAnotherFormat(t *testing.T) {
	ffmpegBin, ffprobeBin := offsetBenchTools(t)
	e := conformEngine()
	primary := e.audioToConformTo()

	src := routing.Source{Tracks: []routing.Track{
		{Index: 0, Channels: 1, Codec: "aac", Layout: "mono"},
		{Index: 1, Channels: 1, Codec: "aac", Layout: "mono"},
	}}
	prof := routing.Profile{Mode: routing.ModeSimple, Normalize: routing.NormOff, SampleRate: 48000,
		Tracks: []routing.TrackSel{{Track: 0, Enabled: true, Gain: 1}, {Track: 1, Enabled: true, Gain: 1}}}
	res, err := routing.Compile(prof, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.FilterComplex, "first_pts=0") {
		t.Fatalf("the compiled graph has no trackAlign leg, so this is not the multi-track case: %s", res.FilterComplex)
	}

	for _, standby := range []string{"backup", "slate"} {
		t.Run(standby, func(t *testing.T) {
			dir := t.TempDir()
			hop := func(name string, args []string, in string, limit int) []byte {
				return runHop(t, ffmpegBin, args, in, filepath.Join(dir, name+".hop.ts"), limit)
			}
			var relay []byte

			p1 := filepath.Join(dir, "primary.ts")
			formatFixture(t, ffmpegBin, p1, 20, 2, 44100, 1)
			relay = append(relay, hop("primary", relayFeedArgs("udp://127.0.0.1:1", "udp://127.0.0.1:2", 0), p1, 0)...)

			switch standby {
			case "backup":
				b := filepath.Join(dir, "backup.ts")
				formatFixture(t, ffmpegBin, b, 10, 1, 48000, 2)
				relay = append(relay, hop("backup",
					conformedFeedArgs("udp://127.0.0.1:1", "udp://127.0.0.1:2", 23, primary), b, 0)...)
			case "slate":
				spec, _ := e.slateSpec(db.DefaultSettings(), "udp://127.0.0.1:2", 23)
				spec.Preset = "ultrafast"
				relay = append(relay, hop("slate", ffmpeg.SlateArgs(spec), "", 10)...)
			}

			p2 := filepath.Join(dir, "primary-again.ts")
			formatFixture(t, ffmpegBin, p2, 10, 2, 44100, 1)
			relay = append(relay, hop("primary-again", relayFeedArgs("udp://127.0.0.1:1", "udp://127.0.0.1:2", 33), p2, 0)...)

			relayPath := filepath.Join(dir, "relay.ts")
			if err := os.WriteFile(relayPath, relay, 0o600); err != nil {
				t.Fatal(err)
			}

			outPath := filepath.Join(dir, "destination.mkv")
			args := ffmpeg.DestinationArgs(ffmpeg.DestSpec{
				Kind: ffmpeg.DestFile, Target: outPath, RelayURL: "udp://127.0.0.1:3",
				FilterComplex: res.FilterComplex, AudioOutLabel: res.OutLabel,
				AudioBitrate: 128, SampleRate: 48000,
			})
			args = swapArg(t, args, "-i", relayPath)
			dest := exec.Command(ffmpegBin, append([]string{"-y"}, args...)...)
			var destErr strings.Builder
			dest.Stderr = &destErr
			if err := dest.Run(); err != nil {
				t.Fatalf("destination: %v\n%s", err, destErr.String())
			}

			h := hiddenSeconds(t, ffmpegBin, ffprobeBin, outPath)
			t.Logf("%s: hidden audio %.3fs", standby, h)
			// #931's bounds. Over 0.1 s is samples squeezed in behind the
			// timestamps -- the fault reads as the 20 s runtime. Under the 3 s
			// outage is audio lost rather than unpadded.
			if h > 0.1 {
				t.Errorf("%.2fs of the multi-track mix is hidden behind its timestamps after a failover onto the %s: "+
					"the switch changed the decoded format, the destination rebuilt its graph, and every "+
					"trackAlign leg padded from zero\ngraph: %s\ndestination stderr:\n%s",
					h, standby, res.FilterComplex, tail(destErr.String(), 20))
			}
			if h < -3.2 {
				t.Errorf("the mix is %.2fs SHORT of its timestamps, more than the 3 s outage", -h)
			}
			aEnd := streamEnd(t, ffprobeBin, outPath, "a")
			vEnd := streamEnd(t, ffprobeBin, outPath, "v")
			if d := vEnd - aEnd; d > 1.5 || d < -1.5 {
				t.Errorf("audio ends at %.2fs and video at %.2fs", aEnd, vEnd)
			}
		})
	}
}
