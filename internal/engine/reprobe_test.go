package engine

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// THE TRIGGER MUST BE FALSE FOR EVERY HEALTHY STATE. #674
//
// Two earlier repairs were reverted because their trigger could not tell this
// fault from something working as designed: refusing to start on an idle relay
// broke failover, and restarting anything "receiving but publishing nothing"
// broke a mismatched-publisher step that pins zero restarts. These pin the
// discrimination itself, which is the part that was wrong both times.

func reprobeEngine() *Engine { return &Engine{log: slog.New(slog.DiscardHandler)} }

func TestTheDemuxerGivingUpOnAudioTriggersAReprobe(t *testing.T) {
	for _, line := range []string{
		"[in#0/mpegts @ 0x1] Could not find codec parameters for stream 2 (Audio: aac ([15][0][0][0] / 0x000F), 0 channels): unspecified sample format",
		"[graph_0_in_0:1 @ 0x2] Neither number of channels nor channel layout specified",
	} {
		if !matchesReprobeSignature(line) {
			t.Errorf("did not trigger on the #674 signature:\n  %s\n\n"+
				"This is the demuxer saying it could not determine an audio stream's\n"+
				"parameters. It never re-probes, so the process publishes nothing for its\n"+
				"whole life WITHOUT EXITING -- AutoRestart fires on exit and never sees it.", line)
		}
	}
}

func TestHealthyAndUnrelatedLinesDoNotTriggerAReprobe(t *testing.T) {
	// Each of these is either normal operation or a DIFFERENT fault whose
	// repair is not a restart. Restarting on any of them reintroduces exactly
	// the regressions that got the previous two attempts reverted.
	for _, line := range []string{
		"[out#0/flv @ 0x1] Nothing was written into output file, because at least one of its streams received no packets.",
		"[in#0/mpegts @ 0x1] Could not find codec parameters for stream 0 (Video: h264): unspecified pixel format",
		"frame= 900 fps= 30 q=-1.0 size=  4096KiB time=00:00:30.00 bitrate=1118.5kbits/s speed=1x",
		"[rtmp @ 0x1] Cannot open connection tcp://sink:1936",
		"Non-monotonic DTS; previous: 3601860, current: 3599985; changing to 3601861",
		"[aost#0:1/aac @ 0x1] Task finished with error code: -22 (Invalid argument)",
	} {
		if matchesReprobeSignature(line) {
			t.Errorf("triggered a restart on a line that is not the #674 fault:\n  %s\n\n"+
				"A restart costs a viewer a reconnect and splits a recording across files.\n"+
				"acceptance-failover pins zero restarts across a mismatched cut for that reason.", line)
		}
	}
}

// The video form of the same message must not fire: a late joiner resolves its
// pixel format at the next keyframe without help, and a restart is pure churn.
func TestAVideoProbeFailureIsNotAReprobeTrigger(t *testing.T) {
	line := "[in#0/mpegts @ 0x1] Could not find codec parameters for stream 0 (Video: h264, none): unspecified pixel format"
	if matchesReprobeSignature(line) {
		t.Fatal("restarted on a VIDEO probe failure. A late joiner resolves its pixel " +
			"format at the next keyframe on its own; restarting only throws away the " +
			"progress it had made toward that keyframe.")
	}
}

// The handler must pass every line through, or wiring it in silently kills the
// console's live log panel and the persisted log with it.
func TestTheReprobeHandlerStillForwardsEveryLine(t *testing.T) {
	e := reprobeEngine()
	var mu sync.Mutex
	var seen []string
	h := e.reprobeOnUncharacterisedAudio("d", db.DestRTMP,
		func() *supervisor.Process { return nil },
		func(l supervisor.LogLine) { mu.Lock(); seen = append(seen, l.Text); mu.Unlock() })

	h(supervisor.LogLine{Text: "ordinary line"})
	h(supervisor.LogLine{Text: "Could not find codec parameters for stream 2 (Audio: aac), 0 channels"})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("forwarded %d of 2 lines. OnLog is what feeds the console's live log "+
			"panel and the persisted log; a handler that swallows lines makes the "+
			"operator's only view of a failing child go quiet.", len(seen))
	}
}

// ONE ATTEMPT PER PROBE, NOT ONE PER MESSAGE.
//
// FFmpeg emits the signature once for EVERY audio stream it could not
// characterise -- three at the same millisecond on a three-track ingest.
// Measured on the acceptance rig before this was fixed: attempts 1, 2 and 3 all
// logged at 06:17:54.834, so the whole budget went on a single failed probe and
// the destination never got a second real chance.
func TestSimultaneousSignaturesCountAsOneAttempt(t *testing.T) {
	e := reprobeEngine()
	var reached atomic.Int64
	h := e.reprobeOnUncharacterisedAudio("d", db.DestRTMP,
		func() *supervisor.Process { reached.Add(1); return nil }, nil)

	// What a three-track ingest actually produces, in one burst.
	for _, st := range []string{"1", "2", "3"} {
		h(supervisor.LogLine{Text: "[in#0/mpegts @ 0x1] Could not find codec parameters for stream " +
			st + " (Audio: aac ([15][0][0][0] / 0x000F), 0 channels): unspecified sample format"})
	}
	time.Sleep(50 * time.Millisecond)
	if n := reached.Load(); n != 1 {
		t.Fatalf("one failed probe consumed %d attempts, want 1.\n\n"+
			"Three tracks means three identical messages in the same millisecond. Counting "+
			"messages spends the entire budget on the first probe, so the restart that "+
			"would have worked -- once the relay is carrying audio -- never happens.", n)
	}
}

// Bounded: a permanent fault must not restart for ever.
func TestTheReprobeIsBounded(t *testing.T) {
	e := reprobeEngine()
	var restarts atomic.Int64
	// A nil process short-circuits before Restart, so count trigger arrivals
	// through the forwarded line instead.
	h := e.reprobeOnUncharacterisedAudio("d", db.DestRTMP,
		func() *supervisor.Process { restarts.Add(1); return nil }, nil)

	// Spaced beyond the cooldown so each is a genuine separate attempt. The
	// handler measures real time, so this asserts the CAP, not the spacing:
	// with the cooldown in force, ten bursts still cannot exceed the bound.
	for i := 0; i < 10; i++ {
		h(supervisor.LogLine{Text: "Could not find codec parameters for stream 2 (Audio: aac), 0 channels"})
	}
	time.Sleep(50 * time.Millisecond)
	if n := restarts.Load(); n > int64(maxReprobeRestarts) || n < 1 {
		t.Fatalf("attempted %d re-probes, want between 1 and %d.\n"+
			"Restarting for ever hides a permanent fault behind a process that always "+
			"looks freshly started -- the same invisibility #674 was.", n, maxReprobeRestarts)
	}
}

// THE HANDLER, NOT JUST THE PREDICATE.
//
// TestHealthyAndUnrelatedLinesDoNotTriggerAReprobe calls matchesReprobeSignature
// directly, so deleting the signature check FROM THE HANDLER left it green --
// the handler would then have restarted on every log line the child emitted.
// This drives the handler itself, which is the thing that is wired up.
func TestTheHandlerDoesNotReachForTheProcessOnHealthyLines(t *testing.T) {
	e := reprobeEngine()
	var reached atomic.Int64
	h := e.reprobeOnUncharacterisedAudio("d", db.DestRTMP,
		func() *supervisor.Process { reached.Add(1); return nil }, nil)

	for _, line := range []string{
		"frame= 900 fps= 30 q=-1.0 size=4096KiB time=00:00:30.00 speed=1x",
		"[out#0/flv @ 0x1] Nothing was written into output file, because at least one of its streams received no packets.",
		"[rtmp @ 0x1] Cannot open connection tcp://sink:1936",
		"Non-monotonic DTS; previous: 3601860, current: 3599985",
		"[in#0/mpegts @ 0x1] Could not find codec parameters for stream 0 (Video: h264): unspecified pixel format",
	} {
		h(supervisor.LogLine{Text: line})
	}
	time.Sleep(50 * time.Millisecond)
	if n := reached.Load(); n != 0 {
		t.Fatalf("the handler moved to restart on %d healthy line(s).\n\n"+
			"Every one of those is normal operation or a different fault. Restarting on "+
			"them is exactly what got the previous attempt reverted: acceptance-failover "+
			"pins zero restarts across a mismatched cut, because a restart splits the "+
			"recording across files.", n)
	}
}
