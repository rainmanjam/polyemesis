package engine

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
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

// retiringOf is the destination finishDrain has taken out of e.draining and is
// still tearing down. It leaves e.retiring only once teardownDest has returned
// -- after the FFmpeg is stopped, the private hub released AND the spill
// deleted -- so it, not any one of those, is the "teardown is over" signal.
func retiringOf(e *Engine, id int64) *destination {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.retiring[id]
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

	// WAIT FOR THE WHOLE TEARDOWN, NOT ONE STEP OF IT. finishDrain moves the
	// destination from e.draining to e.retiring, runs teardownDest, and only
	// then drops it from e.retiring; until it does, Status reports it as
	// transitioning, with its process. This used to wait for draining and dests
	// to empty and then for the private hub to lose its subscriber -- but the
	// unsubscribe is in the MIDDLE of teardownDest, ahead of closeDelay deleting
	// the spill, which on Windows is slow enough that the status read below
	// landed while the destination was still retiring and reported its
	// process. The retiring entry is removed under e.mu after teardownDest has
	// returned, so once it is gone every step of the teardown has happened.
	eventually(t, "the drain and its teardown to finish", 9*time.Second, func() bool {
		return drainingOf(e, row.ID) == nil && destOf(e, row.ID) == nil &&
			retiringOf(e, row.ID) == nil
	})
	if slices.Contains(d.delay.hub.Subscribers(), destSubName(row.ID, "")) {
		t.Fatal("the teardown finished without releasing the private hub")
	}
	if got := d.delay.hub.Stats().RxPackets; got < 200 {
		t.Fatalf("the drain released %d of 200 datagrams before the stop", got)
	}
	if st := statusFor(t, e, row.ID); st.Delay != nil || st.Process != nil {
		t.Fatalf("a drained destination still reports delay %+v process %v (transitioning %v)",
			st.Delay, st.Process, st.Transitioning)
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
	files, _ := filepath.Glob(filepath.Join(delayRoot(e.cfg.DataDir), "*", "*"))
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
	got := delaySpillDir("/srv/polyemesis", 42, 7)
	if want := filepath.Join("/srv/polyemesis", "delay", "42-7"); got != want {
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

// startDraining starts a delayed destination with a long delay, feeds it, and
// stops it plainly, leaving it draining.
func startDraining(t *testing.T, e *Engine, row *db.Destination) *destination {
	t.Helper()
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	feed(e.hub, 20)
	row.Enabled = false
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	e.stopDestinations(map[int64]destPlan{})
	d := drainingOf(e, row.ID)
	if d == nil {
		t.Fatal("setup: a plain stop did not drain")
	}
	return d
}

func longDelayRow(t *testing.T, e *Engine) *db.Destination {
	t.Helper()
	row := delayedRow(t, e)
	row.DelaySeconds = 600
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	return row
}

// DELETE CUTS. Deleting a destination is not a request to keep airing it, so
// a delayed one with no row left is cut at once -- whatever is buffered never
// reaches the platform.
//
// Mutation: drop `stillThere[id] &&` from the drain condition in
// stopDestinations. This test then finds the deleted destination draining.
func TestDeletingADelayedDestinationCutsItAtOnceInsteadOfDraining(t *testing.T) {
	e, _ := storeEngine(t)
	row := longDelayRow(t, e)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	d := destOf(e, row.ID)
	feed(e.hub, 20)
	if err := e.store.DeleteDestination(row.ID); err != nil {
		t.Fatal(err)
	}
	e.stopDestinations(map[int64]destPlan{})
	if drainingOf(e, row.ID) != nil {
		t.Fatal("a deleted destination was left airing its buffered tail")
	}
	if st := d.proc.Status().State; st != supervisor.StateStopped {
		t.Fatalf("the deleted destination's FFmpeg is %q", st)
	}
	if got := d.delay.hub.Stats().RxPackets; got != 0 {
		t.Fatalf("%d buffered datagrams reached the platform side after the delete", got)
	}
}

// STOP-ALL (AND STOP NOW) CUT. CutOnStop marks the destination before the
// reconcile, so the stop that follows never starts a drain at all.
//
// Mutation: drop `&& !cutReq[id]` from the drain condition. This test then
// finds the destination draining after a stop-all.
func TestACutRequestMakesTheFollowingStopCutInsteadOfDrain(t *testing.T) {
	e, _ := storeEngine(t)
	row := longDelayRow(t, e)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	feed(e.hub, 20)
	row.Enabled = false
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	e.CutOnStop(row.ID)
	start := time.Now()
	e.stopDestinations(map[int64]destPlan{})
	if drainingOf(e, row.ID) != nil || destOf(e, row.ID) != nil {
		t.Fatal("a stop with a cut request drained")
	}
	if took := time.Since(start); took > stopTimeout {
		t.Fatalf("the cutting stop took %s", took)
	}
	// Consumed: the next plain stop of something else is not cut by it.
	e.mu.RLock()
	left := len(e.cutRequested)
	e.mu.RUnlock()
	if left != 0 {
		t.Fatal("a cut request outlived the stop it was for")
	}
}

// A DESTINATION DELETED WHILE DRAINING IS STILL VISIBLE AND STILL CUTTABLE. It
// is on the air; a status that dropped it with its row would leave the
// operator unable to see it, let alone stop it.
func TestADestinationDeletedWhileDrainingIsListedAndCanBeCut(t *testing.T) {
	e, _ := storeEngine(t)
	row := longDelayRow(t, e)
	startDraining(t, e, row)
	if err := e.store.DeleteDestination(row.ID); err != nil {
		t.Fatal(err)
	}
	st := statusFor(t, e, row.ID)
	if st.Delay == nil || st.Delay.Phase != DelayDraining {
		t.Fatalf("deleted-but-draining status = %+v", st.Delay)
	}
	if !e.CutDelay(row.ID) {
		t.Fatal("Stop now could not find the deleted destination's drain")
	}
	for _, ds := range e.Status().Destinations {
		if ds.ID == row.ID {
			t.Fatal("a cut, deleted destination is still listed")
		}
	}
}

// TWO LINES FOR ONE ID NEVER SHARE A SPILL DIRECTORY, so neither can clear or
// delete the other's buffered programme.
//
// Mutation: drop the nonce from delaySpillDir. The directories then collide
// and closing the first deletes the second's.
func TestTwoLinesForTheSameDestinationHaveTheirOwnSpillDirectories(t *testing.T) {
	e, _ := storeEngine(t)
	row := longDelayRow(t, e)
	a, err := e.buildDelay(row)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.buildDelay(row)
	if err != nil {
		t.Fatal(err)
	}
	if a.dir == b.dir {
		t.Fatalf("both lines spill to %s", a.dir)
	}
	if err := os.WriteFile(filepath.Join(b.dir, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatalf("the second line's directory is not there: %v", err)
	}
	e.closeDelay(a)
	if _, err := os.Stat(filepath.Join(b.dir, "marker")); err != nil {
		t.Fatalf("closing one line deleted the other's spill: %v", err)
	}
	if _, err := os.Stat(a.dir); !os.IsNotExist(err) {
		t.Fatal("a closed line left its own spill behind")
	}
	e.closeDelay(b)
}

// THE STARTUP SWEEP REMOVES ONLY WHAT NO LIVE LINE OWNS.
func TestTheStartupSweepRemovesStaleSpillAndLeavesLiveSpillAlone(t *testing.T) {
	data := t.TempDir()
	root := delayRoot(data)
	stale := filepath.Join(root, "9-1")
	live := filepath.Join(root, "9-2")
	for _, d := range []string{stale, live} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	liveSpill.Store(live, true)
	t.Cleanup(func() { liveSpill.Delete(live) })
	sweepDelayRoot(data)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("the sweep left a previous run's spill behind")
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("the sweep deleted a live line's spill: %v", err)
	}
}

// ONE PUBLISHER PER KEY, ACROSS PROGRAMMES. A destination draining in one
// engine is cut when the same id starts in another engine sharing the
// install's registry.
//
// Mutation: make cutDrainAnywhere return false without cutting. The old
// engine's drain then survives the second start.
func TestStartingADestinationInAnotherEngineCutsItsDrainFirst(t *testing.T) {
	a, _ := storeEngine(t)
	b, _ := storeEngine(t)
	b.drains = a.drains
	rowA := longDelayRow(t, a)
	rowB := longDelayRow(t, b)
	if rowA.ID != rowB.ID {
		t.Fatalf("setup: ids %d and %d differ", rowA.ID, rowB.ID)
	}
	old := startDraining(t, a, rowA)
	b.startDestinations(map[int64]destPlan{rowB.ID: delayPlan(rowB)})
	if drainingOf(a, rowA.ID) != nil {
		t.Fatal("two publishers: the other programme's drain survived a start of the same id")
	}
	if st := old.proc.Status().State; st != supervisor.StateStopped {
		t.Fatalf("the old publisher is %q", st)
	}
	a.Stop()
	b.Stop()
}

// THE DELAY'S PHASES ARE ON THE #17 STREAM, in order: buffering when the
// destination starts, on-air when the delayed data reaches it, draining when
// it is stopped, drained when the tail is out. And none of them carries the
// stream key, which the row in hand has.
func TestTheBroadcastDelayAnnouncesEachPhaseOnTheEventStreamWithoutTheKey(t *testing.T) {
	e, _ := storeEngine(t)
	hub := eventstream.NewHub()
	e.SetStream(hub)
	sub := streamtest.Subscribe(t, hub)
	row := delayedRow(t, e)

	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	var seen []eventstream.Envelope
	expect := func(reason string) {
		t.Helper()
		env := streamtest.Expect(t, sub, eventstream.TypeDestinationDelay)
		if env.Reason != reason {
			t.Fatalf("destination.delay reason = %q, want %q", env.Reason, reason)
		}
		if env.Destination == nil || env.Destination.ID != row.ID {
			t.Fatalf("destination.delay names %+v", env.Destination)
		}
		seen = append(seen, env)
	}
	expect(DelayBuffering)
	go feed(e.hub, 200)
	expect(DelayOnAir)
	time.Sleep(300 * time.Millisecond)
	row.Enabled = false
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	e.stopDestinations(map[int64]destPlan{})
	expect(DelayDraining)
	expect(delayEventDrained)

	for _, env := range seen {
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), row.StreamKey) || strings.Contains(string(raw), row.URL) {
			t.Fatalf("a destination.delay event carries the publish target: %s", raw)
		}
		if !strings.Contains(string(raw), `"seconds":1`) {
			t.Fatalf("destination.delay data = %v, want seconds 1", env.Data)
		}
	}
}

// RETRY WHILE BUFFERING STARTS NOTHING. A delayed destination has no FFmpeg
// until its delayed data is due; a Retry (REST /restart, MCP retry_destination)
// in that window must not start one onto an idle hub, where it would read as
// stalled and spend #20's quick-rejection budget on nothing.
//
// Mutation: delete the `if waiting { return nil }` guard in RestartDestination.
// The process is then started inside a ten-minute delay.
func TestRetryingADelayedDestinationThatIsStillBufferingDoesNotStartItsFFmpeg(t *testing.T) {
	e, _ := storeEngine(t)
	row := longDelayRow(t, e)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	feed(e.hub, 20)
	d := destOf(e, row.ID)
	if err := e.RestartDestination(row.ID); err != nil {
		t.Fatalf("RestartDestination: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if st := d.proc.Status().State; st != supervisor.StateStopped {
		t.Fatalf("Retry started the FFmpeg (%q) inside a ten-minute delay", st)
	}
	if destOf(e, row.ID).delay != d.delay {
		t.Fatal("Retry replaced the buffer")
	}
	e.Stop()
}

// THE ENCODER GOING AWAY STOPS NOTHING. With no new datagrams arriving, the
// buffer keeps releasing what it holds at its own pace, so viewers see the end
// of the programme Delay after the encoder stopped -- not a cut.
func TestWhenTheFeedStopsTheBufferKeepsReleasingForTheDelay(t *testing.T) {
	e, _ := storeEngine(t)
	row := delayedRow(t, e)
	row.DelaySeconds = 2
	if _, err := e.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	d := destOf(e, row.ID)
	feed(e.hub, 300) // ~0.3 s of feed, then the encoder is gone
	stopped := time.Now()
	if got := d.delay.hub.Stats().RxPackets; got != 0 {
		t.Fatalf("%d datagrams released before the delay was up", got)
	}
	eventually(t, "the held programme to play out after the feed stopped", 6*time.Second, func() bool {
		return d.delay.hub.Stats().RxPackets >= 300
	})
	if took := time.Since(stopped); took < 1500*time.Millisecond {
		t.Fatalf("the tail came out %s after the feed stopped; the delay is 2 s", took)
	}
	if destOf(e, row.ID) != d || d.delay.line.Status().Draining {
		t.Fatal("the encoder going away tore the destination down or started a drain")
	}
	e.Stop()
}

// AN UNMEASURED-INGEST RECONCILE DRAINS, IT DOES NOT CUT. When the encoder's
// layout is cleared, the next reconcile holds destinations with an empty plan;
// a delayed destination whose row is still enabled and still there is a plain
// stop, so its tail plays out rather than being discarded.
func TestAHeldReconcileDrainsAnEnabledDelayedDestinationRatherThanCuttingIt(t *testing.T) {
	e, _ := storeEngine(t)
	row := longDelayRow(t, e)
	if err := e.startDest(delayPlan(row), e.hub, 0); err != nil {
		t.Fatalf("startDest: %v", err)
	}
	feed(e.hub, 20)
	e.stopDestinations(map[int64]destPlan{}) // what a held reconcile passes
	if drainingOf(e, row.ID) == nil {
		t.Fatal("a held reconcile cut an enabled delayed destination's tail")
	}
	e.Stop()
}
