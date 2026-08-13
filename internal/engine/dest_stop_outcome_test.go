package engine

// #209. POST /destinations/{id}/stop answered `state: "stopped"` whether the
// child was reaped or was sent SIGKILL and abandoned, because
// supervisor.stop() sets StateStopped on BOTH of its arms and the one value that
// could tell them apart -- ErrStopDeadline -- was discarded one line after it
// was computed.
//
// These tests pin the two halves of the repair: teardownDest records what the
// stop actually achieved, and Status() carries it to the API for every row,
// including the ones that no longer have a live entry -- which is all of them,
// after a stop.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// engineFakeChildFlag marks a re-execution of this test binary as a child for a
// supervisor to run. Read straight out of os.Args before the testing package
// parses flags, exactly as internal/supervisor's fake does.
//
// A real child and not a stub, because the property under test begins with
// (*supervisor.Process).Stop taking its deadline arm, and stop() returns nil
// immediately for a Process that is not running.
const engineFakeChildFlag = "-polyemesis-engine-fake-child"

// engineFakeChildStderr names an environment variable whose value the fake
// child writes to stderr, once, before failing.
//
// AN ENVIRONMENT VARIABLE AND NOT AN ARGUMENT, deliberately. The line this
// carries is an FFmpeg failure containing a stream key, and a test that proved
// a key never reaches process.log by putting one on a command line every other
// account on the machine can read would be the same joke
// scripts/acceptance-multistream.sh's 8b avoids with `grep -F -f <(...)`.
// supervisor never sets cmd.Env, so the child inherits what t.Setenv put here.
const engineFakeChildStderr = "POLYEMESIS_ENGINE_FAKE_CHILD_STDERR"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == engineFakeChildFlag {
		// A child that says something and dies, for the tests that need the
		// stderr scan to run for real: the line goes through classify,
		// (*Process).scrub, the log ring and the on-disk sink exactly as a
		// destination's would. Exit 1 because a destination that could not open
		// its output is a failure, and the tail of stderr becomes LastError.
		if line := os.Getenv(engineFakeChildStderr); line != "" {
			fmt.Fprintln(os.Stderr, line)
			os.Exit(1)
		}
		// Long enough to outlive any test here, short enough that a run
		// abandoned halfway does not leave it behind for a human to find. It
		// installs no signal handlers, so the SIGTERM the teardown sends ends it
		// the ordinary way.
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runningProcess returns a supervisor Process with a live child, whose reap is
// held open for as long as the test runs.
//
// THE BLOCKED DRAIN IS WHAT MAKES THE DEADLINE ARM DETERMINISTIC. supervise()
// closes `done` only after runOnce() returns, and runOnce cannot return while a
// drain goroutine is still running. So from Start() until the cleanup below,
// stop()'s reap arm is not merely unlikely to be ready -- it is unreachable, and
// the arm taken does not depend on how fast the child happens to die on the
// machine running this. (Same construction as
// supervisor.TestStopReportsWhenItHadToKillTheChild, and the same reason.)
func runningProcess(t *testing.T, name string) *supervisor.Process {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary to re-execute as a child: %v", err)
	}
	release := make(chan struct{})
	p := supervisor.New(testLogger(), supervisor.Spec{
		Name: name,
		Bin:  self,
		Args: []string{engineFakeChildFlag},
		StdoutHandler: func(io.Reader) error {
			<-release
			return nil
		},
	})
	t.Cleanup(func() { close(release) })
	p.Start()

	deadline := time.Now().Add(15 * time.Second)
	for p.Status().State != supervisor.StateRunning {
		if time.Now().After(deadline) {
			t.Fatalf("the child never reached running (state %q); a stop on a Process that "+
				"is not running returns nil immediately and this test would assert "+
				"nothing", p.Status().State)
		}
		time.Sleep(time.Millisecond)
	}
	return p
}

// shrinkStopTimeout makes teardownDest's context expire before it is consulted,
// so the deadline arm is reached without waiting out the production 12 seconds.
// The interval is not what is under test; which arm ran, and whether anyone was
// told, is.
func shrinkStopTimeout(t *testing.T) {
	t.Helper()
	prev := stopTimeout
	stopTimeout = time.Nanosecond
	t.Cleanup(func() { stopTimeout = prev })
}

func TestTeardownDestRecordsAStopThatNeverObservedTheChild(t *testing.T) {
	e, _ := storeEngine(t)
	shrinkStopTimeout(t)

	row := &db.Destination{ID: 11, Name: "youtube", Kind: db.DestRTMP}
	d := &destination{row: row, proc: runningProcess(t, "dest:11")}

	e.teardownDest(d)

	warning, unreaped := e.StopUnreaped(row.ID)
	if !unreaped {
		t.Fatalf("teardownDest stopped a running child on an expired deadline and recorded "+
			"nothing. Stop returned ErrStopDeadline -- SIGKILL issued, not waited for -- "+
			"and teardownDest has just released this destination's relay port and hub "+
			"subscription on the strength of a stop it never confirmed. The API answers "+
			"%q either way, so this record is the only thing that can tell a caller "+
			"which happened.", supervisor.StateStopped)
	}
	if !strings.Contains(warning, "SIGKILL") {
		t.Errorf("warning = %q, want it to say the child was sent SIGKILL and may still be "+
			"running; a warning that does not say what is uncertain is not one", warning)
	}
}

// And the ordinary teardown must stay silent, or every stop would carry a
// warning and the warning would mean nothing.
func TestTeardownDestRecordsNothingWhenTheChildWasReaped(t *testing.T) {
	e, _ := storeEngine(t)

	row := &db.Destination{ID: 12, Name: "twitch", Kind: db.DestRTMP}
	// No live child: Stop on a Process that was never started returns nil, which
	// is the same nil the reap arm returns and the same nil this must not
	// mistake for a deadline.
	d := &destination{
		row:  row,
		proc: supervisor.New(testLogger(), supervisor.Spec{Name: "dest:12"}),
	}

	e.teardownDest(d)

	if warning, unreaped := e.StopUnreaped(row.ID); unreaped {
		t.Errorf("a stop that returned nil was recorded as unreaped (%q). A warning that "+
			"fires on the healthy path is worse than none: the one stop that really did "+
			"abandon a child would be indistinguishable from the hundred that did not.",
			warning)
	}
}

// The record has to reach the caller, and the caller reads Status(). This is the
// half that the obvious implementation gets wrong: a destination that has been
// stopped is GONE from e.dests, so anything hung off the live entry is absent
// exactly when the warning is true.
func TestStatusCarriesTheStopWarningForARowWithNoLiveEntry(t *testing.T) {
	e, store := storeEngine(t)

	row, err := store.CreateDestination(&db.Destination{
		Name: "youtube", Kind: db.DestRTMP, URL: "rtmp://example/live", StreamKey: "k",
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	// The row exists and has no live destination, which is the state a stopped
	// destination is in.
	if _, live := e.dests[row.ID]; live {
		t.Fatalf("precondition: destination %d must not be live", row.ID)
	}

	e.noteStopOutcome(row.ID, nil)
	for _, ds := range e.Status().Destinations {
		if ds.ID == row.ID && ds.StopWarning != "" {
			t.Fatalf("StopWarning = %q on a destination whose last stop reported no "+
				"error", ds.StopWarning)
		}
	}

	e.noteStopOutcome(row.ID, supervisor.ErrStopDeadline)

	found := false
	for _, ds := range e.Status().Destinations {
		if ds.ID != row.ID {
			continue
		}
		found = true
		if ds.StopWarning == "" {
			t.Errorf("destination %d was stopped without its child being observed dead, "+
				"and Status() says nothing. Process is nil here -- the live entry is "+
				"gone, which is what a stop DOES -- so a warning read off the live "+
				"entry would be silent in precisely the case it exists for.", row.ID)
		}
	}
	if !found {
		t.Fatalf("destination %d is missing from Status(); this test asserted nothing", row.ID)
	}

	// And a fresh start retracts it, or the warning becomes permanent and the
	// card carries it for the rest of the process's life.
	e.noteStopOutcome(row.ID, nil)
	if warning, unreaped := e.StopUnreaped(row.ID); unreaped {
		t.Errorf("the warning survived a clean outcome: %q", warning)
	}
}
