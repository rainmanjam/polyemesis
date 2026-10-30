package eventstream

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

var prog = hooks.SourceRef{ID: 1, Name: "Studio"}

func snapAt(sec int, dests ...alerts.DestState) alerts.Snapshot {
	return alerts.Snapshot{At: time.Unix(int64(sec), 0), Destinations: dests}
}

func edge(tr hooks.Trigger, id int64) hooks.Event {
	return hooks.Event{Trigger: tr, Source: prog, Destination: &hooks.DestinationRef{ID: id}}
}

func typesOf(envs []Envelope) []Type {
	out := make([]Type, len(envs))
	for i, e := range envs {
		out[i] = e.Type
	}
	return out
}

func heldReason() *failreason.Reason {
	return &failreason.Reason{Cause: failreason.KeyRejected, Held: true, Rejections: 3, KeyChanged: true}
}

// destination.held is the webhook watcher's edge, converted. The rules of WHEN
// (once per hold, after the down of a destination that was live, at once for
// one that never was) moved to hooks.Watcher with their tests -- see
// internal/hooks/held_test.go -- so that one derivation feeds both surfaces.
// What is left here is what the stream adds, and what it must not.

// TestTheHeldEdgeIsConvertedWithTheWholeFailure: the same name on both surfaces,
// the flat fields from the edge, and the whole classified reason beside them,
// read off the snapshot as a down's is.
func TestTheHeldEdgeIsConvertedWithTheWholeFailure(t *testing.T) {
	if TypeDestinationHeld != Type(hooks.TriggerDestinationHeld) {
		t.Fatalf("the stream calls it %q and the webhook %q; one fact, one name", TypeDestinationHeld, hooks.TriggerDestinationHeld)
	}
	w := NewWatcher()
	held := alerts.DestState{ID: 3, Enabled: true, Reason: heldReason()}
	ev := edge(hooks.TriggerDestinationHeld, 3)
	ev.Cause, ev.Held, ev.KeyChanged = failreason.KeyRejected, true, true
	got := w.Observe(prog, snapAt(0, held), []hooks.Event{ev})
	if !sameTypes(typesOf(got), []Type{TypeDestinationHeld}) {
		t.Fatalf("got %v", typesOf(got))
	}
	h := got[0]
	if !h.Held || h.Cause != failreason.KeyRejected || !h.KeyChanged || h.Source.Name != "Studio" {
		t.Errorf("held = %+v, want cause and keyChanged carried flat", h)
	}
	if h.Failure == nil || h.Failure.Rejections != 3 {
		t.Errorf("held = %+v, want the whole failure beside the flat fields", h)
	}
}

// TestTheStreamAnnouncesNoHoldOfItsOwn: a held destination in the snapshot with
// no held edge from the webhook watcher produces nothing. This watcher used to
// derive the hold itself; doing that as well as converting the edge is the
// double emission this pins.
func TestTheStreamAnnouncesNoHoldOfItsOwn(t *testing.T) {
	w := NewWatcher()
	held := alerts.DestState{ID: 3, Enabled: true, Reason: heldReason()}
	for sec := 0; sec < 10; sec += 2 {
		if got := w.Observe(prog, snapAt(sec, held), nil); len(got) != 0 {
			t.Fatalf("the stream derived %v on its own at %ds", typesOf(got), sec)
		}
	}
}

// TestADisabledDestinationIsNeitherHeldNorCarriesAFailure: the rule
// hooks.Watcher applies to its flat fields, applied to the whole reason.
func TestADisabledDestinationIsNeitherHeldNorCarriesAFailure(t *testing.T) {
	w := NewWatcher()
	off := alerts.DestState{ID: 3, Enabled: false, Reason: heldReason()}
	w.Observe(prog, snapAt(0, alerts.DestState{ID: 3, Enabled: true}), []hooks.Event{edge(hooks.TriggerDestinationUp, 3)})
	got := w.Observe(prog, snapAt(2, off), []hooks.Event{edge(hooks.TriggerDestinationDown, 3)})
	if !sameTypes(typesOf(got), []Type{TypeDestinationDown}) || got[0].Failure != nil {
		t.Fatalf("got %+v, want a bare down", got)
	}
}

// TestARespawnIsAnnouncedButANewProcessIsNot: the count rising on the same
// process is a respawn; the count falling is a new process (a spec change, or
// Retry); the first sight of a destination is a baseline, so a restarted server
// does not announce every destination's lifetime respawns.
func TestARespawnIsAnnouncedButANewProcessIsNot(t *testing.T) {
	w := NewWatcher()
	d := func(n int) alerts.DestState { return alerts.DestState{ID: 3, Enabled: true, Restarts: n} }
	if got := w.Observe(prog, snapAt(0, d(7)), nil); len(got) != 0 {
		t.Fatalf("the first sight was announced: %v", typesOf(got))
	}
	got := w.Observe(prog, snapAt(2, d(9)), nil)
	if !sameTypes(typesOf(got), []Type{TypeDestinationRestarted}) {
		t.Fatalf("got %v", typesOf(got))
	}
	if data := got[0].Data.(map[string]int); data["restarts"] != 9 || data["since"] != 2 {
		t.Errorf("data = %v", data)
	}
	if got := w.Observe(prog, snapAt(4, d(0)), nil); len(got) != 0 {
		t.Errorf("a new process was announced as a respawn: %v", typesOf(got))
	}
	// A removed row is forgotten, so a reused id starts from a baseline.
	w.Observe(prog, snapAt(6), nil)
	if got := w.Observe(prog, snapAt(8, d(5)), nil); len(got) != 0 {
		t.Errorf("a reused id inherited the old count: %v", typesOf(got))
	}
}

func sameTypes(a, b []Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// #11: A DELAYED DESTINATION'S EDGES SAY SO. "up" on one means the DELAYED
// programme reached the platform; up, down and restarted carry delaySeconds, and
// an undelayed destination's edges carry nothing new.
//
// Mutation: make the edge branch's `ok && d.DelaySeconds > 0` condition false.
// The up edge then has no delaySeconds.
func TestADelayedDestinationsEdgesCarryItsDelay(t *testing.T) {
	w := NewWatcher()
	delayed := alerts.DestState{ID: 3, Enabled: true, Running: true, DelaySeconds: 30, Restarts: 1}
	plain := alerts.DestState{ID: 4, Enabled: true, Running: true}
	got := w.Observe(prog, snapAt(0, delayed, plain),
		[]hooks.Event{edge(hooks.TriggerDestinationUp, 3), edge(hooks.TriggerDestinationUp, 4)})
	if len(got) != 2 {
		t.Fatalf("got %v", typesOf(got))
	}
	if data, _ := got[0].Data.(map[string]int); data["delaySeconds"] != 30 {
		t.Errorf("the delayed destination's up carries %v, want delaySeconds 30", got[0].Data)
	}
	if got[1].Data != nil {
		t.Errorf("an undelayed destination's up carries %v", got[1].Data)
	}
	delayed.Restarts = 2
	got = w.Observe(prog, snapAt(2, delayed, plain), nil)
	if data, _ := got[0].Data.(map[string]int); got[0].Type != TypeDestinationRestarted || data["delaySeconds"] != 30 {
		t.Errorf("restarted = %v %v", got[0].Type, got[0].Data)
	}
}

// #11 against Wave 1's move of destination.held: the hold is the webhook
// watcher's edge, converted here, so a delayed destination's held carries
// delaySeconds through the same edge branch as up and down -- once, with its
// failure, and never from the snapshot alone. Written when #11 (which added
// the delay) met main (which had moved the hold): neither branch's tests saw
// both.
//
// Mutation: restrict the delaySeconds branch in Observe to up and down. The
// held edge then carries no delay.
func TestADelayedDestinationsHeldEdgeCarriesItsDelayOnce(t *testing.T) {
	w := NewWatcher()
	held := alerts.DestState{ID: 3, Enabled: true, DelaySeconds: 30, Reason: heldReason()}
	if got := w.Observe(prog, snapAt(0, held), nil); len(got) != 0 {
		t.Fatalf("the stream derived %v on its own for a delayed hold", typesOf(got))
	}
	got := w.Observe(prog, snapAt(2, held), []hooks.Event{edge(hooks.TriggerDestinationHeld, 3)})
	if !sameTypes(typesOf(got), []Type{TypeDestinationHeld}) {
		t.Fatalf("got %v, want exactly one held", typesOf(got))
	}
	if data, _ := got[0].Data.(map[string]int); data["delaySeconds"] != 30 {
		t.Errorf("the delayed destination's held carries %v, want delaySeconds 30", got[0].Data)
	}
	if got[0].Failure == nil || !got[0].Failure.Held {
		t.Errorf("held = %+v, want the failure beside the delay", got[0])
	}
	if got := w.Observe(prog, snapAt(4, held), nil); len(got) != 0 {
		t.Errorf("the hold was announced again with no edge: %v", typesOf(got))
	}
}
