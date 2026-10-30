package hooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/failreason"
)

// TestAFailedDestinationsDownHookCarriesTheCause is the webhook outlet. The
// existing "reason" field already means something else -- "stopped",
// "disabled", "removed", "stalled", which a script branches on -- so the
// classified failure arrives beside it as "cause", the same name the status
// JSON, MQTT and the alert use, rather than overwriting a field scripts read.
func TestAFailedDestinationsDownHookCarriesTheCause(t *testing.T) {
	w := NewWatcher(SourceRef{ID: 1}, WatchConfig{DestinationDownAfter: noDwell})
	up := alerts.Snapshot{
		At: at(0), IngestConfigured: true, IngestLive: true,
		Destinations: []alerts.DestState{{ID: 3, Name: "Twitch", Enabled: true, Running: true, OutTimeMS: 1000}},
	}
	w.Observe(up)
	down := up
	down.At = at(2)
	down.Destinations = []alerts.DestState{{
		ID: 3, Name: "Twitch", Enabled: true,
		Reason: &failreason.Reason{Cause: failreason.KeyRejected, Held: true, Rejections: 3, KeyChanged: true},
	}}
	got := w.Observe(down)
	// The hold follows as its own trigger (held_test.go); the down still
	// carries held=true flat, as it always has.
	if !same(triggers(got), []Trigger{TriggerDestinationDown, TriggerDestinationHeld}) {
		t.Fatalf("triggers = %v", triggers(got))
	}
	ev := got[0]
	if ev.Reason != "stopped" {
		t.Errorf("reason = %q: the existing field must keep its meaning", ev.Reason)
	}
	if ev.Cause != failreason.KeyRejected || !ev.Held || !ev.KeyChanged {
		t.Errorf("event = %+v, want cause key_rejected, held, keyChanged", ev)
	}
}

// TestTheEnvelopeNamesTheCauseFlat: "flat on purpose" -- a jq one-liner reads
// .cause, .held and .keyChanged with no nesting, and a delivery that has no
// cause carries none of the three.
func TestTheEnvelopeNamesTheCauseFlat(t *testing.T) {
	body, err := Encode(Envelope{
		SpecVersion: SpecVersion, ID: "d1", Sequence: 1, Trigger: TriggerDestinationDown,
		At: time.Unix(0, 0).UTC(), Cause: failreason.TLS, Held: true, KeyChanged: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if m["cause"] != "tls" || m["held"] != true || m["keyChanged"] != true {
		t.Fatalf("envelope = %s", body)
	}
	plain, _ := Encode(Envelope{SpecVersion: SpecVersion, ID: "d2", Trigger: TriggerDestinationUp})
	for _, k := range []string{`"cause"`, `"held"`, `"keyChanged"`} {
		if strings.Contains(string(plain), k) {
			t.Errorf("an up delivery carried %s: %s", k, plain)
		}
	}
}
