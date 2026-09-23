package supervisor

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A sink that stops reading leaves the child running and still reporting: the
// progress blocks keep coming, but out_time freezes, FFmpeg's bitrate= (a
// whole-run average) freezes non-zero with it, and speed= decays slowly. Before
// this was fixed the status said running, a healthy bitrate and nothing else
// for as long as the stall lasted (exploratory row 7). The status now says
// stalled, and the two "how fast is it going now" readings say 0.
//
// A real child through the real progress pipe, not noteProgress by hand: the
// thing under test is what an operator reads while FFmpeg keeps talking.
func TestAStalledChildIsReportedStalledWithNothingMoving(t *testing.T) {
	p := testProcess(t, fakeStall(600*time.Millisecond, 100*time.Millisecond), Spec{})
	p.stallAfter = 700 * time.Millisecond
	p.Start()

	waitFor(t, "media to move", func() bool { return p.Status().Progress.OutTimeMS > 0 })
	if st := p.Status(); st.Stalled {
		t.Fatalf("a child whose output is advancing reported stalled: %+v", st)
	}

	// Stalled from 0.6s; reported once out_time has been still for 0.7s. The
	// bound says the stall is seen within a few block intervals of that, not
	// merely eventually.
	waitForBounded(t, 5*time.Second, "the stall to be reported", func() bool { return p.Status().Stalled })
	st := p.Status()
	if st.State != StateRunning {
		t.Errorf("state = %v, want running: the child is alive, it is the delivery that stopped", st.State)
	}
	if st.StalledSec <= 0 {
		t.Errorf("StalledSec = %v, want how long out_time has been still", st.StalledSec)
	}
	if st.Progress.BitrateKbps != 0 || st.Progress.Speed != 0 {
		t.Errorf("bitrate %v kbps, speed %vx while stalled; want 0 and 0 -- FFmpeg's run averages "+
			"are frozen non-zero and read as a healthy destination", st.Progress.BitrateKbps, st.Progress.Speed)
	}
	if st.Progress.OutTimeMS == 0 || st.Progress.TotalSize == 0 {
		t.Errorf("progress counters were blanked (%+v); they are how far the run got and stay", st.Progress)
	}
}

// The control: a child that never stops delivering is never called stalled,
// however long it is watched, with the same threshold.
func TestADeliveringChildIsNeverReportedStalled(t *testing.T) {
	p := testProcess(t, fakeStall(time.Hour, 100*time.Millisecond), Spec{})
	p.stallAfter = 700 * time.Millisecond
	p.Start()

	waitFor(t, "media to move", func() bool { return p.Status().Progress.OutTimeMS > 0 })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := p.Status(); st.Stalled || st.Progress.BitrateKbps == 0 {
			t.Fatalf("a delivering child reported %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Before any media has moved there is nothing to have stalled: a destination
// in its probe window, or an ingest waiting for OBS, is waiting, not stuck.
func TestAChildThatHasMovedNoMediaIsNotStalled(t *testing.T) {
	p := running(time.Hour)
	p.stallAfter = time.Millisecond
	if p.Status().Stalled {
		t.Fatal("a child with no media yet reported stalled")
	}
}

// MOVEMENT IS A CHANGE IN OUTPUT TIME, NOT A RISE. A stall repeats one out_time
// block after block, so any other value means media moved. Output time that
// steps backwards -- a timestamp discontinuity FFmpeg passes through -- and
// then keeps advancing below its old high is a delivering child. Counted only
// on a rise, it read stalled until it climbed past the old figure: a stall
// warning and up=0 on a destination that was sending the whole time.
func TestOutputTimeThatStepsBackIsStillMovement(t *testing.T) {
	p := running(time.Hour)
	p.stallAfter = time.Minute
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 3_600_000})
	// As if the last advance was long ago, so only the next block can clear it.
	p.movedAt = time.Now().Add(-time.Hour)

	p.noteProgress(ffmpeg.Progress{OutTimeMS: 2_000})
	if st := p.Status(); st.Stalled {
		t.Fatalf("output time moved (3600000 ms -> 2000 ms) and the child still reads stalled "+
			"for %.0fs; it is delivering", st.StalledSec)
	}
}
