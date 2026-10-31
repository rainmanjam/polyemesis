package supervisor

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// The two windowed readings the Stream Deck's destination key judges on: how
// many times a process has been respawned recently, and how many frames it has
// dropped recently. Both exist because the counters they come from are
// cumulative for the life of the process, and a cumulative number cannot tell
// "flapping now" from "flapped once, an hour ago".

// TestRecentRestartsCountsOnlyTheRestartsInsideTheStormWindow pins the window
// to the one PolyemesisDestinationRestartStorm judges on. Restarts older than
// RestartStormWindow are history, not a storm, however many of them there were.
func TestRecentRestartsCountsOnlyTheRestartsInsideTheStormWindow(t *testing.T) {
	p := New(quietLog(), Spec{Name: "storm"})
	now := time.Now()
	// Ten restarts an hour ago...
	for i := 0; i < 10; i++ {
		p.noteRestartLocked(now.Add(-time.Hour + time.Duration(i)*time.Second))
	}
	// ...and three in the last five minutes.
	for i := 0; i < 3; i++ {
		p.noteRestartLocked(now.Add(-5*time.Minute + time.Duration(i)*time.Second))
	}
	if got := p.recentRestartsLocked(now); got != 3 {
		t.Fatalf("recent restarts = %d, want 3: only the restarts inside %s count", got, RestartStormWindow)
	}
}

// TestTheRestartRingDoesNotGrowWithoutBound: a process restarted every second
// for a day must not hold a day of timestamps. Pruning on every append keeps the
// ring to the window.
func TestTheRestartRingDoesNotGrowWithoutBound(t *testing.T) {
	p := New(quietLog(), Spec{Name: "storm"})
	base := time.Now().Add(-24 * time.Hour)
	for i := 0; i < 24*60; i++ {
		p.noteRestartLocked(base.Add(time.Duration(i) * time.Minute))
	}
	if n := len(p.restartAt); n > int(RestartStormWindow/time.Minute)+1 {
		t.Fatalf("the restart ring holds %d timestamps after a day of one-a-minute restarts, want at most a window's worth", n)
	}
}

// TestStatusReportsRecentRestartsWhileTheProcessIsReconnecting: the reading is
// on the snapshot whatever the state, because a storm is most often observed
// between runs.
func TestStatusReportsRecentRestartsWhileTheProcessIsReconnecting(t *testing.T) {
	p := New(quietLog(), Spec{Name: "storm"})
	now := time.Now()
	p.mu.Lock()
	for i := 0; i < 7; i++ {
		p.noteRestartLocked(now.Add(-time.Duration(i) * time.Minute))
	}
	p.state = StateReconnecting
	p.mu.Unlock()
	if got := p.Status().RecentRestarts; got != 7 {
		t.Fatalf("Status().RecentRestarts = %d, want 7", got)
	}
}

// TestDroppedFramesAreMeasuredOverTheWindowAndNotTheWholeRun is the drop-frame
// twin of the rate test. A destination that dropped a thousand frames at the
// start of an hour-long run and none since is healthy now.
func TestDroppedFramesAreMeasuredOverTheWindowAndNotTheWholeRun(t *testing.T) {
	p := New(quietLog(), Spec{Name: "drops"})
	base := time.Now().Add(-time.Hour)
	// A thousand drops in the first minute...
	p.sampleDropsLocked(base, 0, 0)
	p.sampleDropsLocked(base.Add(time.Minute), 1800, 1000)
	// ...then an hour of clean delivery at 30 fps.
	for i := 1; i <= 3600; i++ {
		at := base.Add(time.Minute + time.Duration(i)*time.Second)
		p.sampleDropsLocked(at, 1800+int64(i)*30, 1000)
	}
	now := base.Add(time.Minute + 3600*time.Second)
	dropped, frames, ok := p.dropsLocked(now)
	if !ok {
		t.Fatal("no drop reading after an hour of samples")
	}
	if dropped != 0 {
		t.Fatalf("dropped = %d over the last window, want 0: the drops were an hour ago", dropped)
	}
	if frames < 250 || frames > 350 {
		t.Fatalf("frames = %d over the last window, want about %d", frames, int(RateWindow/time.Second)*30)
	}
}

// TestDroppedFramesInsideTheWindowAreReported: drops happening now are read.
func TestDroppedFramesInsideTheWindowAreReported(t *testing.T) {
	p := New(quietLog(), Spec{Name: "drops"})
	base := time.Now()
	for i := 0; i <= 10; i++ {
		p.sampleDropsLocked(base.Add(time.Duration(i)*time.Second), int64(i)*30, int64(i)*4)
	}
	dropped, frames, ok := p.dropsLocked(base.Add(10 * time.Second))
	if !ok || dropped != 40 || frames != 300 {
		t.Fatalf("dropped, frames, ok = %d, %d, %v; want 40, 300, true", dropped, frames, ok)
	}
}

// TestTheDropReadingIsUnknownUntilThereIsAWindow: the first seconds of a run
// are not a verdict, the same rule the rate keeps.
func TestTheDropReadingIsUnknownUntilThereIsAWindow(t *testing.T) {
	p := New(quietLog(), Spec{Name: "drops"})
	now := time.Now()
	p.sampleDropsLocked(now, 0, 0)
	p.sampleDropsLocked(now.Add(time.Second), 30, 30)
	if _, _, ok := p.dropsLocked(now.Add(time.Second)); ok {
		t.Fatal("a drop reading was reported from one second of history")
	}
}

// TestNoteProgressFeedsTheDropWindow: the reading is fed from the same progress
// block every other field is.
func TestNoteProgressFeedsTheDropWindow(t *testing.T) {
	p := New(quietLog(), Spec{Name: "drops"})
	p.noteProgress(ffmpeg.Progress{OutTimeMS: 1000, Frame: 30, DropFrames: 2})
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.drops) != 1 || p.drops[0].frames != 30 || p.drops[0].dropped != 2 {
		t.Fatalf("drop window after one progress block = %+v, want one sample of 2 drops in 30 frames", p.drops)
	}
}

// TestStatusCarriesTheDropReadingOfARunningProcess: a running process reports
// the window on its snapshot.
func TestStatusCarriesTheDropReadingOfARunningProcess(t *testing.T) {
	p := New(quietLog(), Spec{Name: "drops"})
	p.mu.Lock()
	p.state = StateRunning
	base := time.Now().Add(-RateWindow)
	for i := 0; i <= 10; i++ {
		p.sampleDropsLocked(base.Add(time.Duration(i)*time.Second), int64(i)*30, int64(i)*5)
	}
	p.mu.Unlock()
	st := p.Status()
	if !st.DropsKnown || st.DroppedRecent != 50 || st.FramesRecent != 300 {
		t.Fatalf("Status drop reading = (%d of %d, known=%v), want (50 of 300, known=true)",
			st.DroppedRecent, st.FramesRecent, st.DropsKnown)
	}
}

// TestAStormEndsWhenTheRestartsAgeOutWithoutANewOne: a destination that stormed
// twenty minutes ago and has delivered since is not storming. Nothing appends
// to the ring while it delivers, so the age is judged when the count is read.
//
// MUTATION: count every timestamp in recentRestartsLocked regardless of age.
// Observed: FAIL here (7, want 0), while every test above still passed --
// pruning on append alone hid the defect.
func TestAStormEndsWhenTheRestartsAgeOutWithoutANewOne(t *testing.T) {
	p := New(quietLog(), Spec{Name: "storm"})
	now := time.Now()
	for i := 0; i < 7; i++ {
		p.noteRestartLocked(now.Add(-20*time.Minute + time.Duration(i)*time.Second))
	}
	if got := p.recentRestartsLocked(now); got != 0 {
		t.Fatalf("recent restarts = %d twenty minutes after the last one, want 0", got)
	}
}
