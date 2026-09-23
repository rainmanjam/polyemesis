package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// relayConsumers is every builder in this file that reads the relay with the
// consumer probe budgets, each built to read relay. One list, so a builder
// added later is one line here rather than a copy of the assertion.
func relayConsumers(relay string) []struct {
	name string
	args []string
} {
	rec := RecorderSpec{RelayURL: relay, OutputPattern: "/rec/rec-%Y%m%d-%H%M%S.mkv", SegmentSeconds: 3600}
	return []struct {
		name string
		args []string
	}{
		{"destination", DestinationArgs(DestSpec{RelayURL: relay, Kind: DestFile, Target: "/tmp/x.mkv"})},
		{"recorder", RecorderArgs(rec)},
		{"stem recorder", StemRecorderArgs(StemRecorderSpec{RecorderSpec: rec,
			Stems: []StemSpec{{Track: 0, Path: "/rec/stems/rec-%Y%m%d-%H%M%S-mic.flac"}}})},
		{"preview", PreviewArgs(PreviewSpec{RelayURL: relay})},
		{"meters", MetersArgs(MetersSpec{RelayURL: relay, TrackChannels: []int{2, 2}})},
	}
}

// A RELAY CONSUMER'S PROBE ENDS WHEN THE LAYOUT IS KNOWN, NOT WHEN THE WINDOW
// RUNS OUT.
//
// The ffmpeg CLI sets the MPEG-TS demuxer's scan_all_pmts to 1 unless told
// otherwise, and with it set the demuxer never clears AVFMTCTX_NOHEADER -- the
// flag avformat_find_stream_info reads as "more streams may still appear". So
// "All info found" is never reached and every consumer read the full
// -analyzeduration: 15.4s of dead air at every destination start, measured on
// 8.1.2 and 9.0.1, before a single byte went out.
//
// scan_all_pmts=0 lets the demuxer declare the header complete once every
// program has its PMT, which on the relay -- one program, written by our own
// remux -- is the first PMT. The stream list still comes from that PMT, so no
// track the encoder declared can be missed; a stream whose parameters are not
// yet known still holds the probe open up to the full window, exactly as before.
func TestEveryRelayConsumerEndsItsProbeWhenTheLayoutIsKnown(t *testing.T) {
	for _, tc := range relayConsumers("udp://127.0.0.1:21000") {
		iAt, flagAt := -1, -1
		for i, a := range tc.args {
			if a == "-i" && iAt < 0 {
				iAt = i
			}
			if a == "-scan_all_pmts" && i+1 < len(tc.args) && tc.args[i+1] == "0" && flagAt < 0 {
				flagAt = i
			}
		}
		if flagAt < 0 {
			t.Errorf("%s: no -scan_all_pmts 0. Without it the ffmpeg CLI's own default of 1 "+
				"keeps the demuxer from ever declaring its header complete, so this consumer "+
				"sits out the whole %v probe window at every start: %q",
				tc.name, RelayProbeWindow, tc.args)
			continue
		}
		if flagAt > iAt {
			t.Errorf("%s: -scan_all_pmts appears after -i, where it is an output option "+
				"and FFmpeg refuses the command", tc.name)
		}
	}
}

// EVERY RELAY CONSUMER BELIEVES A FORWARD TIMESTAMP JUMP.
//
// A failover to a source with fewer tracks takes a track off the relay for the
// length of the outage, and it comes back an outage later on the wall-clock
// timeline. Under FFmpeg's default -dts_delta_threshold a 30 s outage made the
// demuxer treat that jump as a discontinuity and shift the other streams back
// by it, and a routed track froze until the destination restarted (see
// RelayInputArgs). Every builder must carry the option before -i, or that
// consumer reads with the default again. They get it from relayInputArgsFor
// today, so this is what notices a builder that stops calling it.
//
// Unlike -scan_all_pmts the option is a global one, so a file input keeps it:
// both input kinds are checked.
func TestEveryRelayConsumerBelievesAForwardTimestampJump(t *testing.T) {
	for _, input := range []string{"udp://127.0.0.1:21000", "/data/in.mkv"} {
		for _, tc := range relayConsumers(input) {
			iAt, flagAt := -1, -1
			for i, a := range tc.args {
				if a == "-i" && iAt < 0 {
					iAt = i
				}
				if a == "-dts_delta_threshold" && i+1 < len(tc.args) &&
					tc.args[i+1] == relayDTSDeltaThreshold && flagAt < 0 {
					flagAt = i
				}
			}
			if flagAt < 0 {
				t.Errorf("%s reading %s: no -dts_delta_threshold %s. With FFmpeg's default, a "+
					"track returning from an outage longer than the threshold shifts the other "+
					"streams back by the gap, and a routed track freezes until a restart: %q",
					tc.name, input, relayDTSDeltaThreshold, tc.args)
				continue
			}
			if flagAt > iAt {
				t.Errorf("%s reading %s: -dts_delta_threshold appears after -i, where it no "+
					"longer applies to the input: %q", tc.name, input, tc.args)
			}
		}
	}
}

// scan_all_pmts belongs to the MPEG-TS demuxer, and the ffmpeg CLI refuses an
// input option no demuxer consumed ("Option scan_all_pmts not found", exit 8).
// A builder handed a file must still build a command that starts -- with the
// two budgets, which every demuxer accepts.
func TestANonRelayInputIsNotGivenTheMPEGTSOption(t *testing.T) {
	args := DestinationArgs(DestSpec{RelayURL: "/data/in.mkv", Kind: DestFile, Target: "/tmp/x.mkv"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-scan_all_pmts") {
		t.Errorf("a file input carries -scan_all_pmts, which its demuxer does not have, so "+
			"FFmpeg refuses the whole command: %q", args)
	}
	if !strings.Contains(joined, "-analyzeduration") || !strings.Contains(joined, "-probesize") {
		t.Errorf("a file input lost the probe budgets along with the MPEG-TS option: %q", args)
	}
	// And the shared slice is not what got trimmed.
	if !slices.Contains(RelayInputArgs(), "-scan_all_pmts") {
		t.Error("trimming the option for one input removed it from RelayInputArgs itself")
	}
}

// ------------------------------------------------------------- live checks

// publishTestSource sends an OBS-shaped MPEG-TS -- H.264 at a fixed GOP plus
// two AAC tracks -- to udp://127.0.0.1:port for seconds, paced in real time.
func publishTestSource(t *testing.T, ctx context.Context, bin string, port, gopFrames, seconds int) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-nostdin", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-g", strconv.Itoa(gopFrames), "-keyint_min", strconv.Itoa(gopFrames), "-sc_threshold", "0",
		"-b:v", "1M", "-c:a", "aac", "-b:a", "96k",
		"-t", strconv.Itoa(seconds),
		"-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", port))
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the publisher: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func freeRelayPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free UDP port: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// probeUntilInput runs a relay consumer with this package's input options and
// returns how long FFmpeg took to print "Input #0" -- the line it writes the
// moment avformat_find_stream_info returns -- and the video stream's line.
func probeUntilInput(t *testing.T, ctx context.Context, bin string, port int) (time.Duration, string) {
	t.Helper()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "info", "-fflags", "+genpts"}
	args = append(args, RelayInputArgs()...)
	args = append(args, "-i", RelayInputURL(fmt.Sprintf("udp://127.0.0.1:%d", port)),
		"-map", "0", "-c", "copy", "-t", "1", "-f", "null", "-")
	cmd := exec.CommandContext(ctx, bin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the consumer: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	var took time.Duration
	var video string
	var tail []string
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		tail = append(tail, line)
		if took == 0 && strings.HasPrefix(line, "Input #0") {
			took = time.Since(start)
		}
		if took != 0 && strings.Contains(line, "Video:") {
			video = line
			break
		}
	}
	if took == 0 || video == "" {
		t.Fatalf("the consumer never finished probing:\n%s", strings.Join(tail, "\n"))
	}
	return took, video
}

// A DESTINATION THAT JOINS A FLOWING STREAM IS ON AIR IN ABOUT ONE GOP, not in
// the probe window. Before scan_all_pmts=0 this measured 15.4s against a 2s GOP
// -- the window, to the tenth -- and the harness that "showed" otherwise
// (docs/investigations/398-e-probe-window.sh) stopped its consumer with SIGTERM
// inside the window, which interrupts the probe and flushes what was buffered.
func TestARelayConsumerStartsWithoutWaitingOutItsProbeWindow(t *testing.T) {
	bin := needFFmpeg(t, "ffmpeg")[0]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	port := freeRelayPort(t)
	publishTestSource(t, ctx, bin, port, 60, 40)
	time.Sleep(2 * time.Second)

	took, video := probeUntilInput(t, ctx, bin, port)
	// One 2s GOP plus scheduling slack, and far from the window. Half the window
	// is the line because the defect was the window itself, 1:1.
	if limit := RelayProbeWindow / 2; took > limit {
		t.Errorf("the consumer took %v to finish probing a stream with a 2s GOP -- over %v, "+
			"which is the probe window being waited out rather than used as a ceiling. "+
			"Every destination start and restart pays this as dead air", took.Round(100*time.Millisecond), limit)
	}
	if !strings.Contains(video, "yuv420p") {
		t.Errorf("the probe ended early without the pixel format, which the muxer needs "+
			"to write a header: %s", video)
	}
}

// AND ENDING EARLY MUST NOT MEAN GIVING UP EARLY. #460/#398: a consumer that joins
// mid-GOP cannot learn the pixel format until the NEXT keyframe, and a probe
// that stopped before it would leave the muxer unable to write a header. Joined
// 2s into an 8s GOP, the probe must wait the ~6s to the keyframe and come back
// with the format -- still inside the window, and still well before it.
func TestALateJoinerStillWaitsForTheKeyframeItNeeds(t *testing.T) {
	bin := needFFmpeg(t, "ffmpeg")[0]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	port := freeRelayPort(t)
	publishTestSource(t, ctx, bin, port, 240, 40)
	time.Sleep(2 * time.Second)

	took, video := probeUntilInput(t, ctx, bin, port)
	if !strings.Contains(video, "yuv420p") || !strings.Contains(video, "640x360") {
		t.Errorf("a late joiner finished probing without the parameters the next "+
			"keyframe carries (after %v): %s", took.Round(100*time.Millisecond), video)
	}
}

// THE FIRST SEGMENT OF A RECORDING SURVIVES.
//
// A recorder is started before the encoder connects, so it is waiting when the
// stream arrives. It used to hold the whole probe window of media -- 15s -- and
// hand it to the segment muxer in one burst. With segmentSeconds=10 the muxer
// then cut segment 1 in the same wall-clock second it opened segment 0, both got
// the same one-second strftime name, and segment 1 overwrote segment 0: the first
// ten seconds of every such recording were gone.
func TestARecorderStartedBeforeThePublishKeepsItsFirstSegment(t *testing.T) {
	bins := needFFmpeg(t, "ffmpeg", "ffprobe")
	bin, probe := bins[0], bins[1]
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	port := freeRelayPort(t)
	dir := t.TempDir()
	args := RecorderArgs(RecorderSpec{
		RelayURL:       fmt.Sprintf("udp://127.0.0.1:%d", port),
		OutputPattern:  filepath.Join(dir, "rec-%Y%m%d-%H%M%S.mkv"),
		SegmentSeconds: 10,
	})
	// Stopped with ffmpeg's own "q" on stdin rather than SIGINT, which Windows
	// does not deliver to a child (os.Interrupt is refused there), so this test
	// runs on every platform CI covers. What it proves is the first segment's
	// survival, not how a recorder is stopped, so it drops -nostdin -- which
	// production keeps -- purely to have a portable way to end it cleanly.
	var recArgs []string
	for _, a := range args {
		if a != "-nostdin" {
			recArgs = append(recArgs, a)
		}
	}
	rec := exec.CommandContext(ctx, bin, recArgs...)
	quit, err := rec.StdinPipe()
	if err != nil {
		t.Fatalf("recorder stdin: %v", err)
	}
	if err := rec.Start(); err != nil {
		t.Fatalf("starting the recorder: %v", err)
	}
	time.Sleep(time.Second)

	// The recorder is stopped while the publish is still flowing, because a read
	// on a UDP socket nothing is sending to does not reliably notice SIGINT, and
	// a killed recorder leaves its open segment without a duration to measure.
	const recorded = 24
	publishTestSource(t, ctx, bin, port, 60, recorded+10)
	time.Sleep(recorded * time.Second)
	_, _ = quit.Write([]byte("q"))
	_ = quit.Close()
	done := make(chan struct{})
	go func() { _ = rec.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = rec.Process.Kill()
		<-done
		t.Fatal("the recorder did not stop on q, so its last segment cannot be measured")
	}

	files, _ := filepath.Glob(filepath.Join(dir, "rec-*.mkv"))
	var total float64
	for _, f := range files {
		out, err := exec.Command(probe, "-v", "error", "-show_entries", "format=duration",
			"-of", "csv=p=0", f).Output()
		if err != nil {
			continue
		}
		d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
		t.Logf("%s: %.2fs", filepath.Base(f), d)
		total += d
	}
	// A GOP of slack for the cut points, and nothing like the ten seconds a lost
	// segment costs.
	if total < recorded-3 {
		t.Errorf("%d segment file(s) hold %.1fs of a %ds recording: %.1fs are missing. Two "+
			"segments opened in the same wall-clock second share a strftime name, and the "+
			"second overwrote the first", len(files), total, recorded, recorded-total)
	}
}
