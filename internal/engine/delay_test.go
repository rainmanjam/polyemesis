package engine

import (
	"encoding/binary"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// delayedRow is a passthrough RTMP destination with a one-second broadcast
// delay: the shortest there is, so the tests below can watch a whole fill and
// a whole drain in real time.
func delayedRow(t *testing.T, e *Engine) *db.Destination {
	t.Helper()
	row := testDestination(0, nil)
	row.Name = "delayed"
	row.DelaySeconds = 1
	row.SourceID = &e.sourceID
	saved, err := e.store.CreateDestination(row)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	return saved
}

func delayPlan(row *db.Destination) destPlan {
	compiled := routing.Result{FilterComplex: "[0:a:0]anull[out]", OutLabel: "[out]"}
	return destPlan{row: row, compiled: compiled, spec: destSpec(row, compiled, "")}
}

// feed delivers n 1316-byte datagrams into a hub, paced like a relay.
func feed(h *relay.Hub, n int) {
	for i := range n {
		p := make([]byte, 1316)
		binary.BigEndian.PutUint64(p, uint64(i))
		h.Deliver(p)
		time.Sleep(time.Millisecond)
	}
}

func destOf(e *Engine, id int64) *destination {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.dests[id]
}

func drainingOf(e *Engine, id int64) *destination {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.draining[id]
}

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func statusFor(t *testing.T, e *Engine, id int64) DestStatus {
	t.Helper()
	for _, d := range e.Status().Destinations {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("destination %d is missing from the status", id)
	return DestStatus{}
}

// A DELAYED DESTINATION'S FFMPEG READS THE DELAY, NOT THE RELAY, AND STARTS
// LATE. Its upstream subscription is the line's, under a name of its own; the
// FFmpeg's own name is on the private hub only. And the child is not started
// when the destination is: it waits for the delayed data, which is what keeps
// #20's quick-rejection clock from running against an idle socket.
func TestADelayedDestinationReadsItsPrivateHubAndItsFFmpegWaitsForTheDelayedData(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	d := destOf(e, row.ID)
	if d == nil || d.delay == nil {
		t.Fatal("a destination with a delay was started without one")
	}
	up := e.hub.Subscribers()
	if !slices.Contains(up, delayDestSubName(row.ID)) {
		t.Fatalf("the delay line is not reading the upstream hub: %v", up)
	}
	if slices.Contains(up, destSubName(row.ID, "")) {
		t.Fatalf("the FFmpeg is reading the UNDELAYED upstream hub: %v", up)
	}
	if !slices.Contains(d.delay.hub.Subscribers(), destSubName(row.ID, "")) {
		t.Fatal("the FFmpeg is not subscribed to the private delay hub")
	}
	if st := d.proc.Status().State; st != supervisor.StateStopped {
		t.Fatalf("the FFmpeg is %q before any delayed data exists; it must wait", st)
	}
	if got := statusFor(t, e, row.ID).Delay; got == nil || got.Phase != DelayBuffering || got.Seconds != 1 {
		t.Fatalf("status delay = %+v, want buffering at 1 s", got)
	}

	go feed(e.hub, 300)
	eventually(t, "the FFmpeg to start for the delayed data", 5*time.Second, func() bool {
		return d.proc.Status().State != supervisor.StateStopped
	})
	eventually(t, "the delayed data to reach the private hub", 5*time.Second, func() bool {
		return d.delay.hub.Stats().RxPackets > 0
	})
	t.Cleanup(func() { e.Stop() })
}

// FLUSH ON STOP. Disabling a delayed destination does not stop it: it moves to
// the draining set, Status still reports its process with Phase "draining",
// and the watchers still see it as ENABLED -- so the down edge, and the
// YouTube end it triggers, wait for the tail. When the tail is out it is torn
// down: both subscriptions and every port go back.
func TestStoppingADelayedDestinationPlaysTheTailOutBeforeItIsTornDown(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	d := destOf(e, row.ID)
	feed(e.hub, 200)

	// Stopped: the row is disabled and the plan no longer wants it.
	row.Enabled = false
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	e.stopDestinations(map[int64]destPlan{})

	if drainingOf(e, row.ID) != d {
		t.Fatal("a stopped delayed destination was not left draining")
	}
	st := statusFor(t, e, row.ID)
	if st.Delay == nil || st.Delay.Phase != DelayDraining || st.Process == nil {
		t.Fatalf("draining status = delay %+v process %v", st.Delay, st.Process)
	}
	if st.Enabled {
		t.Fatal("the row is disabled; the status must say so")
	}
	snap := e.alertSnapshot(time.Now(), true)
	for _, ds := range snap.Destinations {
		if ds.ID == row.ID && !ds.Enabled {
			t.Fatal("a destination still publishing its tail reads as disabled to the " +
				"watchers, so the down edge and the YouTube end would fire Delay early")
		}
	}
	if slices.Contains(e.hub.Subscribers(), delayDestSubName(row.ID)) {
		t.Fatal("a draining line is still taking new input from upstream")
	}

	eventually(t, "the drain to finish", 6*time.Second, func() bool {
		return drainingOf(e, row.ID) == nil && destOf(e, row.ID) == nil
	})
	eventually(t, "the teardown to release the private hub", 3*time.Second, func() bool {
		return !slices.Contains(d.delay.hub.Subscribers(), destSubName(row.ID, ""))
	})
	if got := d.delay.hub.Stats().RxPackets; got < 200 {
		t.Fatalf("the drain released %d of 200 datagrams before the stop", got)
	}
	if st := statusFor(t, e, row.ID); st.Delay != nil || st.Process != nil {
		t.Fatalf("a drained destination still reports delay %+v process %v", st.Delay, st.Process)
	}
}

// STOP NOW cuts the tail at once.
func TestStopNowCutsADrainingDestinationAtOnce(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	row.DelaySeconds = 600
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	feed(e.hub, 50)
	e.stopDestinations(map[int64]destPlan{})
	if drainingOf(e, row.ID) == nil {
		t.Fatal("setup: not draining")
	}
	start := time.Now()
	if !e.CutDelay(row.ID) {
		t.Fatal("CutDelay found nothing to cut")
	}
	if drainingOf(e, row.ID) != nil {
		t.Fatal("still draining after Stop now")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Stop now took %s on a ten-minute delay", took)
	}
	if e.CutDelay(row.ID) {
		t.Fatal("a second Stop now found a drain to cut")
	}
	files, _ := filepath.Glob(filepath.Join(delaySpillDir(e.cfg.DataDir, row.ID), "*"))
	if len(files) != 0 {
		t.Fatalf("Stop now left the spill behind: %v", files)
	}
}

// ANY RESTART CUTS. A spec change -- here, the delay itself -- tears the old
// run down at once rather than draining it, since a drain would put two
// FFmpegs on one key.
func TestChangingTheDelayRestartsTheDestinationWithoutDraining(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	old := destOf(e, row.ID)
	changed := *row
	changed.DelaySeconds = 2
	p := delayPlan(&changed)
	if p.spec == old.spec {
		t.Fatal("changing the delay did not change the restart signature")
	}
	e.stopDestinations(map[int64]destPlan{row.ID: p})
	if drainingOf(e, row.ID) != nil {
		t.Fatal("a restart was drained instead of cut")
	}
	if destOf(e, row.ID) != nil {
		t.Fatal("the old run survived a delay change")
	}
	if slices.Contains(e.hub.Subscribers(), delayDestSubName(row.ID)) {
		t.Fatal("the old line is still subscribed upstream")
	}
}

// FAILOVER AND REBUILT TIERS PASS THROUGH THE BUFFER. A delayed destination
// whose upstream hub is replaced keeps its line, its buffer and its FFmpeg;
// only the line's input moves to the new hub.
func TestAReplacedUpstreamRePointsTheDelayLineAndKeepsTheBuffer(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	row.DelaySeconds = 30
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	old, err := relay.New(testLogger(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	if err := e.startDest(delayPlan(row), old, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	before := destOf(e, row.ID)
	feed(old, 100)
	held := before.delay.line.Status().Packets

	// The plan is unchanged; only the hub it would now be given (e.hub) differs.
	e.stopDestinations(map[int64]destPlan{row.ID: delayPlan(row)})

	after := destOf(e, row.ID)
	if after == nil || after.delay != before.delay || after.proc != before.proc {
		t.Fatal("a replaced upstream restarted the delayed destination and lost its buffer")
	}
	if after.hub != e.hub {
		t.Fatal("the destination does not record its new upstream")
	}
	if slices.Contains(old.Subscribers(), delayDestSubName(row.ID)) {
		t.Fatal("the line is still subscribed to the old hub")
	}
	if !slices.Contains(e.hub.Subscribers(), delayDestSubName(row.ID)) {
		t.Fatal("the line was not subscribed to the new hub")
	}
	if got := after.delay.line.Status().Packets; got < held || held == 0 {
		t.Fatalf("buffer held %d before the re-point and %d after", held, got)
	}
	feed(e.hub, 20)
	eventually(t, "the new upstream to reach the buffer", 3*time.Second, func() bool {
		return after.delay.line.Status().Packets >= held+20
	})
	// A second pass with the same hub is a no-op.
	e.stopDestinations(map[int64]destPlan{row.ID: delayPlan(row)})
	if destOf(e, row.ID).delay != before.delay {
		t.Fatal("a steady reconcile disturbed the delay line")
	}
	e.Stop()
}

// STARTED AGAIN WHILE DRAINING: the old tail is cut so only one FFmpeg ever
// publishes the key, and the new run starts with a fresh line.
func TestStartingADestinationThatIsStillDrainingCutsTheOldTail(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	row.DelaySeconds = 600
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	feed(e.hub, 20)
	e.stopDestinations(map[int64]destPlan{})
	old := drainingOf(e, row.ID)
	if old == nil {
		t.Fatal("setup: not draining")
	}
	e.startDestinations(map[int64]destPlan{row.ID: delayPlan(row)})
	if drainingOf(e, row.ID) != nil {
		t.Fatal("the old drain survived a new start")
	}
	cur := destOf(e, row.ID)
	if cur == nil || cur.delay == nil || cur.delay == old.delay {
		t.Fatal("the new start did not get a line of its own")
	}
	if st := old.proc.Status().State; st != supervisor.StateStopped {
		t.Fatalf("the old FFmpeg is %q after its drain was cut", st)
	}
	e.Stop()
}

// SHUTDOWN CUTS DRAINS. A server stopping cannot wait fifteen minutes, and a
// drain left running would be an FFmpeg nothing can reach.
func TestShutdownCutsDestinationsThatAreStillDraining(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	row.DelaySeconds = 600
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	feed(e.hub, 20)
	e.stopDestinations(map[int64]destPlan{})
	d := drainingOf(e, row.ID)
	if d == nil {
		t.Fatal("setup: not draining")
	}
	e.Stop()
	if drainingOf(e, row.ID) != nil {
		t.Fatal("a drain survived shutdown")
	}
	if subs := d.delay.hub.Subscribers(); len(subs) != 0 {
		t.Fatalf("the private hub still has subscribers after shutdown: %v", subs)
	}
}

// RETRY RESTARTS THE FFMPEG, NEVER THE BUFFER. A held destination (#20) is
// retried by restarting its process; the delay line, and the fifteen minutes it
// may hold, must come through untouched.
func TestRetryingADelayedDestinationRestartsTheProcessAndLeavesTheBufferAlone(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	before := destOf(e, row.ID)
	feed(e.hub, 30)
	if err := e.RestartDestination(row.ID); err != nil {
		t.Fatalf("RestartDestination: %v", err)
	}
	after := destOf(e, row.ID)
	if after.delay != before.delay {
		t.Fatal("Retry replaced the delay line")
	}
	if !slices.Contains(e.hub.Subscribers(), delayDestSubName(row.ID)) {
		t.Fatal("Retry took the line off the upstream hub")
	}
	if after.delay.line.Status().Packets == 0 && !after.delay.line.Status().Releasing {
		t.Fatal("Retry emptied the buffer")
	}
	e.Stop()
}

// A FILLING DELAY IS NOT AN OUTAGE. The alert snapshot marks it Buffering, so
// the alert watcher does not page "destination down" for the fifteen minutes
// it is meant to hold.
func TestABufferingDestinationIsMarkedSoTheAlertWatcherDoesNotCallItDown(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	row.DelaySeconds = 600
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	found := false
	for _, ds := range e.alertSnapshot(time.Now(), true).Destinations {
		if ds.ID == row.ID {
			found = true
			if !ds.Buffering || !ds.Enabled || ds.Running {
				t.Fatalf("snapshot = %+v, want enabled, not running, buffering", ds)
			}
		}
	}
	if !found {
		t.Fatal("the destination is missing from the alert snapshot")
	}
	e.Stop()
}

// The spill directory is built from the data directory and the numeric id
// alone: the line deletes it wholesale, so nothing an operator typed may reach
// the path.
func TestTheSpillDirectoryIsTheDataDirectoryAndTheNumericIDOnly(t *testing.T) {
	got := delaySpillDir("/srv/polyemesis", 42)
	if want := filepath.Join("/srv/polyemesis", "delay", "42"); got != want {
		t.Fatalf("spill dir = %q, want %q", got, want)
	}
}

// The floor a spill must leave free follows the recording guard's when one is
// set, so the delay and recordings fail at the same free-space line.
func TestTheDelayDiskFloorFollowsTheRecordingGuard(t *testing.T) {
	s := db.DefaultSettings()
	s.Recording.MinFreeGB = 5
	if got := delayFloorBytes(s); got != 5<<30 {
		t.Fatalf("floor = %d, want 5 GiB", got)
	}
	s.Recording.MinFreeGB = 0
	if got := delayFloorBytes(s); got != 2<<30 {
		t.Fatalf("floor with no recording guard = %d, want 2 GiB", got)
	}
}

func TestADroppingBufferPutsAWarningOnTheCard(t *testing.T) {
	if delayWarning(nil) != "" || delayWarning(&DelayStatus{Seconds: 5}) != "" {
		t.Fatal("a healthy delay produced a warning")
	}
	w := delayWarning(&DelayStatus{DroppedBytes: 3 << 20, Problem: "the disk budget is full"})
	if !strings.Contains(w, "3 MB") || !strings.Contains(w, "disk budget") {
		t.Fatalf("warning = %q", w)
	}
}
