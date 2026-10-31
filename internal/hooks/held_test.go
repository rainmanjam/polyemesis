package hooks

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/failreason"
)

// destination.held is a webhook trigger, derived HERE and nowhere else.
//
// It used to be derived by the events stream's own watcher, beside the edges
// this one emits, because no webhook carried it -- the hold reached a script
// only folded into a destination.down as held=true. Now that it is a trigger in
// its own right, the derivation moved here so there is one: the stream converts
// this edge like every other (eventstream.FromHook) and announces nothing of
// its own, which is what keeps it from being said twice.
//
// The rules are the stream's, unchanged, and these are its table tests, moved
// with it. Once per hold. A destination this watcher still calls ON has its
// down dwell running, so the hold waits for that down and follows it in the
// same sweep rather than telling a script "held" about a destination it still
// believes is delivering. One that was never ON has no down coming, and is
// announced at once.

func heldReason() *failreason.Reason {
	return &failreason.Reason{Cause: failreason.KeyRejected, Held: true, Rejections: 3, KeyChanged: true}
}

func heldSnap(sec int, d alerts.DestState) alerts.Snapshot {
	return alerts.Snapshot{At: at(sec), Destinations: []alerts.DestState{d}}
}

func TestHeldFollowsTheDownOfADestinationThatWasLiveAndIsAnnouncedOncePerHold(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1, Name: "Studio"}, WatchConfig{DestinationDownAfter: 10e9})
	w.Observe(heldSnap(0, alerts.DestState{ID: 3, Name: "Twitch", Enabled: true, Running: true, OutTimeMS: 1000}))

	held := alerts.DestState{ID: 3, Name: "Twitch", Enabled: true, Reason: heldReason(), Error: "publish refused"}
	if got := w.Observe(heldSnap(4, held)); len(got) != 0 {
		t.Fatalf("held was announced inside the down dwell: %v", triggers(got))
	}
	got := w.Observe(heldSnap(15, held))
	if want := []Trigger{TriggerDestinationDown, TriggerDestinationHeld}; !same(triggers(got), want) {
		t.Fatalf("triggers = %v, want %v", triggers(got), want)
	}
	h := got[1]
	if h.Destination == nil || h.Destination.ID != 3 || h.Source.Name != "Studio" {
		t.Errorf("held = %+v, want destination 3 of Studio", h)
	}
	if !h.Held || h.Cause != failreason.KeyRejected || !h.KeyChanged || h.Error != "publish refused" || h.Reason == "" {
		t.Errorf("held = %+v, want the classified cause, held, keyChanged, the error and a reason", h)
	}
	for sec := 17; sec < 30; sec += 2 {
		if again := w.Observe(heldSnap(sec, held)); len(again) != 0 {
			t.Fatalf("a hold was announced again at %ds: %v", sec, triggers(again))
		}
	}
}

func TestHeldIsAnnouncedAtOnceForADestinationThatNeverWentLive(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{})
	got := w.Observe(heldSnap(0, alerts.DestState{ID: 3, Enabled: true, Reason: heldReason()}))
	if !same(triggers(got), []Trigger{TriggerDestinationHeld}) {
		t.Fatalf("triggers = %v, want an immediate held", triggers(got))
	}
}

// Retry lifts a hold; a second failure is a new hold, and a script must hear
// about it. Delivering again lifts it too, whatever did the lifting.
func TestAHoldIsAnnouncedAgainAfterItIsLifted(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: noDwell})
	held := alerts.DestState{ID: 3, Enabled: true, Reason: heldReason()}
	w.Observe(heldSnap(0, held))
	w.Observe(heldSnap(2, alerts.DestState{ID: 3, Enabled: true})) // Retry
	if got := w.Observe(heldSnap(4, held)); !same(triggers(got), []Trigger{TriggerDestinationHeld}) {
		t.Fatalf("a second hold was not announced: %v", triggers(got))
	}
	w.Observe(heldSnap(6, alerts.DestState{ID: 3, Enabled: true, Running: true, OutTimeMS: 500}))
	got := w.Observe(heldSnap(8, held))
	if want := []Trigger{TriggerDestinationDown, TriggerDestinationHeld}; !same(triggers(got), want) {
		t.Fatalf("a hold after delivering again: %v, want %v", triggers(got), want)
	}
}

// The operator's switch is not a failure. A destination disabled while held
// keeps its last reason in the snapshot, and announcing that as a hold would
// tell a script the disable was a key rejection -- the rule the down applies
// to its own cause fields.
func TestADisabledDestinationIsNotAnnouncedHeld(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: noDwell})
	w.Observe(heldSnap(0, alerts.DestState{ID: 3, Enabled: true, Running: true, OutTimeMS: 1000}))
	got := w.Observe(heldSnap(2, alerts.DestState{ID: 3, Enabled: false, Reason: heldReason()}))
	if !same(triggers(got), []Trigger{TriggerDestinationDown}) || got[0].Held {
		t.Fatalf("got %+v, want a bare down", got)
	}
}

// A row removed while held, then a new row reusing its id, starts afresh: its
// hold is news.
func TestARemovedHeldDestinationIsForgotten(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{})
	held := alerts.DestState{ID: 3, Enabled: true, Reason: heldReason()}
	w.Observe(heldSnap(0, held))
	w.Observe(alerts.Snapshot{At: at(2)})
	if got := w.Observe(heldSnap(4, held)); !same(triggers(got), []Trigger{TriggerDestinationHeld}) {
		t.Fatalf("a reused id inherited the old hold: %v", triggers(got))
	}
}

func TestHeldIsSubscribableAndAppendedAfterEveryExistingTrigger(t *testing.T) {
	if !KnownTrigger(TriggerDestinationHeld) {
		t.Fatal("destination.held cannot be subscribed to")
	}
	// Seventh: appended after the six that existed before it. failover.switched
	// (#6) was appended after it in turn, which is the same rule applied again.
	all := AllTriggers()
	if all[6] != TriggerDestinationHeld {
		t.Errorf("AllTriggers[6] is %q; a new trigger is appended so no picker row moves", all[6])
	}
	if TriggerDestinationHeld != "destination.held" {
		t.Errorf("the trigger is %q; the events stream has always called this destination.held", TriggerDestinationHeld)
	}
}

// failover.switched (#6) is appended LAST, after destination.held, so no row of
// the settings picker moves under an operator who has learned it.
func TestFailoverSwitchedIsSubscribableAndAppendedLast(t *testing.T) {
	if !KnownTrigger(TriggerFailoverSwitched) {
		t.Fatal("failover.switched cannot be subscribed to")
	}
	all := AllTriggers()
	if all[len(all)-1] != TriggerFailoverSwitched {
		t.Errorf("AllTriggers ends with %q, want failover.switched appended last", all[len(all)-1])
	}
	if TriggerFailoverSwitched != "failover.switched" {
		t.Errorf("the trigger is %q; the alert and the events stream already call this failover.switched", TriggerFailoverSwitched)
	}
}
