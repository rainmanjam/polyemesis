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

// TestHeldFollowsTheDownOfADestinationThatWasLive: the hold usually lands
// inside the webhook watcher's ten-second down dwell. Announcing it then would
// tell a script "held" about a destination it still believes is delivering, so
// it waits for the down and follows it in the same sweep.
func TestHeldFollowsTheDownOfADestinationThatWasLive(t *testing.T) {
	w := NewWatcher()
	w.Observe(prog, snapAt(0, alerts.DestState{ID: 3, Enabled: true}), []hooks.Event{edge(hooks.TriggerDestinationUp, 3)})

	held := alerts.DestState{ID: 3, Enabled: true, Reason: heldReason()}
	if got := w.Observe(prog, snapAt(4, held), nil); len(got) != 0 {
		t.Fatalf("held was announced inside the down dwell: %v", typesOf(got))
	}
	got := w.Observe(prog, snapAt(12, held), []hooks.Event{edge(hooks.TriggerDestinationDown, 3)})
	if want := []Type{TypeDestinationDown, TypeDestinationHeld}; !sameTypes(typesOf(got), want) {
		t.Fatalf("got %v, want %v", typesOf(got), want)
	}
	if got[0].Failure == nil || !got[0].Failure.Held || got[1].Source.Name != "Studio" {
		t.Errorf("events = %+v", got)
	}
	if !got[1].KeyChanged || got[1].Cause != failreason.KeyRejected {
		t.Errorf("held = %+v, want cause and keyChanged carried flat", got[1])
	}
	// Once per hold.
	if again := w.Observe(prog, snapAt(14, held), nil); len(again) != 0 {
		t.Errorf("a hold was announced twice: %v", typesOf(again))
	}
}

// TestHeldIsAnnouncedAtOnceForADestinationThatNeverWentLive: the webhook
// watcher starts every destination OFF, so one with a bad key from its first
// attempt never gets a down -- and its hold must not wait for one.
func TestHeldIsAnnouncedAtOnceForADestinationThatNeverWentLive(t *testing.T) {
	w := NewWatcher()
	got := w.Observe(prog, snapAt(0, alerts.DestState{ID: 3, Enabled: true, Reason: heldReason()}), nil)
	if !sameTypes(typesOf(got), []Type{TypeDestinationHeld}) {
		t.Fatalf("got %v", typesOf(got))
	}
}

// TestAHoldIsAnnouncedAgainAfterItIsLifted: Retry lifts it; a second failure is
// a new hold and a script must hear about it.
func TestAHoldIsAnnouncedAgainAfterItIsLifted(t *testing.T) {
	w := NewWatcher()
	held := alerts.DestState{ID: 3, Enabled: true, Reason: heldReason()}
	w.Observe(prog, snapAt(0, held), nil)
	w.Observe(prog, snapAt(2, alerts.DestState{ID: 3, Enabled: true}), nil) // Retry
	got := w.Observe(prog, snapAt(4, held), nil)
	if !sameTypes(typesOf(got), []Type{TypeDestinationHeld}) {
		t.Fatalf("a second hold was not announced: %v", typesOf(got))
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
