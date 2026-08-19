package engine

import (
	"io"
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// TestStatusReportsAProcessForADestinationOnItsWayOut is #462.
//
// stopDestinations deletes the entry from e.dests, releases e.mu, and only then
// calls teardownDest -- which does not ask the child to stop until much further
// in. For the whole of that window the process is alive and belongs to nothing,
// and a status read landing there published a NIL Process for a destination that
// was still delivering. The acceptance suite prints that as "no destination
// process was reported; nothing was measured", which reads as a death. Measured
// at 1 run in 6 against a suite that reads status across a reconcile.
//
// ASSERTED THROUGH Status(), which is the only place the defect was ever
// visible. A first attempt asserted around stopDestinations instead -- that the
// map got an entry, that the entry had a process -- and every one of those
// assertions survived deleting the whole fix, because stopDestinations clears
// the record before it returns and the window cannot be seen from outside it.
// A test that cannot fail is worse than no test, so those went.
func TestStatusReportsAProcessForADestinationOnItsWayOut(t *testing.T) {
	e := failoverEngine(t)
	saved, err := e.store.CreateDestination(&db.Destination{
		Name: "onair", Kind: db.DestFile, URL: "onair.ts", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	proc := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		supervisor.Spec{Name: "dest:1", Kind: "destination", Bin: "true"})

	// EXACTLY the state stopDestinations leaves between its unlock and the child
	// being asked to stop: gone from the running set, still holding a process.
	if e.retiring == nil {
		e.retiring = map[int64]*destination{}
	}
	e.retiring[saved.ID] = &destination{
		row: saved, proc: proc,
		compiled: routing.Result{Summary: "stereo", FilterComplex: "[0:a:0]anull[aout]"},
	}

	var got *DestStatus
	for i := range e.Status().Destinations {
		if d := &e.Status().Destinations[i]; d.ID == saved.ID {
			got = d
			break
		}
	}
	if got == nil {
		t.Fatalf("destination %d is missing from the status payload entirely", saved.ID)
	}
	if got.Process == nil {
		t.Fatal("Status reported no process for a destination whose child is alive. " +
			"Every consumer reads that as the destination having died -- the dashboard " +
			"shows it dead, and the acceptance suites report 'no destination process " +
			"was reported; nothing was measured'. That is #462.")
	}
	if !got.Transitioning {
		t.Error("the process is reported but not the fact that it is on its way out. " +
			"Reporting it as ordinarily live is the opposite lie: a destination being " +
			"torn down would look healthy for the length of the teardown.")
	}
}

// And a destination that is neither running nor retiring must still report no
// process, or the flag above would be worthless -- everything would look alive.
func TestStatusStillReportsNoProcessForADestinationThatIsNotRunning(t *testing.T) {
	e := failoverEngine(t)
	saved, err := e.store.CreateDestination(&db.Destination{
		Name: "idle", Kind: db.DestFile, URL: "idle.ts", Enabled: false,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	for _, d := range e.Status().Destinations {
		if d.ID != saved.ID {
			continue
		}
		if d.Process != nil {
			t.Errorf("a destination that has never run reports a process: %+v", d.Process)
		}
		if d.Transitioning {
			t.Error("a destination that has never run is marked transitioning")
		}
		return
	}
	t.Fatalf("destination %d is missing from the status payload", saved.ID)
}
