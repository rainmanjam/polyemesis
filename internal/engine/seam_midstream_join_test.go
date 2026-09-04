package engine

import (
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
)

// WHAT out_time IS, AND THEREFORE WHAT THE SEAM LEDGER'S predictedStepMs IS.
//
// The ledger (logSeam, selector.go) predicts the step a destination sees as
// `(out.offset + out_time) - in.offset`, and calls it a prediction of a
// timestamp discontinuity. It is only that if `out.offset + out_time` is the
// outgoing feed's last PUBLISHED timestamp. This test measures the half of that
// claim that can be measured without a failover: what FFmpeg's -progress
// out_time counts.
//
// It counts MEDIA PRODUCED SINCE THE PROCESS'S OWN FIRST OUTPUT. It does not
// count where that media sits on the published timeline. Two consequences, both
// load-bearing for #126:
//
//   - `out.offset + out_time` is the tier stamp taken at the switch DECISION
//     plus the media the feed has produced since it started producing. If the
//     feed began producing late -- and a feed that follows a teardown always
//     does -- that sum is behind the tier clock by the lateness, so
//     predictedStepMs is dominated by MINUS THE OUTGOING FEED'S START LAG. That
//     is a latency, not a timestamp step. It explains, without needing a
//     failure to interpret, why 91% of ledger rows carry a negative prediction
//     while almost none of them sit at a backward step, and why the seam whose
//     predecessor logged an 8002ms teardown carries predictedStepMs = -8586ms.
//   - `out.offset + out_time` is also short of the real published timestamp by
//     the feed's own start constant C (its input timeline's first value, which
//     -output_ts_offset ADDS to). relayfeed_offset_integration_test.go argues
//     that C "sits under every feed and CANCELS in the difference between two of
//     them". Measured on this bench it does NOT, because production feeds join
//     the hub at different points and a slate joins no hub at all: C was 1.421
//     for a hop reading from the head, 2.304 / 2.464 / 2.304 / 2.485 for hops
//     joining the same live hub at +4s, +8s, +12s and +16s, and 1.333 for a
//     SlateArgs feed. A spread of 1.15s under a detector whose threshold is 1ms.
//     Logged below rather than asserted: the number that must not change for the
//     ledger to mean anything is the out_time one, and pinning a spread would
//     pin the defect.
//
// THE ASSERTION IS A DIFFERENCE OF TWO out_time VALUES, not either one against
// a clock. Two hops read the same live hub and are stopped at the same instant,
// one having started six seconds after the other. If out_time counts media
// produced, they differ by those six seconds. If it ever counted position on
// the input's timeline -- which is what -copyts or moving the offset to the
// input side would make it count -- they would be equal, and the ledger would be
// adding two things that are already the same thing. Taking the difference is
// what keeps a slow runner from being mistaken for a signal: both hops pay the
// same platform cost and it cancels.

// joinHop is one feed in the bench: when it joins, what offset startFeed would
// have given it, and what it published.
type joinHop struct {
	name    string
	delay   time.Duration // after the publisher starts
	offset  float64       // what startFeed would have computed as the tier offset
	firstD  float64
	lastD   float64
	lastMS  int64
	runFor  time.Duration
	packets int
}

const (
	joinBenchPublish = 14 * time.Second
	joinBenchStopAt  = 12 * time.Second
	joinBenchStagger = 6 * time.Second
)

func TestProgressOutTimeCountsMediaProducedNotPositionOnThePublishedTimeline(t *testing.T) {
	ffmpegBin, _ := offsetBenchTools(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// A FAILURE rather than a skip. The environmental reasons this test can
	// legitimately decline are all in offsetBenchTools above, which already owns
	// its skip sites; an ephemeral loopback UDP port is not an environment fact,
	// and a skip here would be a free pass added to the census for nothing.
	hub, err := relay.New(log, 0)
	if err != nil {
		t.Fatalf("bind a relay hub on an ephemeral loopback port: %v", err)
	}
	defer func() { _ = hub.Close() }()

	dir := t.TempDir()
	early := &joinHop{name: "early", delay: 250 * time.Millisecond, offset: 0.25}
	late := &joinHop{name: "late", delay: joinBenchStagger, offset: joinBenchStagger.Seconds()}

	// A paced live source: an ingest is what these feeds read, and an ingest
	// does not hand a hop a backlog.
	pub := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=f=440:r=48000",
		"-t", strconv.FormatFloat(joinBenchPublish.Seconds(), 'f', 0, 64),
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-g", "30", "-sc_threshold", "0", "-c:a", "aac",
		"-f", "mpegts", "-flush_packets", "1",
		"udp://127.0.0.1:"+strconv.Itoa(hub.Port())+"?pkt_size=1316")
	var pubErr strings.Builder
	pub.Stderr = &pubErr

	var wg sync.WaitGroup
	origin := time.Now()
	if err := pub.Start(); err != nil {
		t.Fatalf("start the publisher: %v", err)
	}
	defer func() {
		_ = pub.Process.Kill()
		_ = pub.Wait()
	}()

	for _, h := range []*joinHop{early, late} {
		wg.Add(1)
		go func(h *joinHop) {
			defer wg.Done()
			time.Sleep(time.Until(origin.Add(h.delay)))
			runJoinHop(t, ffmpegBin, hub, dir, h, origin)
		}(h)
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	for _, h := range []*joinHop{early, late} {
		t.Logf("hop %s: joined +%.2fs, offset %.3f, ran %s, %d datagrams -> firstDTS %.3f lastDTS %.3f, out_time %.3f",
			h.name, h.delay.Seconds(), h.offset, h.runFor.Round(time.Millisecond), h.packets,
			h.firstD, h.lastD, float64(h.lastMS)/1000)
		// C, the start constant -output_ts_offset was added to. Reported because
		// it is the term the ledger's prediction silently drops, not because
		// anything below depends on its value.
		t.Logf("  hop %s: firstDTS - offset = %.3fs is C, this feed's own start constant", h.name, h.firstD-h.offset)
	}

	// THE MEASUREMENT. Both hops were stopped at the same instant, so anything
	// that scales with the runner cancels here.
	earlyS, lateS := float64(early.lastMS)/1000, float64(late.lastMS)/1000
	gap := earlyS - lateS
	t.Logf("out_time: early %.3fs, late %.3fs, difference %.3fs against a %.3fs stagger",
		earlyS, lateS, gap, joinBenchStagger.Seconds())

	if earlyS <= 0 || lateS <= 0 {
		t.Fatalf("a hop reported no out_time at all (early %.3f, late %.3f); there is nothing to compare", earlyS, lateS)
	}
	if math.Abs(gap-joinBenchStagger.Seconds()) > 1.5 {
		t.Errorf("two hops on the same hub, stopped together, %0.1fs apart in start time, reported "+
			"out_time %.3fs and %.3fs -- a difference of %.3fs. out_time therefore does NOT count the "+
			"media each feed produced; it is tracking position on the input's timeline instead. The seam "+
			"ledger adds out_time to a tier-clock offset (logSeam, selector.go) and that sum only means "+
			"anything while out_time is media produced. Read #126 before changing either.",
			joinBenchStagger.Seconds(), earlyS, lateS, gap)
	}
}

// runJoinHop starts one production relay feed against the hub, lets it run to
// the shared stop instant, takes it down with SIGTERM so its final -progress
// block is emitted, and records what it published.
func runJoinHop(t *testing.T, ffmpegBin string, hub *relay.Hub, dir string, h *joinHop, origin time.Time) {
	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Errorf("open the capture socket for %s: %v", h.name, err)
		return
	}
	sinkPort := sink.LocalAddr().(*net.UDPAddr).Port
	outPath := filepath.Join(dir, h.name+".ts")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Errorf("create %s: %v", outPath, err)
		return
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

	subPort := freeUDPPort(t)
	args := relayFeedArgs(mustSubscribe(t, hub, h.name, subPort),
		"udp://127.0.0.1:"+strconv.Itoa(sinkPort), h.offset)
	feed := exec.Command(ffmpegBin, args...)
	var feedErr strings.Builder
	feed.Stderr = &feedErr
	feedOut, err := feed.StdoutPipe()
	if err != nil {
		t.Errorf("stdout pipe for %s: %v", h.name, err)
		return
	}
	if err := feed.Start(); err != nil {
		t.Errorf("start hop %s: %v", h.name, err)
		return
	}
	hopStart := time.Now()
	// The same parser the supervisor uses, so what the ledger would have read is
	// what this compares.
	var mu sync.Mutex
	var last ffmpeg.Progress
	progDone := make(chan struct{})
	go func() {
		defer close(progDone)
		_ = ffmpeg.ParseProgress(feedOut, func(p ffmpeg.Progress) {
			mu.Lock()
			last = p
			mu.Unlock()
		})
	}()

	time.Sleep(time.Until(origin.Add(joinBenchStopAt)))
	_ = feed.Process.Signal(syscall.SIGTERM)
	select {
	case <-progDone:
	case <-time.After(2 * time.Second):
		// Measured behaviour, not a guess: a copy hop blocked on a UDP read does
		// not answer SIGTERM. The wait exists so an FFmpeg that DOES exit is seen
		// doing it rather than killed before it can.
		_ = feed.Process.Kill()
		<-progDone
	}
	_ = feed.Wait()
	h.runFor = time.Since(hopStart)
	_ = sink.Close()
	h.packets = <-captured
	_ = outFile.Close()
	hub.Unsubscribe(h.name)

	if h.packets == 0 {
		t.Errorf("hop %s published nothing.\nstderr: %s", h.name, feedErr.String())
		return
	}
	h.firstD, h.lastD = probeFirstLastDTS(t, outPath)
	mu.Lock()
	h.lastMS = last.OutTimeMS
	mu.Unlock()
}
