package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A DESTINATION WHOSE PROGRAMME HAS NO ENGINE STILL HAS TO BE REPORTED (#540).
//
// Sync logs and carries on when a source cannot be brought up, so its rows stay
// in the database with nothing to describe them. The concatenation in
// DestinationStatuses walks REGISTERED engines only, so those destinations
// vanished from the dashboard, from the WebSocket push and from the scrape --
// while GET /destinations, which is store-backed, went on listing them. Two
// screens disagreeing, and the disappearance looks exactly like a destination
// nobody ever configured.
//
// The row comes back with the REASON attached rather than as a silent zero,
// which is the difference between "this destination is down" and "this
// destination is not being looked at".
//
// Mutation: delete the orphan sweep and return after the engine loop. Observed
// to fail with "a destination whose programme has no engine is missing".
func TestDestinationStatusesReportsRowsNoEngineSpeaksFor(t *testing.T) {
	m, store := managerFixture(t)

	live := addSource(t, store, "a programme that runs")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Added to the store AFTER Start and never Sync'd, so the row exists and no
	// engine is registered for it. That is the same end state as engine.New or
	// Engine.Start failing for one source -- the case this is about -- and it is
	// the one a test can produce without breaking ffmpeg on purpose.
	dark := &db.Source{Name: "a programme with no engine", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(dark); err != nil {
		t.Fatalf("CreateSource(dark): %v", err)
	}

	for _, e := range m.Engines() {
		if e.SourceID() == dark.ID {
			t.Fatalf("the disabled source got an engine, so this test cannot "+
				"produce the state it is about (source %d)", dark.ID)
		}
	}

	orphan, err := store.CreateDestination(&db.Destination{
		Name: "nothing is publishing this", Kind: db.DestFile, URL: "orphan.mkv",
		Enabled: true, AudioBitrate: 160, SourceID: &dark.ID,
	})
	if err != nil {
		t.Fatalf("CreateDestination(orphan): %v", err)
	}
	kept, err := store.CreateDestination(&db.Destination{
		Name: "this one has an engine", Kind: db.DestFile, URL: "kept.mkv",
		Enabled: false, AudioBitrate: 160, SourceID: &live.ID,
	})
	if err != nil {
		t.Fatalf("CreateDestination(kept): %v", err)
	}

	byID := map[int64]DestStatus{}
	for _, d := range m.DestinationStatuses() {
		byID[d.ID] = d
	}

	got, ok := byID[orphan.ID]
	if !ok {
		t.Fatal("a destination whose programme has no engine is missing from the " +
			"whole-install view, so the dashboard and the scrape drop it while " +
			"GET /destinations still lists it")
	}
	// Error, not Warnings: nothing about this destination is running or can be
	// made to run, and an amber caveat would read as "delivering, with a note".
	if got.Error == "" {
		t.Error("the orphan is reported with no reason attached, which reads as a " +
			"destination that is merely down rather than one nothing is looking at")
	}
	if !strings.Contains(got.Error, "no running engine") {
		t.Errorf("the reason does not name the cause: %q", got.Error)
	}
	// The control: an implementation that reported EVERY store row as an orphan
	// would satisfy the assertions above.
	if k, ok := byID[kept.ID]; !ok {
		t.Fatal("the destination that does have an engine is missing, so the " +
			"assertions above prove nothing")
	} else if k.Error != "" {
		t.Errorf("a destination with a running engine was reported as an orphan: %q", k.Error)
	}
}

// Scheduler is reachable BEFORE Start, and on a nil Manager.
//
// It is built in NewManager rather than in Start so the runs page reads Last()
// and renders an empty report instead of nothing at all, and it is nil-safe so
// an API reading it during boot does not panic. Both are load-bearing claims
// that nothing exercised.
func TestSchedulerIsUsableBeforeStartAndOnNil(t *testing.T) {
	var nilMgr *Manager
	if got := nilMgr.Scheduler(); got != nil {
		t.Errorf("a nil Manager answered Scheduler() with %v, want nil -- the API "+
			"reads this during boot", got)
	}

	m, _ := managerFixture(t)
	if m.Scheduler() == nil {
		t.Fatal("Scheduler() is nil before Start, so the runs page has nothing to " +
			"read and renders no report at all rather than an empty one")
	}
	// One runner for the install, not one per engine -- asserted here only as
	// identity, since TestOneSchedulerForTheInstallNotOnePerEngine covers the
	// behaviour it protects.
	if m.Scheduler() != m.Scheduler() {
		t.Error("Scheduler() hands out a different runner each call")
	}
}
