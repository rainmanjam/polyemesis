package engine

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// A level applied live lives only in the running graph, and FFmpeg rebuilds that
// graph from its argv whenever the decoded format changes (see the note above
// argvGraph, and TestALiveMuteSurvivesAFormatChange for the real FFmpeg). These
// tests pin the repair: at a format event, a process whose argv levels a live
// change has left stale is respawned, and the respawn's argv carries the stored
// level.
//
// The stand-in child is this test binary re-executed (TestMain's
// engineFakeChildFlag branch), so they run on every platform, Windows included,
// and need no stdin path: the live change is modelled by moving the holder,
// which is exactly the state liveApplyTo leaves behind.

// levelChild starts a stand-in destination whose argv carries the graph its
// holder held at each spawn, the way destinations.go builds a real one.
func levelChild(t *testing.T, holder *graphHolder) *supervisor.Process {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary to re-execute as a child: %v", err)
	}
	argv := func() []string {
		return []string{engineFakeChildFlag, "-filter_complex", holder.get().FilterComplex}
	}
	p := supervisor.New(testLogger(), supervisor.Spec{
		Name: "dest-level", Kind: "destination", Bin: self, Args: argv(), NextArgs: argv,
	})
	p.Start()
	t.Cleanup(func() { _ = p.Stop(t.Context()) })
	waitRunning(t, p)
	return p
}

func waitRunning(t *testing.T, p *supervisor.Process) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for p.Status().State != supervisor.StateRunning {
		if time.Now().After(deadline) {
			t.Fatalf("the stand-in child never reached running (state %q)", p.Status().State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var argvMutedTrk0 = regexp.MustCompile(`volume@trk0=volume=0([^0-9.]|$)`)

// stagedLiveMute publishes a running destination and then mutes track 0 the way
// a live apply does: the holder moves, the running argv does not.
func stagedLiveMute(t *testing.T, e *Engine, store *db.DB) (*supervisor.Process, *graphHolder) {
	t.Helper()
	row := liveRow(t, store, false)
	before := planFor(t, e, row)
	holder := newGraphHolder(before.compiled)
	proc := levelChild(t, holder)
	e.mu.Lock()
	e.dests[row.ID] = &destination{row: row, proc: proc, compiled: before.compiled, graph: holder, spec: before.spec}
	e.mu.Unlock()

	muted := *row
	muted.Profile = mixProfile(0)
	muted.Profile.Tracks[0].Muted = true
	holder.set(planFor(t, e, &muted).compiled)
	if argvMutedTrk0.MatchString(strings.Join(proc.Args(), " ")) {
		t.Fatal("the running argv already carries the mute; the test is not staging a stale level")
	}
	if !levelsStale(proc, holder) {
		t.Fatal("a live mute did not read as stale against the spawned argv")
	}
	return proc, holder
}

func waitArgvMuted(t *testing.T, proc *supervisor.Process, why string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !argvMutedTrk0.MatchString(strings.Join(proc.Args(), " ")) || proc.Status().State != supervisor.StateRunning {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the process was not respawned with the stored mute; argv is still\n%s",
				why, strings.Join(proc.Args(), " "))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// MUTATION: make respawnStaleLevels return without restarting anything.
// Observed: FAIL, "the process was not respawned with the stored mute".
func TestAStaleLiveLevelIsRespawnedFromTheHolder(t *testing.T) {
	e, store := storeEngine(t)
	proc, holder := stagedLiveMute(t, e, store)

	e.respawnStaleLevels("test: the source feed changed")
	waitArgvMuted(t, proc, "a format event")
	if levelsStale(proc, holder) {
		t.Error("after the respawn the argv still disagrees with the holder")
	}
}

// The ingest listeners' state callback is a format event: each spawn is a new
// streamer session, which may arrive at another rate with no selector switch.
//
// MUTATION: drop the respawnStaleLevels call from onUpstreamState. Observed:
// FAIL, "the process was not respawned with the stored mute".
func TestANewIngestSessionRespawnsAStaleLevel(t *testing.T) {
	e, store := storeEngine(t)
	proc, _ := stagedLiveMute(t, e, store)

	e.onUpstreamState(supervisor.Status{Name: "ingest", State: supervisor.StateRunning})
	waitArgvMuted(t, proc, "an ingest session")
}

// The respawn is bounded to destinations a live change has left stale. One
// nobody changed is never cycled by a failover, which would cost every output
// a reconnect at exactly the moment the tier exists to hide.
//
// MUTATION: make levelsStale return true. Observed: FAIL, "a destination with
// no live change was restarted".
func TestAFormatEventLeavesAnUnchangedDestinationAlone(t *testing.T) {
	e, store := storeEngine(t)
	row := liveRow(t, store, false)
	p := planFor(t, e, row)
	holder := newGraphHolder(p.compiled)
	proc := levelChild(t, holder)
	e.mu.Lock()
	e.dests[row.ID] = &destination{row: row, proc: proc, compiled: p.compiled, graph: holder, spec: p.spec}
	e.mu.Unlock()
	pid := proc.Status().PID

	e.respawnStaleLevels("test: the source feed changed")
	e.onUpstreamState(supervisor.Status{Name: "ingest", State: supervisor.StateRunning})
	time.Sleep(300 * time.Millisecond)
	if got := proc.Status(); got.PID != pid || got.State != supervisor.StateRunning {
		t.Errorf("a destination with no live change was restarted: pid %d -> %d, state %s", pid, got.PID, got.State)
	}
}

// The three format events are wired. A unit test of each callee cannot see a
// call site that was dropped, and the selector's feed swap has no seam a unit
// test can drive without the failover rig, so the wiring is pinned in source.
//
// MUTATION: revert either ingest spec's OnState to e.onState, or delete the
// call in ensureFeed. Observed: FAIL naming the missing site.
func TestEveryFormatEventIsWiredToTheStaleLevelRespawn(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	engineSrc, selectorSrc := read("engine.go"), read("selector.go")
	if n := strings.Count(engineSrc+selectorSrc, "OnState: e.onUpstreamState"); n != 2 {
		t.Errorf("%d ingest specs use onUpstreamState; want 2 (the primary and the backup listener)", n)
	}
	start := strings.Index(selectorSrc, "func (e *Engine) ensureFeed(")
	if start < 0 {
		t.Fatal("ensureFeed not found in selector.go")
	}
	body := selectorSrc[start:]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end+1]
	}
	if !strings.Contains(body, "e.respawnStaleLevels(") {
		t.Error("ensureFeed no longer calls respawnStaleLevels after a feed change")
	}
}
