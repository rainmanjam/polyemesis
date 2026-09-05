package engine

import (
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
)

// What -output_ts_offset actually does, and how fast an unpaced copy hop runs.
//
// BOTH REFUTED FIXES FOR #126 RESTED ON THE SAME UNMEASURED ASSUMPTION. The
// comment in ensureFeed argues about whether the incoming feed's offset should
// be taken before or after the teardown, and the argument only means anything if
// everyone agrees what the flag does with the number: whether it SETS where the
// output timeline starts or ADDS to whatever timestamps came in. Nobody in this
// repository has measured it. There is no -copyts anywhere in the tree, so the
// input's own start time is a live variable, and an MPEG-TS input does not start
// at zero -- FFmpeg's muxer conventionally starts it at 1.4 s.
//
// The second question is the leading hypothesis itself. The relay hop has no
// -re, while the slate does (internal/ffmpeg/synth.go, "-re on the audio too").
// A hop that publishes as fast as bytes arrive puts its output timeline ahead of
// the tier clock its offset was computed from, and that lead is exactly what the
// seam ledger's predictedStepMs is trying to catch in the wild.
//
// Measured here rather than argued, offline, against the PRODUCTION command line
// -- relayFeedArgs itself, not a copy of it. A copy would drift from production
// on the first edit and go on answering the old question.
//
// Skipped without FFmpeg and in -short: it binds UDP sockets and encodes video.

func offsetBenchTools(t *testing.T) (ffmpegBin, ffprobeBin string) {
	t.Helper()
	if testing.Short() {
		t.Skip("binds UDP and encodes real video")
	}
	// NOT ON WINDOWS, for two independent reasons and neither of them is
	// squeamishness about the platform. Process.Signal cannot deliver SIGTERM
	// there, so every hop would fall through to the kill path and the clean-exit
	// half of the measurement would be untestable. And the Windows job is already
	// the one at its timeout ceiling -- see #103 -- so adding half a minute of
	// video encoding to it buys a duplicate answer at the cost of the build
	// everyone else is waiting on. What this measures is FFmpeg's behaviour, and
	// FFmpeg is the same binary line on all three.
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is not deliverable on Windows and that job is already at its ceiling (#103)")
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

// offsetBenchFixture writes the stream that gets published into the hub: short,
// cheap, and MPEG-TS so that its start timestamp is the muxer's own rather than
// a zero that would hide the difference between "sets" and "adds".
func offsetBenchFixture(t *testing.T, ffmpegBin, path string) {
	t.Helper()
	cmd := exec.Command(ffmpegBin,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=f=440:r=48000",
		"-t", "6",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-g", "30", "-sc_threshold", "0",
		"-c:a", "aac",
		"-f", "mpegts", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
}

// relayHopResult is one run of the production relay hop.
type relayHopResult struct {
	firstDTS, lastDTS float64
	// outTimeMs is the last figure FFmpeg's own -progress reported, read through
	// the same parser the supervisor uses. The seam ledger builds its prediction
	// out of this number, so the bench has to be able to check it against the
	// timestamps that were actually published.
	outTimeMs    int64
	progressDone bool
	// publishWall is how long the publisher took to push the whole fixture in.
	publishWall time.Duration
	packets     int
}

func (r relayHopResult) span() float64 { return r.lastDTS - r.firstDTS }

// runRelayHop publishes the fixture into a real relay.Hub and runs the
// production relay feed against it at one offset, capturing what the feed
// published.
//
// The OUTPUT side is a plain UDP socket this test owns rather than a second hub.
// A hub would only fan the same datagrams out again; what is under test is what
// FFmpeg puts on the wire, and owning the socket is what makes it readable.
func runRelayHop(t *testing.T, ffmpegBin, fixture string, offset float64, paced bool) relayHopResult {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	hub, err := relay.New(log, 0)
	if err != nil {
		t.Skipf("cannot bind a relay hub here: %v", err)
	}
	defer func() { _ = hub.Close() }()

	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("open the capture socket: %v", err)
	}
	sinkPort := sink.LocalAddr().(*net.UDPAddr).Port

	outPath := filepath.Join(t.TempDir(), "hop.ts")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create %s: %v", outPath, err)
	}
	captured := make(chan int, 1)
	go func() {
		buf := make([]byte, 65536)
		n := 0
		for {
			read, _, err := sink.ReadFrom(buf)
			if read > 0 {
				_, _ = outFile.Write(buf[:read])
				n++
			}
			if err != nil {
				captured <- n
				return
			}
		}
	}()

	// The feed reads the hub on a port of its own, exactly as startFeed gives it
	// one, and writes to the capture socket.
	subPort := freeUDPPort(t)
	args := relayFeedArgs(mustSubscribe(t, hub, "offsetbench", subPort),
		"udp://127.0.0.1:"+strconv.Itoa(sinkPort), offset)
	feed := exec.Command(ffmpegBin, args...)
	var feedErr strings.Builder
	feed.Stderr = &feedErr
	feedOut, err := feed.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := feed.Start(); err != nil {
		t.Fatalf("start the relay feed: %v", err)
	}
	// Read -progress with the SAME parser the supervisor uses, so what the ledger
	// would have recorded is what this bench compares against the wire.
	var progMu sync.Mutex
	var lastProgress ffmpeg.Progress
	progDone := make(chan struct{})
	go func() {
		defer close(progDone)
		_ = ffmpeg.ParseProgress(feedOut, func(p ffmpeg.Progress) {
			progMu.Lock()
			lastProgress = p
			progMu.Unlock()
		})
	}()
	// The feed has to have bound its input socket before the publisher starts,
	// or the head of the stream -- the PAT/PMT everything downstream needs --
	// goes to a closed port.
	time.Sleep(750 * time.Millisecond)

	pubArgs := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if paced {
		pubArgs = append(pubArgs, "-re")
	}
	pubArgs = append(pubArgs, "-i", fixture, "-map", "0", "-c", "copy",
		"-f", "mpegts", "-flush_packets", "1",
		"udp://127.0.0.1:"+strconv.Itoa(hub.Port())+"?pkt_size=1316")
	pub := exec.Command(ffmpegBin, pubArgs...)
	var pubErr strings.Builder
	pub.Stderr = &pubErr
	started := time.Now()
	if err := pub.Run(); err != nil {
		t.Fatalf("publish the fixture: %v\n%s", err, pubErr.String())
	}
	publishWall := time.Since(started)

	// Let the hop finish what is in flight, then take it down the way the engine
	// does -- SIGTERM, not a kill. That is not cosmetic here: on the signal
	// FFmpeg shuts down cleanly and emits its FINAL progress block, which is the
	// very value teardownFeed leaves behind for the ledger to read.
	//
	// The stdout pipe is drained to EOF BEFORE Wait, never after: os/exec closes
	// the pipe when it sees the child exit, so a Wait that lands first truncates
	// the parser's read and takes the final progress block with it. And the
	// SIGTERM gets a deadline of its own, because an FFmpeg blocked on a UDP read
	// with nothing arriving is exactly the wedge stopTimeout exists for.
	time.Sleep(1500 * time.Millisecond)
	_ = feed.Process.Signal(syscall.SIGTERM)
	select {
	case <-progDone:
	// Two seconds rather than the supervisor's own deadline: the measured
	// behaviour is that it does not exit at all on this input, so a longer wait
	// only makes the suite slower. The wait exists so that a future FFmpeg which
	// DOES exit is seen doing it rather than killed before it can.
	case <-time.After(2 * time.Second):
		t.Log("the hop did not exit on SIGTERM within 2s; killing it")
		_ = feed.Process.Kill()
		<-progDone
	}
	_ = feed.Wait()
	_ = sink.Close()
	packets := <-captured
	_ = outFile.Close()

	if packets == 0 {
		t.Fatalf("the relay hop published nothing.\nfeed stderr: %s\npublisher stderr: %s",
			feedErr.String(), pubErr.String())
	}

	progMu.Lock()
	prog := lastProgress
	progMu.Unlock()

	first, last := probeFirstLastDTS(t, outPath)
	return relayHopResult{
		firstDTS: first, lastDTS: last,
		outTimeMs: prog.OutTimeMS, progressDone: prog.Done,
		publishWall: publishWall, packets: packets,
	}
}

func probeFirstLastDTS(t *testing.T, path string) (first, last float64) {
	t.Helper()
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	out, err := exec.Command(ffprobeBin, "-v", "error", "-select_streams", "v",
		"-show_entries", "packet=dts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	have := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if line == "" || line == "N/A" {
			continue
		}
		v, err := strconv.ParseFloat(line, 64)
		if err != nil {
			continue
		}
		if !have {
			first, have = v, true
		}
		last = v
	}
	if !have {
		t.Fatalf("no decode timestamps in %s", path)
	}
	return first, last
}

// TestOutputTSOffsetAddsToTheInputTimelineRatherThanSettingIt is the
// measurement, and it came back the OPPOSITE way round from the name this test
// was first given.
//
// The selector's comments read as though a feed's offset were WHERE ON THE
// TIER'S TIMELINE that feed begins publishing. It is not. Measured against
// FFmpeg 8.1.2, an MPEG-TS fixture whose own first decode timestamp is 1.421s
// came out of the production relay hop at 1.421s with -output_ts_offset 0, and
// at 101.421s with -output_ts_offset 100. The flag ADDS.
//
// The property the tier actually needs survives that: two feeds whose offsets
// differ by N begin N apart on the published timeline. That is the first
// assertion below, and it is the one that would matter if it broke.
//
// THE CONSTANT DOES NOT CANCEL, AND THIS TEST CANNOT SEE THAT. An earlier
// version of this comment said the constant "sits under every feed and CANCELS
// in the difference between two of them", and therefore that the seam ledger's
// predictedStepMs is unharmed by it. It cancels HERE, and only here, because
// both hops below read the SAME fixture from its HEAD. Production feeds do not:
// each joins a live hub at its own moment, and a slate joins no hub at all.
// Measured against a running hub in
// TestProgressOutTimeCountsMediaProducedNotPositionOnThePublishedTimeline and
// the bench behind it, the constant was 1.421s for a hop reading from the head,
// 2.304 / 2.464 / 2.304 / 2.485 for hops joining at +4s, +8s, +12s and +16s, and
// 1.333 for a SlateArgs feed -- a spread of 1.15s that the ledger's prediction
// drops, under an acceptance detector whose threshold is 1ms. See #126.
//
// The second assertion pins the other half of the ledger's formula: FFmpeg's own
// -progress out_time is measured WITHOUT the offset added. What that makes
// `out.offset + out_time` is the switch DECISION's tier stamp plus the media the
// feed has produced -- not the feed's last published timestamp, which is that
// sum plus the constant above.
func TestOutputTSOffsetAddsToTheInputTimelineRatherThanSettingIt(t *testing.T) {
	ffmpegBin, _ := offsetBenchTools(t)

	fixture := filepath.Join(t.TempDir(), "fixture.ts")
	offsetBenchFixture(t, ffmpegBin, fixture)
	inFirst, inLast := probeFirstLastDTS(t, fixture)
	t.Logf("the fixture itself starts at %.3fs and ends at %.3fs", inFirst, inLast)

	const offsetA, offsetB = 0.0, 100.0
	a := runRelayHop(t, ffmpegBin, fixture, offsetA, true)
	b := runRelayHop(t, ffmpegBin, fixture, offsetB, true)
	for _, r := range []struct {
		offset float64
		res    relayHopResult
	}{{offsetA, a}, {offsetB, b}} {
		t.Logf("offset %.0f -> first dts %.3f, last %.3f, progress out_time %.3fs (done=%v, %d datagrams)",
			r.offset, r.res.firstDTS, r.res.lastDTS, float64(r.res.outTimeMs)/1000,
			r.res.progressDone, r.res.packets)
	}

	// THE ARITHMETIC THE TIER RUNS ON, and the only part of this whose failure
	// would invalidate the whole timeline arrangement.
	if step := b.firstDTS - a.firstDTS; step < offsetB-offsetA-0.25 || step > offsetB-offsetA+0.25 {
		t.Errorf("raising the offset by %.0fs moved the output start by %.3fs; a switch is only "+
			"a forward step on a shared timeline if these are the same number",
			offsetB-offsetA, step)
	}

	// ADDS, NOT SETS. Pinned so that an FFmpeg upgrade which changes it fails
	// here, in a test that says what the difference means, rather than surfacing
	// months later as an unexplained seam.
	if a.firstDTS < inFirst-0.25 || a.firstDTS > inFirst+0.25 {
		t.Errorf("at offset 0 the hop started at %.3fs and the fixture starts at %.3fs. "+
			"-output_ts_offset used to ADD to the timestamps it was handed; if it now SETS the "+
			"start, the constant that cancels between two feeds is gone and every comment in "+
			"selector.go about the offset needs rereading", a.firstDTS, inFirst)
	}

	// THE OTHER HALF OF predictedStepMs: out_time excludes the offset. If it did
	// not, the ledger would be adding the offset in twice.
	outTimeA := float64(a.outTimeMs) / 1000
	if outTimeA <= 0 {
		t.Fatal("the hop reported no out_time at all, so there is nothing for the ledger to read")
	}
	if outTimeB := float64(b.outTimeMs) / 1000; outTimeB > offsetB/2 {
		t.Errorf("progress out_time was %.3fs at offset %.0f, so it INCLUDES the offset. "+
			"The seam ledger adds the two together and would be double-counting", outTimeB, offsetB)
	}

	// AND NOW THE LEDGER'S OWN FORMULA, END TO END, against timestamps that were
	// really published.
	//
	// Treat the two hops as one seam: the offset-0 run is the outgoing feed and
	// the offset-100 run is the incoming one. The step a destination would see is
	// the outgoing feed's last timestamp minus the incoming feed's first, and
	// logSeam predicts it as (out.offset + out_time) - in.offset -- WITHOUT the
	// constant, which appears in both absolute positions and cancels here. This
	// is the assertion that says the constant really does cancel, rather than the
	// comment above merely claiming it does.
	//
	// Half a second of slack: out_time is the last packet FFmpeg wrote in ANY
	// stream and the probe reads video only.
	actualStepMs := (a.lastDTS - b.firstDTS) * 1000
	predictedStepMs := (offsetA + outTimeA - offsetB) * 1000
	t.Logf("treated as one seam: actual step %.1fms, ledger prediction %.1fms", actualStepMs, predictedStepMs)
	if diff := actualStepMs - predictedStepMs; diff < -500 || diff > 500 {
		t.Errorf("the seam ledger would have predicted %.1fms and the published timestamps stepped "+
			"%.1fms; predictedStepMs cannot falsify anything about #126 if it is not this number",
			predictedStepMs, actualStepMs)
	}

	// NOT AN ASSERTION, BECAUSE IT IS TIMING-DEPENDENT -- and it is the finding
	// with the most to say about #126 that this bench turned up.
	//
	// The hop does not answer SIGTERM while it is blocked reading a UDP input
	// that has gone quiet: both runs above had to be killed after five seconds.
	// That is not a lab artefact. A full acceptance run recorded a teardown of
	// 8002ms at the seam where the encoder disappeared, which is the same
	// condition, and it means the tier clock advances by seconds during a switch
	// while the incoming feed's offset was fixed at the decision time. Left as an
	// observation on purpose: this branch is instrumenting #126, not fixing it.
	t.Logf("final progress block present: offset-0 run %v, offset-100 run %v", a.progressDone, b.progressDone)
}

// TestAnUnpacedCopyHopPublishesFasterThanRealtime measures the lead the seam
// ledger's predictedStepMs exists to detect.
//
// relayFeedArgs has no -re. Given bytes faster than realtime it emits them
// faster than realtime, and its output timeline then runs ahead of the tier
// clock that fixed its offset. This does NOT establish that a burst reaches a
// feed in production -- relay.Hub is a pure fanout and queues nothing, so any
// backlog has to come from a socket buffer or FFmpeg's own fifo -- and saying
// so is the point of measuring it here rather than assuming either way.
func TestAnUnpacedCopyHopPublishesFasterThanRealtime(t *testing.T) {
	ffmpegBin, _ := offsetBenchTools(t)

	fixture := filepath.Join(t.TempDir(), "fixture.ts")
	offsetBenchFixture(t, ffmpegBin, fixture)

	fast := runRelayHop(t, ffmpegBin, fixture, 0, false)
	t.Logf("unpaced: %.3fs of media published while the publisher ran for %s (%d datagrams)",
		fast.span(), fast.publishWall.Round(time.Millisecond), fast.packets)

	if fast.span() < 1 {
		t.Fatalf("the hop captured only %.3fs of media; there is nothing to compare", fast.span())
	}
	// The publisher pushes the whole fixture as fast as the socket takes it, and
	// the hop keeps up. If this ever failed -- media taking as long as its own
	// duration to cross an unpaced copy -- the hop would be pacing itself
	// somewhere, and the lead this measures could not exist.
	if fast.publishWall > time.Duration(fast.span()*float64(time.Second)) {
		t.Errorf("%.3fs of media took %s to cross an unpaced copy hop; the hop appears to be "+
			"paced, which contradicts the absence of -re in relayFeedArgs",
			fast.span(), fast.publishWall)
	}
	t.Logf("the unpaced hop ran %.1fx realtime", fast.span()/fast.publishWall.Seconds())
}
