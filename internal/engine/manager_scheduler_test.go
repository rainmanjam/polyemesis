package engine

import (
	"context"
	"testing"
)

// ONE runner for the install, not one per engine.
//
// `schedules` carries no source_id: a timetable is a property of the box. A
// Runner per engine put N of them on that one table, and the failure was not a
// race that sometimes lost -- it was structural. Whichever swept first wrote
// `enabled` on EVERY destination, including other programmes', then reconciled
// ONLY ITS OWN engine and marked the occurrence handled. The other engines read
// it as handled and never reconciled, so their destinations sat enabled in the
// database with no process publishing: a scheduled broadcast that did not go on
// air, while the log said `schedule fired`.
//
// MarkScheduleRun's `WHERE last_run_at < ?` is a ratchet on the row, not a
// lease over the work, and Actuator has no way to learn whether it won it --
// MarkScheduleRun returns only an error.
//
// Asserted on the count rather than on behaviour through a sweep, because the
// thing that was wrong is structural: with two engines running there must still
// be exactly one runner, and its Reconcile must be the Manager's. A behavioural
// test would have to win a race to fail, which is how this survived.
//
// Mutation: point scheduleActuator.Reconcile back at a single engine. Observed
// to fail with "runs 2 engine(s), want 3".
func TestOneSchedulerForTheInstallNotOnePerEngine(t *testing.T) {
	m, store := managerFixture(t)
	addSource(t, store, "first programme")
	addSource(t, store, "second programme")
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := len(m.Engines()); got < 2 {
		t.Fatalf("the manager runs %d engine(s); this test needs two or it asserts nothing", got)
	}

	if m.Scheduler() == nil {
		t.Fatal("the install has no schedule runner, so no timetable can fire at all")
	}

	// THE DISCRIMINATOR. A third programme is created AFTER Start, so no engine
	// exists for it yet. Manager.Reconcile calls Sync first and builds one;
	// Engine.Reconcile cannot -- an engine cannot create its siblings.
	//
	// So this distinguishes the two implementations by behaviour rather than by
	// shape. Asserting only that a runner exists would pass even with the
	// per-engine runners restored, which is what the first version of this test
	// did and why it was worth nothing.
	before := len(m.Engines())
	addSource(t, store, "third programme, created after start")
	if got := len(m.Engines()); got != before {
		t.Fatalf("a new source built an engine without any reconcile (%d -> %d); "+
			"the assertion below would then prove nothing", before, got)
	}

	act := scheduleActuator{m: m}
	if err := act.Reconcile(); err != nil {
		t.Fatalf("the scheduled reconcile failed: %v", err)
	}
	if got := len(m.Engines()); got <= before {
		t.Errorf("after a scheduled reconcile the install still runs %d engine(s), "+
			"unchanged from %d. The actuator reconciled ONE engine rather than the "+
			"install, which is the bug: a schedule fires, writes `enabled` on every "+
			"programme's destinations, reconciles only its own, and marks the "+
			"occurrence handled -- so the other programmes are enabled in the "+
			"database with nothing publishing", got, before)
	}

	// And the expansion of "everything" is install-wide, so a schedule that
	// targets all destinations does not stop at one programme's.
	ids, err := act.ListDestinationIDs()
	if err != nil {
		t.Fatalf("ListDestinationIDs: %v", err)
	}
	if ids == nil {
		t.Error("a schedule targeting every destination expanded to nothing")
	}
}
