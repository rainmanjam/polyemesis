package main

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
)

// fakeMQTTEngine is an mqttEngine whose destination statuses the test chooses.
//
// It is deliberately dumb everywhere except ScrubDestinationText, which is the
// one thing under test. That one applies the BOUNDARY half of what the real
// engine applies -- the exact secret set built from the destination's own row --
// and stops there. The real helper follows it with alerts.Redact as a residual;
// this does not, on purpose, because alerts.Redact has a call-site allowlist
// (#162) and a test fake is not a place worth widening it for. Nothing here
// measures whether the scrub WORKS, only whether snapshot calls it; whether it
// works is internal/engine's TestScrubDestinationTextCoversTheRetainedTopic.
type fakeMQTTEngine struct {
	id     int64
	name   string
	status engine.Status

	// dests maps a destination id to its stream key, which is what
	// ScrubDestinationText would remove.
	dests map[int64]string

	// scrubbed counts the calls, so a snapshot that scrubs the WRONG field --
	// or a test fixture that produces no destinations at all -- cannot pass by
	// accident.
	scrubbed int
}

func (f *fakeMQTTEngine) SourceID() int64       { return f.id }
func (f *fakeMQTTEngine) SourceName() string    { return f.name }
func (f *fakeMQTTEngine) Settings() db.Settings { return db.Settings{} }
func (f *fakeMQTTEngine) Status() engine.Status { return f.status }
func (f *fakeMQTTEngine) ScrubDestinationText(id int64, text string) string {
	f.scrubbed++
	if text == "" {
		return text
	}
	if key := f.dests[id]; key != "" {
		text = alerts.NewSecretSet(nil, key).Scrub(text)
	}
	return text
}

// TestTheRetainedDestTopicIsScrubbedAtTheSink is the WIRING half of #160.
//
// internal/engine has a test for ScrubDestinationText itself. That test calls
// the helper directly, which means it stays green when the single line that
// CALLS the helper is deleted -- and the helper has exactly one caller, the
// DestState.Error assignment in mqttRunner.snapshot. An adversarial review of
// this PR reverted that line to `Error: d.Error` and cmd/polyemesis,
// internal/mqtt and internal/engine all still passed.
//
// That is the worst place in this repository for a silent regression. The topic
// is RETAINED: the broker keeps the last message and hands it to every client
// that connects afterwards, so a credential that reaches it is not recoverable
// by rotating the credential -- the old value is already sitting on somebody
// else's broker and will be delivered again. Nothing about the string's origin
// makes that safe: the compile diagnostics that reach this field today are
// harmless, but "today's callers are harmless" is what every disclosure was
// before it was one.
//
// The mutation this exists to catch: change
//
//	Error: e.ScrubDestinationText(d.ID, d.Error),
//
// back to `Error: d.Error` in cmd/polyemesis/mqtt.go, and this fails.
func TestTheRetainedDestTopicIsScrubbedAtTheSink(t *testing.T) {
	const streamKey = "SENTINEL-mqtt-retained-7c40de91"

	fake := &fakeMQTTEngine{
		id: 7, name: "Main programme",
		dests: map[int64]string{42: streamKey},
		status: engine.Status{
			Destinations: []engine.DestStatus{{
				ID: 42, Name: "twitch", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
				Enabled: true,
				// The shape that actually occurs: the credential GLUED into a
				// URL inside a diagnostic. A field-level allowlist would not
				// have saved this; only scrubbing the text does.
				Error: "cannot start: rtmp://live.example/app/" + streamKey,
			}},
		},
	}

	r := &mqttRunner{
		version: "test",
		engines: func() []mqttEngine { return []mqttEngine{fake} },
	}
	snap := r.snapshot()

	if fake.scrubbed == 0 {
		t.Fatal("snapshot built a destination state without calling ScrubDestinationText " +
			"at all. DestState.Error is published to a RETAINED MQTT topic; it is the one " +
			"field on this tree that cannot be un-published. See #160.")
	}

	var errors []string
	for _, item := range snap.Sources {
		for _, d := range item.Dests {
			errors = append(errors, d.Error)
		}
	}
	if len(errors) != 1 {
		t.Fatalf("snapshot produced %d destination states, want 1 -- the assertion below "+
			"would be vacuous: %#v", len(errors), snap.Sources)
	}
	got := errors[0]

	if strings.Contains(got, streamKey) {
		t.Errorf("DestState.Error = %q, which still carries the destination's stream key.\n\n"+
			"This string is published RETAINED, so the broker replays it to every future "+
			"subscriber and rotating the key does not recall it. The assignment in "+
			"mqttRunner.snapshot must stay `e.ScrubDestinationText(d.ID, d.Error)` (#160).", got)
	}
	if !strings.Contains(got, alerts.Mask) {
		t.Errorf("DestState.Error = %q -- the key is gone but nothing says a redaction "+
			"happened, which usually means the diagnostic was dropped rather than masked", got)
	}
	if !strings.Contains(got, "cannot start") {
		t.Errorf("DestState.Error = %q lost its diagnostic. On a headless box this topic is "+
			"the only place the reason appears; masking the message along with the key "+
			"trades one failure for another", got)
	}
}

// TestSnapshotCarriesTheOrdinaryFields is the complement, and the reason the
// test above can be read as being about redaction rather than about plumbing.
//
// If snapshot stopped copying anything at all, every assertion above would
// still hold -- an empty string contains no stream key. This pins that the
// mapping is alive.
func TestSnapshotCarriesTheOrdinaryFields(t *testing.T) {
	fake := &fakeMQTTEngine{
		id: 7, name: "Main programme",
		status: engine.Status{
			Destinations: []engine.DestStatus{{
				ID: 42, Name: "twitch", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
				Enabled: true, RenditionName: "720p",
			}},
		},
	}
	r := &mqttRunner{
		version: "test",
		engines: func() []mqttEngine { return []mqttEngine{fake} },
	}
	snap := r.snapshot()

	if snap.Host.Sources != 1 {
		t.Fatalf("Host.Sources = %d, want 1", snap.Host.Sources)
	}
	if len(snap.Sources) != 1 || len(snap.Sources[0].Dests) != 1 {
		t.Fatalf("snapshot lost the destination entirely: %#v", snap.Sources)
	}
	d := snap.Sources[0].Dests[0]
	if d.ID != 42 || d.Name != "twitch" || d.Platform != string(db.PlatformTwitch) ||
		d.Kind != string(db.DestRTMP) || !d.Enabled || d.Rendition != "720p" {
		t.Errorf("destination state came through as %#v; the ordinary fields are not "+
			"being copied, which would make the redaction assertions above vacuous", d)
	}
	if d.Error != "" {
		t.Errorf("DestState.Error = %q for a destination with no error; a bare mask on a "+
			"healthy destination reads as a fault", d.Error)
	}
}
