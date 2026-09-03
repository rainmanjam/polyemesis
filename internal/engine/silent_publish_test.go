package engine

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// THE WATCHDOG FOR #674.
//
// A destination that starts while the relay carries video but no audio yet
// characterises no audio stream, and FFmpeg never re-probes. It then runs for
// ever publishing nothing WITHOUT EXITING, so AutoRestart -- which fires on
// exit -- never sees it. These tests pin the three decisions that separate
// "wedged" from "idle", because getting that wrong makes the guard worse than
// the bug: restarting idle destinations burns MaxRestarts and then gives up
// permanently on an install whose only fault was that nobody was streaming.

func testEngine() *Engine {
	return &Engine{log: slog.New(slog.DiscardHandler)}
}

// wedged: the relay is receiving and the destination publishes nothing.
func TestASilentDestinationOnAFedRelayIsRestarted(t *testing.T) {
	e := testEngine()
	var restarts atomic.Int64
	done := make(chan struct{})
	var once sync.Once

	go e.watchSilentPublish("d", db.DestRTMP, time.Millisecond, &destWatch{}, silentPublishDeps{
		retired: func() bool { return false },
		rxBytes: func() uint64 { return 4096 },
		restart: func() {
			if restarts.Add(1) >= 1 {
				once.Do(func() { close(done) })
			}
		},
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a destination that published nothing while the relay was receiving was never restarted;\n" +
			"that is #674 exactly: it does not exit, so the supervisor never restarts it, and it\n" +
			"publishes nothing for ever with nothing in the log saying so")
	}
}

// idle: nothing is arriving, so there is nothing to re-probe.
func TestAnIdleDestinationIsLeftAlone(t *testing.T) {
	e := testEngine()
	var restarts atomic.Int64

	go e.watchSilentPublish("d", db.DestRTMP, time.Millisecond, &destWatch{}, silentPublishDeps{
		retired: func() bool { return false },
		rxBytes: func() uint64 { return 0 }, // no ingest at all
		restart: func() { restarts.Add(1) },
	})

	time.Sleep(200 * time.Millisecond)
	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarted an idle destination %d times.\n"+
			"Nothing is reaching the relay, so this destination is idle rather than wedged, and\n"+
			"restarting it burns MaxRestarts and then gives up PERMANENTLY -- on an install whose\n"+
			"only fault was that nobody had started streaming yet. That is strictly worse than the\n"+
			"bug this guard exists to fix.", n)
	}
}

// publishing: healthy, and it stays healthy across windows.
func TestAPublishingDestinationIsNeverRestarted(t *testing.T) {
	e := testEngine()
	var restarts atomic.Int64
	w := &destWatch{}

	stop := make(chan struct{})
	defer close(stop)
	go func() { // media keeps moving
		ms := int64(0)
		for {
			select {
			case <-stop:
				return
			default:
			}
			ms += 100
			w.mu.Lock()
			w.last, w.published = ms, true
			w.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	go e.watchSilentPublish("d", db.DestRTMP, 2*time.Millisecond, w, silentPublishDeps{
		retired: func() bool { return false },
		rxBytes: func() uint64 { return 4096 },
		restart: func() { restarts.Add(1) },
	})

	time.Sleep(200 * time.Millisecond)
	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarted a destination %d times while it was publishing", n)
	}
}

// bounded: a permanent fault must not restart for ever.
func TestTheGuardGivesUpAfterMaxSilentRestarts(t *testing.T) {
	e := testEngine()
	var restarts atomic.Int64

	go e.watchSilentPublish("d", db.DestRTMP, time.Millisecond, &destWatch{}, silentPublishDeps{
		retired: func() bool { return false },
		rxBytes: func() uint64 { return 4096 },
		restart: func() { restarts.Add(1) },
	})

	time.Sleep(500 * time.Millisecond)
	if n := restarts.Load(); n != int64(maxSilentRestarts) {
		t.Fatalf("restarted %d times, want exactly %d.\n"+
			"Restarting for ever would hide a permanent fault behind a process that always looks\n"+
			"freshly started, which is the same invisibility #674 was.", n, maxSilentRestarts)
	}
}

// retired: the watchdog must not outlive the process it watches.
func TestTheGuardStandsDownWhenTheProcessRetires(t *testing.T) {
	e := testEngine()
	var restarts atomic.Int64

	go e.watchSilentPublish("d", db.DestRTMP, time.Millisecond, &destWatch{}, silentPublishDeps{
		retired: func() bool { return true }, // stopped for good
		rxBytes: func() uint64 { return 4096 },
		restart: func() { restarts.Add(1) },
	})

	time.Sleep(100 * time.Millisecond)
	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarted a retired destination %d times", n)
	}
}

// the latch itself: OutTimeMS is what "published" means.
func TestTheWatchLatchTracksOutTimeOnly(t *testing.T) {
	e := testEngine()
	onProgress, w := e.firstMediaLoggerWatched("d", db.DestRTMP)

	onProgress(ffmpeg.Progress{Frame: 900, OutTimeMS: 0})
	if w.publishedSinceRearm() {
		t.Fatal("frames alone counted as publishing.\n" +
			"A destination reading video and no audio reports frames while writing NOTHING --\n" +
			"that is the #674 state, and it must not read as healthy.")
	}
	onProgress(ffmpeg.Progress{OutTimeMS: 40})
	if !w.publishedSinceRearm() {
		t.Fatal("OutTimeMS advanced and the latch did not set")
	}
	w.rearm()
	if w.publishedSinceRearm() {
		t.Fatal("rearm did not clear the latch")
	}
}

// THE TWO BUDGETS MUST NOT CROSS.
//
// A destination inside its own probe has published nothing YET. If the
// watchdog's budget ever falls to or below FFmpeg's probe window, the guard
// restarts exactly the slow starts it exists to rescue -- and it would do it
// on a loop, up to maxSilentRestarts, turning a stream that was merely slow to
// characterise into one that never starts at all.
//
// The two numbers live in different packages. silentPublishBudget is derived
// from ffmpeg.RelayProbeWindow rather than written beside it, and this asserts
// the derivation still holds: raising the probe window for #674 (15s -> 45s)
// would have collided with a hardcoded 45s here.
//
// Warning rung. Control would mean one package owning both, which is a larger
// change than a multiplier and an assertion are worth.
func TestTheSilentPublishBudgetOutlastsTheProbeWindow(t *testing.T) {
	if silentPublishBudget <= ffmpeg.RelayProbeWindow {
		t.Fatalf("silentPublishBudget is %s and ffmpeg.RelayProbeWindow is %s.\n"+
			"A destination still probing has published nothing yet, so this watchdog would\n"+
			"restart it mid-probe -- repeatedly, up to maxSilentRestarts -- and a stream that\n"+
			"was only slow to characterise would never start at all.",
			silentPublishBudget, ffmpeg.RelayProbeWindow)
	}
	// Not merely greater: it must clear the window plus a spawn and a connect.
	if margin := silentPublishBudget - ffmpeg.RelayProbeWindow; margin < ffmpeg.RelayProbeWindow {
		t.Fatalf("only %s between silentPublishBudget (%s) and the probe window (%s).\n"+
			"That margin is the spawn, the connect, and a stream that uses its whole probe\n"+
			"budget legitimately -- it is headroom, not slack.",
			margin, silentPublishBudget, ffmpeg.RelayProbeWindow)
	}
}
