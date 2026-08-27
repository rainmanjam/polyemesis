package automod

import (
	"context"
	"testing"
	"time"
)

/* THE DEFECT, STATED AS A TEST (#502).
 *
 * The spend ceiling used to be a field on *Model, and ApplyAutomod builds a
 * fresh Model on every settings save. So the only bound on model spend in the
 * product was refilled by saving a setting -- the "tweak something
 * mid-incident" reflex, which is exactly when an operator is most likely to be
 * saving and least likely to be watching the bill.
 *
 * Rebuilding the connector is correct and still happens here. What must not
 * happen is the allowance coming back with it. */
func TestRebuildingTheConnectorDoesNotRefillTheHour(t *testing.T) {
	srv := modelServer(t, respondVerdict(false, 1, "fine"))
	budget := NewBudget()

	cfg := DefaultModelConfig()
	cfg.Enabled = true
	cfg.Endpoint = srv.URL
	cfg.MaxCallsPerHour = 2

	first := NewModel(cfg, budget)
	for i := 0; i < 2; i++ {
		if _, err := first.Check(context.Background(), "x"); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if _, err := first.Check(context.Background(), "x"); err == nil {
		t.Fatal("the ceiling did not apply to the first connector")
	}

	// The settings save. Same budget, brand-new connector -- which is what
	// ApplyAutomod does.
	second := NewModel(cfg, budget)
	if _, err := second.Check(context.Background(), "x"); err == nil {
		t.Fatal("saving a setting refilled the hourly allowance: the rebuilt " +
			"connector was allowed a call the ceiling had already spent")
	}

	// AND THE EVIDENCE SURVIVES TOO. CallsThisHour dropping to zero would tell
	// an operator watching the spend panel that nothing had been spent, at the
	// same moment the limit came back.
	if got := second.Stats().CallsThisHour; got != 2 {
		t.Fatalf("CallsThisHour = %d after a rebuild, want 2: the spend reading "+
			"reset along with the connector", got)
	}
}

/* THE CONTROL CASE. A budget that refuses everything would pass the test above
 * for the wrong reason, so a fresh one must still allow the calls it should. */
func TestAFreshBudgetAllowsItsCeiling(t *testing.T) {
	b := NewBudget()
	for i := 0; i < 3; i++ {
		if !b.reserve(3) {
			t.Fatalf("a fresh budget refused call %d of a ceiling of 3", i+1)
		}
	}
	if b.reserve(3) {
		t.Fatal("the ceiling did not apply")
	}
	if got := b.Spent(); got != 3 {
		t.Fatalf("Spent() = %d, want 3", got)
	}
}

/* A ceiling of zero or less is the config's own convention for "no limit", and
 * the budget must not invent one. */
func TestZeroCeilingMeansUnlimited(t *testing.T) {
	b := NewBudget()
	for i := 0; i < 50; i++ {
		if !b.reserve(0) {
			t.Fatalf("a zero ceiling refused call %d; zero means unlimited", i+1)
		}
	}
}

/* The window is measured on the shared budget, so it survives a rebuild in the
 * same way the count does -- otherwise a save loop could spend without bound
 * while every individual window looked untouched. */
func TestTheWindowSurvivesARebuild(t *testing.T) {
	b := NewBudget()
	clk := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return clk }

	if !b.reserve(1) {
		t.Fatal("first call refused")
	}
	if b.reserve(1) {
		t.Fatal("the ceiling did not apply")
	}
	clk = clk.Add(61 * time.Minute)
	if !b.reserve(1) {
		t.Fatal("the window did not roll over after an hour")
	}
}
