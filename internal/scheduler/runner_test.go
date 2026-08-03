package scheduler

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeStore struct {
	rows   []Schedule
	err    error
	marked map[int64]time.Time
	markEr error
}

func (f *fakeStore) Schedules() ([]Schedule, error) { return f.rows, f.err }

func (f *fakeStore) MarkScheduleRun(id int64, at time.Time) error {
	if f.markEr != nil {
		return f.markEr
	}
	if f.marked == nil {
		f.marked = map[int64]time.Time{}
	}
	f.marked[id] = at
	// Mirror what the database does, so a second Tick in the same test sees the
	// schedule as handled.
	for i := range f.rows {
		if f.rows[i].ID == id {
			f.rows[i].LastRunAt = at
		}
	}
	return nil
}

type fakeActuator struct {
	all       []int64
	allErr    error
	setErr    error
	set       map[int64]bool
	setCalls  int
	reconcile int

	// playlistEnabled is the stored value. playlistSet is tracked separately
	// because a test asserting only playlistEnabled cannot tell "disabled
	// because the schedule ran" from "never touched".
	playlistEnabled bool
	playlistSet     bool
	playlistErr     error
}

func (f *fakeActuator) SetDestinationEnabled(id int64, enabled bool) error {
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	if f.set == nil {
		f.set = map[int64]bool{}
	}
	f.set[id] = enabled
	return nil
}

func (f *fakeActuator) ListDestinationIDs() ([]int64, error) { return f.all, f.allErr }

func (f *fakeActuator) SetPlaylistEnabled(enabled bool) error {
	if f.playlistErr != nil {
		return f.playlistErr
	}
	f.playlistEnabled = enabled
	f.playlistSet = true
	return nil
}

func (f *fakeActuator) Reconcile() error { f.reconcile++; return nil }

func oneShot(id int64, at time.Time, mut ...func(*Schedule)) Schedule {
	s := Schedule{
		ID: id, Name: "the broadcast", Enabled: true, Action: ActionStart,
		Kind: KindOnce, RunAt: at, GraceSeconds: 300,
		DestinationIDs: []int64{4, 7},
	}
	for _, m := range mut {
		m(&s)
	}
	return s.Normalized()
}

func TestTickFiresThroughTheEnableDisablePathAndReconcilesOnce(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		action      Action
		wantEnabled bool
	}{
		{name: "start enables", action: ActionStart, wantEnabled: true},
		{name: "stop disables", action: ActionStop, wantEnabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{rows: []Schedule{oneShot(1, at, func(s *Schedule) { s.Action = tt.action })}}
			act := &fakeActuator{}
			r := New(quietLog(), store, act)

			got := r.Tick(at.Add(30 * time.Second))
			if len(got) != 1 || !got[0].Fired {
				t.Fatalf("Tick = %+v, want one fired schedule", got)
			}
			if len(act.set) != 2 || act.set[4] != tt.wantEnabled || act.set[7] != tt.wantEnabled {
				t.Errorf("destinations set to %v, want both %v", act.set, tt.wantEnabled)
			}
			if act.reconcile != 1 {
				t.Errorf("Reconcile called %d times, want exactly 1 per sweep", act.reconcile)
			}
			if !store.marked[1].Equal(at) {
				t.Errorf("marked %s, want the occurrence %s", store.marked[1], at)
			}
		})
	}
}

func TestTickDoesNotFireTheSameOccurrenceTwice(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{oneShot(1, at)}}
	act := &fakeActuator{}
	r := New(quietLog(), store, act)

	r.Tick(at.Add(10 * time.Second))
	before := act.setCalls
	r.Tick(at.Add(40 * time.Second))
	if act.setCalls != before {
		t.Errorf("the second sweep acted again: %d calls, want %d", act.setCalls, before)
	}
}

// The behaviour that keeps a server that was down overnight from starting a
// stream at four in the morning.
func TestTickMarksAMissedWindowHandledWithoutActingOnIt(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{oneShot(1, at)}}
	act := &fakeActuator{}
	r := New(quietLog(), store, act)

	got := r.Tick(at.Add(4 * time.Hour))
	if len(got) != 1 {
		t.Fatalf("Tick = %+v, want one result", got)
	}
	if got[0].Fired || !got[0].Skipped {
		t.Errorf("result = %+v, want skipped and not fired", got[0])
	}
	if act.setCalls != 0 {
		t.Errorf("a missed window touched %d destinations, want 0", act.setCalls)
	}
	if act.reconcile != 0 {
		t.Errorf("a missed window reconciled %d times, want 0", act.reconcile)
	}
	if !store.marked[1].Equal(at) {
		t.Errorf("the missed occurrence was not recorded as handled, so it will fire later")
	}
}

func TestTickExpandsAnEmptyTargetListToEveryDestination(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{oneShot(1, at, func(s *Schedule) { s.DestinationIDs = nil })}}
	act := &fakeActuator{all: []int64{1, 2, 3}}
	r := New(quietLog(), store, act)

	got := r.Tick(at.Add(time.Second))
	if len(got) != 1 || len(got[0].Targets) != 3 {
		t.Fatalf("Tick = %+v, want all three destinations", got)
	}
	if len(act.set) != 3 {
		t.Errorf("set %v, want all three", act.set)
	}
}

func TestTickLeavesTheOccurrenceForTheNextSweepWhenTheTargetsCannotBeListed(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{oneShot(1, at, func(s *Schedule) { s.DestinationIDs = nil })}}
	act := &fakeActuator{allErr: errors.New("database is locked")}
	r := New(quietLog(), store, act)

	got := r.Tick(at.Add(time.Second))
	if len(got) != 1 || got[0].Err == "" {
		t.Fatalf("Tick = %+v, want the failure reported", got)
	}
	if got[0].Fired {
		t.Error("a schedule that could not find its targets reported as fired")
	}
	if _, marked := store.marked[1]; marked {
		t.Error("the occurrence was marked handled, so the retry inside the grace window is lost")
	}
}

func TestTickMarksHandledEvenWhenOneDestinationFailed(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{oneShot(1, at)}}
	act := &fakeActuator{setErr: errors.New("no such destination")}
	r := New(quietLog(), store, act)

	got := r.Tick(at.Add(time.Second))
	if len(got) != 1 || got[0].Err == "" {
		t.Fatalf("Tick = %+v, want the failure reported", got)
	}
	if _, marked := store.marked[1]; !marked {
		t.Error("re-running the occurrence would re-apply it to the destinations that worked")
	}
	if act.reconcile != 0 {
		t.Error("nothing changed, so nothing should have been reconciled")
	}
}

func TestTickSkipsSchedulesWithNothingToDo(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{
		oneShot(1, at.Add(time.Hour)),                             // not yet
		oneShot(2, at, func(s *Schedule) { s.Enabled = false }),   // switched off
		oneShot(3, at, func(s *Schedule) { s.Kind = KindWeekly }), // invalid
		oneShot(4, at, func(s *Schedule) { s.LastRunAt = at }),    // handled
	}}
	act := &fakeActuator{}
	r := New(quietLog(), store, act)

	if got := r.Tick(at.Add(time.Second)); len(got) != 0 {
		t.Fatalf("Tick = %+v, want nothing to do", got)
	}
	if act.setCalls != 0 || act.reconcile != 0 {
		t.Errorf("the actuator was touched: %d sets, %d reconciles", act.setCalls, act.reconcile)
	}
}

func TestTickSurvivesAStoreThatCannotBeRead(t *testing.T) {
	store := &fakeStore{err: errors.New("database is locked")}
	act := &fakeActuator{}
	r := New(quietLog(), store, act)

	if got := r.Tick(time.Now()); got != nil {
		t.Errorf("Tick = %+v, want nothing when the schedules cannot be read", got)
	}
	if act.reconcile != 0 {
		t.Error("a failed read still reconciled")
	}
}

func TestOnResultIsCalledForEveryScheduleThatActed(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{
		oneShot(1, at),
		oneShot(2, at.Add(-4*time.Hour)),
	}}
	var seen []Result
	r := New(quietLog(), store, &fakeActuator{},
		WithOnResult(func(res Result) { seen = append(seen, res) }))

	r.Tick(at.Add(time.Second))
	if len(seen) != 2 {
		t.Fatalf("OnResult called %d times, want 2 (one fired, one skipped)", len(seen))
	}
	if !seen[0].Fired || !seen[1].Skipped {
		t.Errorf("results = %+v, want a fire then a skip", seen)
	}
}

// The feature: a playlist schedule flips the stored setting.
//
// Asserted on what was STORED, not on a call being recorded, because the
// invariant this package is built on is that a schedule writes the same
// intent a human writes. A fake that merely counts calls would pass while the
// runner did something else entirely.
//
// The mutation: delete the TargetsPlaylist branch in Tick and this fails.
func TestAPlaylistScheduleEnablesThePlaylist(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{{
		ID: 1, Name: "filler", Enabled: true,
		Action: ActionPlaylistStart, Kind: KindOnce,
		RunAt: at, GraceSeconds: 60,
	}}}
	act := &fakeActuator{}
	r := New(quietLog(), store, act)

	res := r.Tick(at.Add(time.Second))

	if len(res) != 1 || !res[0].Fired {
		t.Fatalf("results = %+v, want one fired", res)
	}
	if !act.playlistSet || !act.playlistEnabled {
		t.Error("the playlist schedule fired and the playlist was not enabled")
	}
}

// And it must RECONCILE. Tick only reconciles when `changed` is set, and the
// only thing that sets it today is a destination flip. A playlist flip that
// forgets it writes the setting and reconciles nothing, so the playlist
// starts whenever some unrelated event next happens to reconcile -- the exact
// silent-until-something-else-happens failure sub-project B1 rewrote the
// readiness gate to avoid.
//
// The mutation: remove `changed = true` from the playlist branch and this fails.
func TestAPlaylistScheduleAsksForAReconcile(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{{
		ID: 1, Name: "filler", Enabled: true,
		Action: ActionPlaylistStart, Kind: KindOnce,
		RunAt: at, GraceSeconds: 60,
	}}}
	act := &fakeActuator{}
	r := New(quietLog(), store, act)

	r.Tick(at.Add(time.Second))

	if act.reconcile != 1 {
		t.Errorf("reconciles = %d, want 1: the setting was written and nothing applied it", act.reconcile)
	}
}

// playlist.stop disables the playlist AND TOUCHES NO DESTINATION.
//
// Enables() answers false for it, and the destination path reads Enables(): if
// a playlist action ever reached that path it would disable every destination
// in the install. This is the test that catches it.
//
// The mutation: route by Enables() instead of TargetsPlaylist and this fails
// with destinations disabled.
func TestPlaylistStopDisablesThePlaylistAndNoDestinations(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	// `all` MUST be populated, and that is the difference between this test
	// guarding the hazard and merely appearing to.
	//
	// A playlist schedule carries no DestinationIDs, so a misroute into the
	// destination path takes the "empty means every destination" branch and asks
	// ListDestinationIDs for the list. With an empty `all` that returns nothing,
	// no destination is flipped, and the assertion below passes while the
	// misroute it exists to catch is happening. The fixture has to be able to
	// PRODUCE the failure before the assertion means anything.
	act := &fakeActuator{playlistEnabled: true, all: []int64{7, 8}}
	store := &fakeStore{rows: []Schedule{{
		ID: 1, Name: "stop filler", Enabled: true,
		Action: ActionPlaylistStop, Kind: KindOnce,
		RunAt: at, GraceSeconds: 60,
	}}}
	r := New(quietLog(), store, act)

	r.Tick(at.Add(time.Second))

	if act.playlistEnabled {
		t.Error("playlist.stop fired and the playlist is still enabled")
	}
	if act.setCalls != 0 || len(act.set) != 0 {
		t.Errorf("playlist.stop disabled destinations %v in %d calls. Enables() answers "+
			"false for playlist.stop and the destination path reads Enables(), so a "+
			"playlist action reaching that path takes every destination in the install "+
			"off air", act.set, act.setCalls)
	}
}

// A failed settings write leaves the occurrence UNHANDLED, so the next sweep
// retries it inside its grace window.
//
// This differs from the destination path on purpose. There, a partial
// failure is marked handled because retrying would re-apply the flip to the
// ones that worked. Here there is no partial: one write either lands or does
// not.
//
// The mutation: mark the occurrence handled on error and this fails.
func TestAFailedPlaylistWriteIsRetriedOnTheNextSweep(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	act := &fakeActuator{playlistErr: errors.New("disk is full")}
	store := &fakeStore{rows: []Schedule{{
		ID: 1, Name: "filler", Enabled: true,
		Action: ActionPlaylistStart, Kind: KindOnce,
		RunAt: at, GraceSeconds: 600,
	}}}
	r := New(quietLog(), store, act)

	res := r.Tick(at.Add(time.Second))

	if len(res) != 1 || res[0].Err == "" {
		t.Fatalf("results = %+v, want one carrying the error", res)
	}
	if len(store.marked) != 0 {
		t.Error("the occurrence was marked handled after a failed write, so the next " +
			"sweep will not retry it inside its grace window")
	}
}

// A missed playlist occurrence is skipped and marked handled, exactly as a
// missed destination one is. The skip rule generalising is a claim, and
// claims get tested.
//
// The mutation: exempt playlist actions from the skip branch and this fails.
func TestAMissedPlaylistOccurrenceIsSkippedNotFiredLate(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	act := &fakeActuator{}
	store := &fakeStore{rows: []Schedule{{
		ID: 1, Name: "filler", Enabled: true,
		Action: ActionPlaylistStart, Kind: KindOnce,
		RunAt: at, GraceSeconds: 60,
	}}}
	r := New(quietLog(), store, act)

	res := r.Tick(at.Add(10 * time.Minute))

	if len(res) != 1 || !res[0].Skipped {
		t.Fatalf("results = %+v, want one skipped", res)
	}
	if act.playlistSet {
		t.Error("a missed occurrence started the playlist late; the only thing worse " +
			"than not starting is starting late")
	}
}

func TestLastReportsTheMostRecentSweepThatDidSomething(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{oneShot(1, at)}}
	r := New(quietLog(), store, &fakeActuator{})

	if got := r.Last(); len(got) != 0 {
		t.Fatalf("Last before any sweep = %+v, want empty", got)
	}
	r.Tick(at.Add(time.Second))
	if got := r.Last(); len(got) != 1 || !got[0].Fired {
		t.Fatalf("Last = %+v, want the fired schedule", got)
	}
	// A sweep with nothing to do must not erase the record of the last one.
	r.Tick(at.Add(2 * time.Second))
	if got := r.Last(); len(got) != 1 {
		t.Errorf("Last after an idle sweep = %+v, want the previous result kept", got)
	}
}
