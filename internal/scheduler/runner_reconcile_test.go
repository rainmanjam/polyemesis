package scheduler

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// reconcileFails is an actuator whose reconcile is refused.
type reconcileFails struct{ *fakeActuator }

func (f reconcileFails) Reconcile() error {
	f.reconcile++
	return errors.New("engine busy")
}

// #709. A sweep that fired but could not reconcile says so on every result that
// fired, and on none that did not.
func TestAFailedReconcileIsReportedOnEveryFiredResult(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{
		oneShot(1, at),
		oneShot(2, at.Add(-2*time.Hour)),
	}}
	act := reconcileFails{&fakeActuator{}}
	r := New(quietLog(), store, act)

	got := r.Tick(at.Add(30 * time.Second))
	var fired, skipped int
	for _, res := range got {
		switch {
		case res.Fired:
			fired++
			if !strings.Contains(res.ReconcileErr, "engine busy") {
				t.Errorf("fired result %d ReconcileErr = %q, want the reconcile error", res.ID, res.ReconcileErr)
			}
		case res.ReconcileErr != "":
			t.Errorf("result %d did not fire but carries ReconcileErr %q", res.ID, res.ReconcileErr)
		default:
			skipped++
		}
	}
	if fired != 1 || skipped != 1 {
		t.Fatalf("Tick = %+v, want one fired and one missed result", got)
	}
	if act.reconcile != 1 {
		t.Errorf("Reconcile called %d times, want 1", act.reconcile)
	}
}
