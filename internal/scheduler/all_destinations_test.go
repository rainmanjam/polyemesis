package scheduler

import (
	"strings"
	"testing"
	"time"
)

// app-council A54: "every destination" is a choice the schedule carries, not
// what an empty list happens to mean.
//
// Before this, an empty DestinationIDs was read as EVERY destination on the
// install. That is the commonest shape for "start the show", and for a stop it
// is the most destructive thing the scheduler can do: a stop schedule whose
// author ticked nothing -- or whose ticks were lost -- took down every broadcast
// on the box at the hour it fired, and stopping a YouTube broadcast completes it
// for good. AllDestinations is the explicit answer, and a destination schedule
// that has neither a list nor the flag is not a schedule the runner can act on.

// The mutation: drop the "names no destinations" clause in Validate and this
// passes the empty stop through.
func TestADestinationScheduleWithNoListAndNoAllDestinationsIsRefused(t *testing.T) {
	for _, a := range []Action{ActionStart, ActionStop} {
		s := Schedule{Name: "late show", Action: a, Kind: KindDaily}.Normalized()
		err := s.Validate()
		if err == nil {
			t.Fatalf("Validate(%q with no destinations and no allDestinations) = nil, want a refusal", a)
		}
		if !strings.Contains(err.Error(), "allDestinations") {
			t.Errorf("error %q does not name allDestinations, the field that would fix it", err)
		}
	}
}

func TestAllDestinationsWithAnEmptyListValidates(t *testing.T) {
	for _, a := range []Action{ActionStart, ActionStop} {
		s := Schedule{Name: "late show", Action: a, Kind: KindDaily, AllDestinations: true}.Normalized()
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%q, allDestinations) = %v, want nil", a, err)
		}
	}
}

// Both at once is two answers to one question. Refused rather than resolved,
// because either resolution acts on something the author did not say.
func TestAllDestinationsTogetherWithANamedListIsRefused(t *testing.T) {
	s := Schedule{
		Name: "late show", Action: ActionStop, Kind: KindDaily,
		AllDestinations: true, DestinationIDs: []int64{3},
	}.Normalized()
	err := s.Validate()
	if err == nil {
		t.Fatal("a schedule naming destinations AND every destination was accepted")
	}
	if !strings.Contains(err.Error(), "allDestinations") {
		t.Errorf("error %q does not name allDestinations", err)
	}
}

// A playlist schedule acts on no destination at all, so the flag means nothing
// there; Normalized clears it so two playlist rows that mean the same thing
// store the same bytes.
func TestNormalizedClearsAllDestinationsOnAPlaylistSchedule(t *testing.T) {
	s := Schedule{Name: "filler", Action: ActionPlaylistStart, Kind: KindDaily, AllDestinations: true}.Normalized()
	if s.AllDestinations {
		t.Error("a playlist schedule kept allDestinations")
	}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate = %v, want nil", err)
	}
}

// The runner side: the flag is what expands to every destination.
func TestTickExpandsAllDestinationsToEveryDestination(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{oneShot(1, at, func(s *Schedule) {
		s.Action = ActionStop
		s.DestinationIDs = nil
		s.AllDestinations = true
	})}}
	act := &fakeActuator{all: []int64{1, 2, 3}}
	r := New(quietLog(), store, act)

	got := r.Tick(at.Add(time.Second))
	if len(got) != 1 || len(got[0].Targets) != 3 {
		t.Fatalf("Tick = %+v, want all three destinations", got)
	}
	for id, on := range act.set {
		if on {
			t.Errorf("destination %d was enabled by a stop", id)
		}
	}
}

// THE ONE THAT FAILS ON THE OLD CODE. A stop row that reaches the runner with an
// empty list and no flag -- a database edited by hand, or a row a future bug
// lets through -- used to stop every destination. Now it stops none.
//
// The mutation: restore `if len(targets) == 0 { targets = all }` in Tick and
// Validate's clause, and this sees three destinations disabled.
func TestTickStopsNothingForAStopWithNoDestinationsAndNoAllDestinations(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []Schedule{oneShot(1, at, func(s *Schedule) {
		s.Action = ActionStop
		s.DestinationIDs = nil
	})}}
	act := &fakeActuator{all: []int64{1, 2, 3}}
	r := New(quietLog(), store, act)

	r.Tick(at.Add(time.Second))
	if act.setCalls != 0 {
		t.Fatalf("a stop naming nothing flipped %d destination(s): %v", act.setCalls, act.set)
	}
}
