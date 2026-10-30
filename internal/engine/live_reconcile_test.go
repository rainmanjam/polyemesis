package engine

import (
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The reconcile's own decision, end to end through stopDestinations: a kept
// destination whose only change is a level is sent the level and stays
// running, and one whose child refuses is restarted with the reason recorded.
// The child is a shell standing in for FFmpeg's command channel.

func liveRow(t *testing.T, store *db.DB, muted bool) *db.Destination {
	t.Helper()
	p := mixProfile(0)
	p.Tracks[0].Muted = muted
	row, err := store.CreateDestination(&db.Destination{
		Name: "live", Kind: db.DestFile, URL: "live.mkv", Enabled: true,
		AudioBitrate: 160, Profile: p,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	return row
}

// shellChild runs script as a destination process with the command channel on.
func shellChild(t *testing.T, script string) *supervisor.Process {
	t.Helper()
	p := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)), supervisor.Spec{
		Name: "dest-live", Kind: "destination", Bin: "/bin/sh", Args: []string{"-c", script},
		LiveCommands: true,
	})
	p.Start()
	deadline := time.Now().Add(5 * time.Second)
	for p.Status().State != supervisor.StateRunning {
		if time.Now().After(deadline) {
			t.Fatal("the stand-in child never reached running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p
}

// liveRunningDest publishes row as a destination running proc from plan p.
func liveRunningDest(e *Engine, row *db.Destination, p destPlan, proc *supervisor.Process) *destination {
	d := &destination{
		row: row, proc: proc, compiled: p.compiled, graph: newGraphHolder(p.compiled), spec: p.spec,
	}
	e.mu.Lock()
	e.dests[row.ID] = d
	e.mu.Unlock()
	return d
}

func planFor(t *testing.T, e *Engine, row *db.Destination) destPlan {
	t.Helper()
	p := e.planDestinations([]*db.Destination{row}, nil, pairSource(), "", false)[row.ID]
	if p.err != "" {
		t.Fatalf("plan: %s", p.err)
	}
	return p
}

// MUTATION: in stopDestinations, skip the liveTry append (treat every kept
// level change as nothing to do). Observed: FAIL, "no outcome recorded".
func TestALevelOnlyChangeGoesToTheRunningProcess(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	before := planFor(t, e, row)

	proc := shellChild(t, `while read l; do echo "Command reply for stream 0: ret:0 res:" >&2; done`)
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	d := liveRunningDest(e, row, before, proc)

	muted := *row
	muted.Profile = mixProfile(0)
	muted.Profile.Tracks[0].Muted = true
	after := planFor(t, e, &muted)
	if after.spec != before.spec {
		t.Fatal("a mute moved the restart signature; it can never be live")
	}

	e.stopDestinations(map[int64]destPlan{row.ID: after})

	applied, reason, ok := e.DestinationLive(row.ID)
	if !ok {
		t.Fatal("no outcome recorded for a level change")
	}
	if applied != liveApplied {
		t.Fatalf("applied = %q (%s), want live", applied, reason)
	}
	e.mu.RLock()
	cur := e.dests[row.ID]
	e.mu.RUnlock()
	if cur == nil || cur.proc != proc {
		t.Fatal("the destination was replaced; a live change must keep the same process")
	}
	if proc.Status().State != supervisor.StateRunning {
		t.Fatalf("the process is %s after a live change", proc.Status().State)
	}
	if !strings.Contains(d.graph.get().FilterComplex, "volume@trk0=volume=0") {
		t.Errorf("the respawn graph does not carry the mute: %s", d.graph.get().FilterComplex)
	}
	if !strings.Contains(cur.compiled.FilterComplex, "volume@trk0=volume=0") {
		t.Errorf("the published graph does not carry the mute: %s", cur.compiled.FilterComplex)
	}
}

// Restart is the fallback: a refusal tears the destination down so the next
// start builds it with the new level, and the reason is kept for the operator.
//
// MUTATION: in stopDestinations, on a live error `continue` without queuing
// the stop. Observed: FAIL, "a refused level change left the process running".
func TestARefusedLevelChangeRestartsAndSaysWhy(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	before := planFor(t, e, row)

	proc := shellChild(t, `while read l; do echo "Command reply for stream 0: ret:-78 res:" >&2; done`)
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	liveRunningDest(e, row, before, proc)

	muted := *row
	muted.Profile = mixProfile(0)
	muted.Profile.Tracks[0].Muted = true
	e.stopDestinations(map[int64]destPlan{row.ID: planFor(t, e, &muted)})

	applied, reason, _ := e.DestinationLive(row.ID)
	if applied != liveRestart || reason == "" {
		t.Fatalf("applied = %q reason = %q, want restart with a reason", applied, reason)
	}
	e.mu.RLock()
	_, still := e.dests[row.ID]
	e.mu.RUnlock()
	if still {
		t.Error("a refused level change left the destination in place; it must be restarted")
	}
	if proc.Status().State == supervisor.StateRunning {
		t.Error("a refused level change left the process running")
	}

	// And the status says so, where the dashboard reads it.
	for _, ds := range e.Status().Destinations {
		if ds.ID == row.ID && !strings.HasPrefix(ds.LevelChange, "applied by restart: ") {
			t.Errorf("status levelChange = %q, want it to say the restart and why", ds.LevelChange)
		}
	}
}

// PERSIST FIRST, THEN A RESTART CANNOT UNDO IT. The mute is written to the
// store; any later restart -- a failover, a hub change, a crash past the
// holder -- plans from the stored row, so the argv it spawns carries the mute.
// The level was never only in the running process.
//
// MUTATION: trackVolume returns trackGain without checking Muted. Observed:
// FAIL, "the restarted argv plays the muted track".
func TestAMuteSurvivesARestart(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)

	// The API's order: read, set the mute, write.
	stored, err := store.GetDestination(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Profile.Tracks[0].Muted = true
	if _, err := store.UpdateDestination(stored); err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}

	// A restart: the destination is planned again from what the store holds.
	reread, err := store.GetDestination(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := planFor(t, e, reread)
	argv := strings.Join(e.destArgs(reread, p.compiled, "udp://127.0.0.1:1", reread.Target()), " ")
	if !regexp.MustCompile(`volume@trk0=volume=0([^0-9.]|$)`).MatchString(argv) {
		t.Errorf("the restarted argv plays the muted track:\n%s", argv)
	}

	// And unmuting is just as durable.
	reread.Profile.Tracks[0].Muted = false
	if _, err := store.UpdateDestination(reread); err != nil {
		t.Fatal(err)
	}
	again, _ := store.GetDestination(row.ID)
	argv = strings.Join(e.destArgs(again, planFor(t, e, again).compiled, "udp://127.0.0.1:1", again.Target()), " ")
	if !regexp.MustCompile(`volume@trk0=volume=1([^0-9.]|$)`).MatchString(argv) {
		t.Errorf("an unmute did not survive a restart:\n%s", argv)
	}
}

// Whether a destination has a process the live path could reach. The API uses
// it to say "not running" rather than "restarted" when neither happened.
func TestDestinationRunning(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	if e.DestinationRunning(row.ID) {
		t.Fatal("a destination with no entry reads as running")
	}
	proc := shellChild(t, `while read l; do :; done`)
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	liveRunningDest(e, row, planFor(t, e, row), proc)
	if !e.DestinationRunning(row.ID) {
		t.Fatal("a destination with a process reads as not running")
	}
}

// A level change that rides with a structural one (here a second track
// joining the mix) restarts, and the outcome says so. Without a note the API
// could not tell this apart from a change that touched nothing running.
//
// MUTATION: drop the structural-restart note in stopDestinations. Observed:
// FAIL, "no outcome recorded".
func TestALevelChangeWithAStructuralOneRecordsTheRestart(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	before := planFor(t, e, row)

	proc := shellChild(t, `while read l; do echo "Command reply for stream 0: ret:0 res:" >&2; done`)
	t.Cleanup(func() { _ = proc.Stop(t.Context()) })
	liveRunningDest(e, row, before, proc)

	wider := *row
	wider.Profile = mixProfile(0)
	wider.Profile.Tracks[0].Muted = true
	wider.Profile.Tracks = append(wider.Profile.Tracks, routing.TrackSel{Track: 1, Enabled: true, Gain: 1})
	after := planFor(t, e, &wider)
	if after.spec == before.spec {
		t.Fatal("adding a track did not move the signature; the test is not testing a structural change")
	}
	e.stopDestinations(map[int64]destPlan{row.ID: after})

	applied, reason, ok := e.DestinationLive(row.ID)
	if !ok {
		t.Fatal("no outcome recorded for a level change that restarted")
	}
	if applied != liveRestart || reason == "" {
		t.Errorf("applied = %q reason = %q, want restart with a reason", applied, reason)
	}
}
