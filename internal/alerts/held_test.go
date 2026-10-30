package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/failreason"
)

// destination.held is its own alert type. It used to be folded into
// destination.down's text ("held after 3 quick rejections, press Retry"), which
// left an operator no way to page on the one stop that will not fix itself
// without also paging on every stop that will.
//
// It fires ONCE PER HOLD, on the sweep the destination enters it -- not on
// every sweep the hold lasts, which is what a level read of Reason.Held would
// do -- and again only after the hold is lifted and a new one begins. Its key
// is its own ("destination:<id>:held"): the coalescer groups by key and the
// newest event in a group wins the message, so sharing destination.down's key
// would let a down arriving inside the debounce window overwrite it.

func heldDest(held bool) DestState {
	d := DestState{ID: 4, Name: "YouTube", Platform: "youtube", Enabled: true, Error: "publish refused"}
	if held {
		d.Reason = &failreason.Reason{Cause: failreason.KeyRejected, Held: true, Rejections: 3}
	}
	return d
}

func TestADestinationHeldAlertFiresOncePerHoldUnderItsOwnKey(t *testing.T) {
	w := NewWatcher(WatchConfig{DownFor: 20 * time.Second})
	w.Observe(Snapshot{At: base, Destinations: []DestState{{ID: 4, Name: "YouTube", Enabled: true, Running: true}}})

	var held []Event
	var types []Type
	for sec := 2; sec <= 40; sec += 2 {
		for _, ev := range w.Observe(Snapshot{At: base.Add(time.Duration(sec) * time.Second), Destinations: []DestState{heldDest(true)}}) {
			types = append(types, ev.Type)
			if ev.Type == TypeDestinationHeld {
				held = append(held, ev)
			}
		}
	}
	if len(held) != 1 {
		t.Fatalf("destination.held fired %d times over one hold, want once: %v", len(held), types)
	}
	// The down still fires on its own dwell, and still says why.
	if len(types) != 2 || types[0] != TypeDestinationHeld || types[1] != TypeDestinationDown {
		t.Errorf("events = %v, want held at once, then down after its dwell", types)
	}
	ev := held[0]
	if ev.Key != "destination:4:held" || ev.Severity != SeverityCritical {
		t.Errorf("held = %+v, want key destination:4:held at critical", ev)
	}
	for _, want := range []string{"YouTube", "stream key rejected", "Retry"} {
		if !strings.Contains(ev.Title+" "+ev.Text, want) {
			t.Errorf("held title+text %q does not say %q", ev.Title+" "+ev.Text, want)
		}
	}
	if fieldOf(ev, "cause") != "key_rejected" || fieldOf(ev, "destination") != "YouTube" || fieldOf(ev, "error") != "publish refused" {
		t.Errorf("held fields = %+v", ev.Fields)
	}
}

func TestAHoldIsAlertedAgainOnlyAfterItIsLifted(t *testing.T) {
	w := NewWatcher(WatchConfig{DownFor: time.Hour})
	count := func(sec int, d DestState) int {
		n := 0
		for _, ev := range w.Observe(Snapshot{At: base.Add(time.Duration(sec) * time.Second), Destinations: []DestState{d}}) {
			if ev.Type == TypeDestinationHeld {
				n++
			}
		}
		return n
	}
	if count(0, heldDest(true)) != 1 || count(2, heldDest(true)) != 0 {
		t.Fatal("a hold was not alerted exactly once")
	}
	count(4, heldDest(false)) // Retry
	if count(6, heldDest(true)) != 1 {
		t.Error("a second hold after Retry was not alerted")
	}
	// Disabling clears it, and a disabled destination is not held whatever
	// reason it still carries: the operator's switch is not an incident.
	off := heldDest(true)
	off.Enabled = false
	if count(8, off) != 0 {
		t.Error("a disabled destination was alerted held")
	}
	if count(10, heldDest(true)) != 1 {
		t.Error("a hold after re-enabling was not alerted")
	}
}

func TestDestinationHeldIsSubscribableAndOnTheTimeline(t *testing.T) {
	if !KnownType(TypeDestinationHeld) {
		t.Fatal("destination.held cannot be subscribed to")
	}
	if !IsTimelineType(TypeDestinationHeld) {
		t.Fatal("destination.held is not on the timeline beside destination.down")
	}
	r := Rule{Enabled: true, Events: []Type{TypeDestinationHeld}}.Normalized()
	if len(r.Events) != 1 || !r.Wants(Event{Type: TypeDestinationHeld, Severity: SeverityCritical}) {
		t.Errorf("a rule subscribed to destination.held = %+v, which does not keep or want it", r)
	}
}

// The held alert carries the same two strings a down does that this process did
// not write -- the child's last error and FFmpeg's classified line -- and both
// routinely hold an rtmps:// URL with the key on the end. It goes through the
// same Redacted pass and the same encoders; this drives the real watcher's
// event through them in every format, so the type is checked by the bytes it
// would send rather than assumed to match.
func TestADestinationHeldAlertNeverCarriesTheStreamKey(t *testing.T) {
	dirty := "rtmps://live.twitch.tv/app/" + secretKey + ": Input/output error"
	w := NewWatcher(WatchConfig{})
	d := DestState{ID: 4, Name: "Twitch", Platform: "twitch", Enabled: true, Error: dirty,
		Reason: &failreason.Reason{Cause: failreason.KeyRejected, Held: true, Rejections: 3, Line: dirty}}
	var ev Event
	for _, e := range w.Observe(Snapshot{At: base, Destinations: []DestState{d}}) {
		if e.Type == TypeDestinationHeld {
			ev = e
		}
	}
	if ev.Type != TypeDestinationHeld || !strings.Contains(fieldOf(ev, "error")+fieldOf(ev, "detail"), secretKey) {
		t.Fatalf("the fixture did not plant the key in the held alert, so its absence below proves nothing: %+v", ev)
	}
	ev = ev.Redacted()
	for _, format := range []Format{FormatJSON, FormatDiscord, FormatSlack} {
		body, _, err := Encode(Delivery{
			Rule:  Rule{ID: 1, Name: "ops", URL: "https://example.com/h", Format: format},
			Items: []Item{{Event: ev, Count: 1, First: base, Last: base}},
		})
		if err != nil {
			t.Fatalf("Encode %s: %v", format, err)
		}
		if strings.Contains(string(body), secretKey) {
			t.Errorf("%s payload of destination.held carried the stream key:\n%s", format, body)
		}
	}
}
