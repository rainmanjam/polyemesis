package scheduler

import (
	"reflect"
	"testing"
)

type preparingActuator struct {
	order []string
	ids   []int64
}

func (a *preparingActuator) SetDestinationEnabled(id int64, enabled bool) error {
	if enabled {
		a.order = append(a.order, "enable")
	} else {
		a.order = append(a.order, "disable")
	}
	return nil
}
func (a *preparingActuator) ListDestinationIDs() ([]int64, error) { return nil, nil }
func (a *preparingActuator) SetPlaylistEnabled(bool) error        { return nil }
func (a *preparingActuator) Reconcile() error                     { return nil }
func (a *preparingActuator) PrepareStarts(ids []int64) func() {
	a.order = append(a.order, "prepare")
	a.ids = ids
	return func() { a.order = append(a.order, "release") }
}

// A start schedule hands every target to PrepareStarts ONCE, before the first
// flag is written, and releases after the last; a stop schedule never prepares.
//
// Mutation: drop the StartPreparer block in setDestinations. Red.
func TestAStartScheduleIsPreparedOnceBeforeAnyFlagIsWritten(t *testing.T) {
	a := &preparingActuator{}
	r := &Runner{act: a, log: quietLog()}

	r.setDestinations(Schedule{Action: ActionStart}, []int64{1, 2})
	if want := []string{"prepare", "enable", "enable", "release"}; !reflect.DeepEqual(a.order, want) {
		t.Fatalf("order = %v, want %v", a.order, want)
	}
	if !reflect.DeepEqual(a.ids, []int64{1, 2}) {
		t.Fatalf("prepared %v, want [1 2]", a.ids)
	}

	a.order = nil
	r.setDestinations(Schedule{Action: ActionStop}, []int64{1})
	if want := []string{"disable"}; !reflect.DeepEqual(a.order, want) {
		t.Fatalf("a stop was prepared: %v", a.order)
	}
}
