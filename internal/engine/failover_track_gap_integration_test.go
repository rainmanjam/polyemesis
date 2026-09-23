package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A REAL-LENGTH OUTAGE, THROUGH THE PRODUCTION COMMAND LINES.
//
// routing's track_gap_test proves the destination graph realigns a track that
// goes missing, and it proved it with a five-second gap. In the field the same
// graph failed after a thirty-second one: a 3-track primary routed 0+2, killed
// for ~30 s while a 1-track backup held the air, came back with its track 2
// summed against the backup's track 0 from the OUTAGE, and the routed audio then
// froze while the video ran on.
//
// The graph was never the problem. Before a packet reaches it, the ffmpeg CLI's
// demuxer layer checks every MPEG-TS packet against its own stream's previous
// one, and a jump past -dts_delta_threshold (10 s by default) is taken for a
// broken source: the jump is subtracted from the WHOLE INPUT's offset. When
// track 2 reappears thirty seconds after its last packet, that is exactly what
// it looks like, so every stream -- track 0 and the video included -- is pulled
// back thirty seconds. Track 0's next packet then looks thirty seconds in the
// past, is "corrected" forwards, track 2's looks thirty seconds in the future,
// and the two trade the offset back and forth on every packet. Five seconds is
// under the threshold, which is why the unit test never saw any of it.
//
// So this test builds the relay the way the selector does -- the production
// relayFeedArgs hop, run once per feed with the offsets a switch would give it,
// 3-track primary then a 30 s 1-track backup then the primary again -- and reads
// it with the production DestinationArgs command. Offline, from a file, because
// what is under test is timestamps and a file has the same ones as the wire
// without the pacing that would make this a minute long.
//
// Skipped without FFmpeg and in -short: it encodes video.

// trackGapFixture writes one feed's worth of ingest: a small video plus
// `tracks` silent stereo AAC tracks, with a 0.5 s tone on the last track at
// toneAt seconds when toneAt >= 0.
func trackGapFixture(t *testing.T, bin, path string, seconds, tracks int, toneAt float64) {
	t.Helper()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x120:rate=15:duration=" + strconv.Itoa(seconds)}
	for i := 0; i < tracks; i++ {
		src := "anullsrc=channel_layout=stereo:sample_rate=48000:duration=" + strconv.Itoa(seconds)
		if i == tracks-1 && toneAt >= 0 {
			from := strconv.FormatFloat(toneAt, 'f', 3, 64)
			to := strconv.FormatFloat(toneAt+0.5, 'f', 3, 64)
			tone := "if(between(t," + from + "," + to + "),0.5*sin(2*PI*1000*t),0)"
			src = "aevalsrc='" + tone + "|" + tone + "':s=48000:d=" + strconv.Itoa(seconds)
		}
		args = append(args, "-f", "lavfi", "-i", src)
	}
	args = append(args, "-map", "0:v")
	for i := 0; i < tracks; i++ {
		args = append(args, "-map", strconv.Itoa(i+1)+":a")
	}
	args = append(args,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "15",
		"-c:a", "aac", "-b:a", "96k", "-f", "mpegts", path)
	if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
		t.Fatalf("build fixture %s: %v\n%s", path, err, out)
	}
}

// swapArg replaces the value that follows flag, which must be present.
func swapArg(t *testing.T, args []string, flag, value string) []string {
	t.Helper()
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == flag {
			out[i+1] = value
			return out
		}
	}
	t.Fatalf("%s is not on the command line: %q", flag, args)
	return nil
}

// startTime is a file's container start time in seconds.
func startTime(t *testing.T, ffprobeBin, path string) float64 {
	t.Helper()
	out, err := exec.Command(ffprobeBin, "-v", "error", "-show_entries", "format=start_time",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("start_time of %s: %q: %v", path, out, err)
	}
	return v
}

func TestARoutedTrackRejoinsOnItsOwnTimelineAfterAThirtySecondOutage(t *testing.T) {
	ffmpegBin, ffprobeBin := offsetBenchTools(t)
	dir := t.TempDir()

	// The three feeds, in the order the selector puts them on the relay. The
	// tone is 5 s into the returning primary, on track 2 only.
	const toneAt = 5.0
	feeds := []struct {
		name    string
		seconds int
		tracks  int
		tone    float64
		offset  float64
	}{
		{"primary", 10, 3, -1, 0},
		// Thirty seconds: three times the threshold that decides whether FFmpeg
		// believes the returning track.
		{"backup", 30, 1, -1, 10},
		{"primary-again", 15, 3, toneAt, 40},
	}

	relayPath := filepath.Join(dir, "relay.ts")
	relayFile, err := os.Create(relayPath)
	if err != nil {
		t.Fatal(err)
	}
	var returnStart float64
	for i, f := range feeds {
		fixture := filepath.Join(dir, f.name+".ts")
		trackGapFixture(t, ffmpegBin, fixture, f.seconds, f.tracks, f.tone)

		// The production hop, pointed at files: its input and output URLs are
		// the only two arguments that change.
		hopOut := filepath.Join(dir, f.name+".hop.ts")
		args := relayFeedArgs("udp://127.0.0.1:1", "udp://127.0.0.1:2", f.offset)
		args = swapArg(t, args, "-i", fixture)
		args[len(args)-1] = hopOut
		if out, err := exec.Command(ffmpegBin, append([]string{"-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("relay hop for %s: %v\n%s", f.name, err, out)
		}
		if i == len(feeds)-1 {
			returnStart = startTime(t, ffprobeBin, hopOut)
		}
		// Consecutive feeds writing to one UDP address are, on the wire, one
		// stream of TS packets back to back -- which is what concatenation is.
		b, err := os.ReadFile(hopOut)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := relayFile.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := relayFile.Close(); err != nil {
		t.Fatal(err)
	}
	relayStart := startTime(t, ffprobeBin, relayPath)

	stereo := routing.Track{Channels: 2, Codec: "aac", Layout: "stereo"}
	src := routing.Source{}
	for i := 0; i < 3; i++ {
		tr := stereo
		tr.Index = i
		src.Tracks = append(src.Tracks, tr)
	}
	prof := routing.Profile{Mode: routing.ModeSimple, Normalize: routing.NormOff, SampleRate: 48000,
		Tracks: []routing.TrackSel{
			{Track: 0, Enabled: true, Gain: 1},
			{Track: 1, Enabled: false, Gain: 1},
			{Track: 2, Enabled: true, Gain: 1},
		}}
	res, err := routing.Compile(prof, src)
	if err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(dir, "destination.mkv")
	args := ffmpeg.DestinationArgs(ffmpeg.DestSpec{
		Kind:          ffmpeg.DestFile,
		Target:        outPath,
		RelayURL:      "udp://127.0.0.1:3",
		FilterComplex: res.FilterComplex,
		AudioOutLabel: res.OutLabel,
		AudioBitrate:  128,
		SampleRate:    48000,
	})
	args = swapArg(t, args, "-i", relayPath)
	// -progress would write to our stdout; harmless, but kept out of the log.
	dest := exec.Command(ffmpegBin, append([]string{"-y"}, args...)...)
	var destErr strings.Builder
	dest.Stderr = &destErr
	if err := dest.Run(); err != nil {
		t.Fatalf("destination: %v\n%s", err, destErr.String())
	}

	// Where did the tone land? It belongs where track 2 put it on the relay,
	// measured from the relay's first timestamp, because that is the origin the
	// destination's output timeline starts from.
	want := returnStart + toneAt - relayStart
	out, err := exec.Command(ffmpegBin, "-hide_banner", "-nostdin", "-i", outPath,
		"-map", "0:a:0", "-af", "silencedetect=n=-40dB:d=1", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("silencedetect: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`silence_end: ([0-9.]+)`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("the returning track's tone never reached the destination (want it at %.2fs)\n"+
			"graph: %s\ndestination stderr:\n%s", want, res.FilterComplex, tail(destErr.String(), 40))
	}
	got, _ := strconv.ParseFloat(string(m[1]), 64)
	if got < want-0.75 || got > want+0.75 {
		t.Errorf("track 2's tone reached the destination at %.2fs, want %.2fs: the returning track is "+
			"%.1fs off its own timeline after a 30s outage\ngraph: %s\ndestination stderr:\n%s",
			got, want, want-got, res.FilterComplex, tail(destErr.String(), 40))
	}

	// And the audio did not freeze: it runs to the end of the relay, as the
	// video does.
	aEnd := streamEnd(t, ffprobeBin, outPath, "a")
	vEnd := streamEnd(t, ffprobeBin, outPath, "v")
	if vEnd-aEnd > 1.5 {
		t.Errorf("the destination's audio ends at %.2fs and its video at %.2fs: the routed mix "+
			"stopped %.1fs before the programme did", aEnd, vEnd, vEnd-aEnd)
	}
}

// streamEnd is the last packet timestamp of a file's first stream of kind.
func streamEnd(t *testing.T, ffprobeBin, path, kind string) float64 {
	t.Helper()
	out, err := exec.Command(ffprobeBin, "-v", "error", "-select_streams", kind+":0",
		"-show_entries", "packet=pts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	last := 0.0
	for _, line := range strings.Split(string(out), "\n") {
		if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(line, ",")), 64); err == nil && v > last {
			last = v
		}
	}
	return last
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
