package hooks

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

func at(sec int) time.Time {
	return time.Date(2026, 7, 31, 12, 0, sec, 0, time.UTC)
}

func triggers(evs []Event) []Trigger {
	out := make([]Trigger, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Trigger)
	}
	return out
}

func same(got, want []Trigger) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// THE test for this whole feature. alerts.Watcher cannot produce this event:
// watchIngest only emits ingest.recovered after it has emitted ingest.lost, so
// an install whose streamer connects inside DownFor of boot produces neither.
func TestFirstBytesFireIngestPublishedWithNoDwell(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1, Name: "Main"}, WatchConfig{})

	if got := w.Observe(alerts.Snapshot{At: at(0), IngestConfigured: true}); len(got) != 0 {
		t.Fatalf("a server that has never seen a stream announced %v", triggers(got))
	}
	got := w.Observe(alerts.Snapshot{At: at(2), IngestConfigured: true, IngestLive: true})
	if !same(triggers(got), []Trigger{TriggerIngestPublished}) {
		t.Fatalf("triggers = %v, want [ingest.published] on the very first bytes",
			triggers(got))
	}
	if got[0].Source.Name != "Main" {
		t.Errorf("source = %+v; a script told the stream started but not which "+
			"programme cannot act on it", got[0].Source)
	}
	// Still live: no repeats. A hook that fires every two seconds for the
	// duration of a broadcast is a hook nobody keeps enabled.
	if again := w.Observe(alerts.Snapshot{At: at(4), IngestConfigured: true, IngestLive: true}); len(again) != 0 {
		t.Fatalf("repeated while still live: %v", triggers(again))
	}
}

func TestDisconnectWaitsForTheDwellAndOnlyAfterAPublish(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DisconnectAfter: 5 * time.Second})

	w.Observe(alerts.Snapshot{At: at(0), IngestConfigured: true, IngestLive: true})
	if got := w.Observe(alerts.Snapshot{At: at(2), IngestConfigured: true}); len(got) != 0 {
		t.Fatalf("fired at 2s into a 5s dwell: %v", triggers(got))
	}
	if got := w.Observe(alerts.Snapshot{At: at(4), IngestConfigured: true}); len(got) != 0 {
		t.Fatalf("fired at 4s into a 5s dwell: %v", triggers(got))
	}
	// at(8), not at(6). The dwell counts from the first snapshot that OBSERVED
	// silence, which is at(2) here, not from the last one that saw data. The
	// watcher cannot know when the bytes actually stopped -- only that they were
	// there at 0 and gone at 2 -- so it starts the clock at the observation it
	// can defend. That makes "no data for 5s" a floor: by the time it fires the
	// silence is at least the dwell and at most the dwell plus one sweep.
	//
	// Counting from the last live sample instead would fire early and make the
	// reason string false, which for a machine-readable event is worse than
	// firing one sweep late.
	if got := w.Observe(alerts.Snapshot{At: at(6), IngestConfigured: true}); len(got) != 0 {
		t.Fatalf("fired at 4s into the dwell measured from first observed silence: %v",
			triggers(got))
	}
	got := w.Observe(alerts.Snapshot{At: at(8), IngestConfigured: true})
	if !same(triggers(got), []Trigger{TriggerIngestDisconnected}) {
		t.Fatalf("triggers = %v, want [ingest.disconnected] once the dwell elapsed",
			triggers(got))
	}
	// And not again. The stream is not disconnecting once every two seconds.
	if again := w.Observe(alerts.Snapshot{At: at(20), IngestConfigured: true}); len(again) != 0 {
		t.Fatalf("repeated while still disconnected: %v", triggers(again))
	}
}

func TestAnIdleServerNeverAnnouncesADisconnection(t *testing.T) {
	// A server that has been up for an hour with nobody streaming has not lost
	// anything. Without the "only after a publish" rule this fires once, five
	// seconds after boot, on every install that is not currently live.
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DisconnectAfter: 5 * time.Second})
	for s := 0; s <= 60; s += 2 {
		if got := w.Observe(alerts.Snapshot{At: at(s), IngestConfigured: true}); len(got) != 0 {
			t.Fatalf("at %ds an idle server announced %v", s, triggers(got))
		}
	}
}

func TestAFlappingDestinationDoesNotStorm(t *testing.T) {
	// The supervisor reconnecting an RTMP destination is normal operation. The
	// dwell on the DOWN edge alone is what suppresses it: a destination that
	// comes back inside the window never goes down, so it never comes up again
	// either.
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: 10 * time.Second})
	dest := func(running bool) alerts.Snapshot {
		return alerts.Snapshot{
			At: time.Time{}, IngestConfigured: true, IngestLive: true,
			Destinations: []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: running}},
		}
	}
	first := dest(true)
	first.At = at(0)
	got := w.Observe(first)
	if !same(triggers(got), []Trigger{TriggerIngestPublished, TriggerDestinationUp}) {
		t.Fatalf("triggers = %v, want ingest.published then destination.up", triggers(got))
	}
	for i, running := range []bool{false, true, false, true, false, true} {
		s := dest(running)
		s.At = at(2 + i*2)
		if evs := w.Observe(s); len(evs) != 0 {
			t.Fatalf("flap %d produced %v; the 10s dwell should have absorbed it",
				i, triggers(evs))
		}
	}
}

// noDwell asks for an immediate DOWN edge.
//
// NOT ZERO. A zero DestinationDownAfter means "unset, take the default" -- the
// struct says "A zero value takes every default" and DisconnectAfter has always
// read it that way -- so these tests used to get their immediate edge from a
// default that was simply never applied. The negative clamp in normalized()
// exists precisely so a caller can say "no dwell" and mean it.
const noDwell = -1

func TestDisablingADestinationIsADownWithAReason(t *testing.T) {
	// Deliberately different from alerts, which treats a disabled destination
	// as "not down". A hook is a fact, not an incident: a script mirroring
	// "what are we live to" needs the edge whoever caused it.
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: noDwell})
	up := alerts.Snapshot{
		At: at(0), IngestConfigured: true, IngestLive: true,
		Destinations: []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: true}},
	}
	w.Observe(up)

	off := up
	off.At = at(2)
	off.Destinations = []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: false}}
	got := w.Observe(off)
	if !same(triggers(got), []Trigger{TriggerDestinationDown}) {
		t.Fatalf("triggers = %v, want [destination.down]", triggers(got))
	}
	if got[0].Reason != "disabled" {
		t.Errorf("reason = %q, want \"disabled\" so a script can tell an "+
			"operator's decision from a failure", got[0].Reason)
	}
}

func TestARemovedDestinationGoesDownRatherThanVanishing(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: noDwell})
	up := alerts.Snapshot{
		At: at(0), IngestConfigured: true, IngestLive: true,
		Destinations: []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: true}},
	}
	w.Observe(up)

	gone := alerts.Snapshot{At: at(2), IngestConfigured: true, IngestLive: true}
	got := w.Observe(gone)
	if !same(triggers(got), []Trigger{TriggerDestinationDown}) {
		t.Fatalf("triggers = %v, want [destination.down]; a deleted destination "+
			"that was live leaves a script believing it still is", triggers(got))
	}
	if got[0].Reason != "removed" {
		t.Errorf("reason = %q, want \"removed\"", got[0].Reason)
	}
	if got[0].Destination == nil || got[0].Destination.Name != "Twitch" {
		t.Errorf("the removed destination lost its identity: %+v", got[0].Destination)
	}
}

func TestDestinationEventsAreOrderedByID(t *testing.T) {
	// Map iteration order must never reach the wire: a receiver correlating
	// deliveries by sequence sees a different order on every run otherwise.
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: noDwell})
	s := alerts.Snapshot{
		At: at(0), IngestConfigured: true, IngestLive: true,
		Destinations: []alerts.DestState{
			{ID: 9, Name: "c", Enabled: true, Running: true},
			{ID: 2, Name: "a", Enabled: true, Running: true},
			{ID: 5, Name: "b", Enabled: true, Running: true},
		},
	}
	got := w.Observe(s)
	var names []string
	for _, e := range got {
		if e.Destination != nil {
			names = append(names, e.Destination.Name)
		}
	}
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Fatalf("destination order = %v, want a b c (ascending id)", names)
	}
}

func TestUnconfiguredIngestResetsRatherThanFiring(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DisconnectAfter: 5 * time.Second})
	w.Observe(alerts.Snapshot{At: at(0), IngestConfigured: true, IngestLive: true})

	// Ingest removed from the configuration entirely. There is nothing to lose,
	// so say nothing -- and forget the session, so re-adding it publishes
	// afresh rather than continuing a session that ended.
	if got := w.Observe(alerts.Snapshot{At: at(2)}); len(got) != 0 {
		t.Fatalf("an unconfigured ingest announced %v", triggers(got))
	}
	got := w.Observe(alerts.Snapshot{At: at(4), IngestConfigured: true, IngestLive: true})
	if !same(triggers(got), []Trigger{TriggerIngestPublished}) {
		t.Fatalf("triggers = %v, want a fresh publish after reconfiguration",
			triggers(got))
	}
}

/* THE DOWN DWELL WAS DECLARED AND NEVER APPLIED.
 *
 * normalized() clamped a negative DestinationDownAfter and stopped, so a zero
 * -- which the struct documents as "takes every default", and which is exactly
 * what engine.go:623 constructs with hooks.WatchConfig{} -- meant NO DWELL.
 * DefaultDestinationDownAfter appeared nowhere but its own declaration and a
 * comment reasoning about a delay that never happened.
 *
 * So every supervisor reconnect crossed a DOWN edge and published a
 * destination.down hook with a destination.up behind it: the storm the constant
 * was written to absorb. It also falsified the LifecycleObserver design at
 * engine.go:3853, which reasons that a restarted destination "crosses no edge
 * at all, because the DOWN direction has a 10s dwell".
 */
func TestTheZeroConfigTakesTheDestinationDownDefault(t *testing.T) {
	if got := (WatchConfig{}).normalized().DestinationDownAfter; got != DefaultDestinationDownAfter {
		t.Errorf("a zero WatchConfig dwells %v before a destination DOWN, want %v — "+
			"the struct documents a zero value as taking every default, and this "+
			"is the config the engine actually constructs", got, DefaultDestinationDownAfter)
	}
}

// A restart shorter than the dwell must cross no edge at all — the property the
// lifecycle design rests on.
func TestASupervisorReconnectInsideTheDwellPublishesNothing(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{})
	live := alerts.Snapshot{
		At: at(0), IngestConfigured: true, IngestLive: true,
		Destinations: []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: true}},
	}
	w.Observe(live)

	// Down for two seconds: one reconcile, well inside the 10s dwell.
	blip := live
	blip.At = at(2)
	blip.Destinations = []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: false}}
	if got := w.Observe(blip); len(got) != 0 {
		t.Errorf("a 2s reconnect published %v — with no dwell every restart "+
			"becomes a down/up pair on every subscriber's endpoint", triggers(got))
	}

	back := live
	back.At = at(4)
	if got := w.Observe(back); len(got) != 0 {
		t.Errorf("coming back published %v, want nothing: it never went down", triggers(got))
	}
}
