package mqtt

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func publishDiscovery(t *testing.T) (haDiscovery, message) {
	t.Helper()
	tel, f := newTelemetry(t)
	if err := tel.PublishDiscovery(context.Background(), sampleSnapshot()); err != nil {
		t.Fatalf("PublishDiscovery: %v", err)
	}
	if len(f.msgs) != 1 {
		t.Fatalf("PublishDiscovery sent %d messages, want 1", len(f.msgs))
	}
	var d haDiscovery
	if err := json.Unmarshal(f.msgs[0].payload, &d); err != nil {
		t.Fatalf("discovery payload is not JSON: %v", err)
	}
	return d, f.msgs[0]
}

func TestDiscoveryGoesToHomeAssistantsTopicRetained(t *testing.T) {
	_, m := publishDiscovery(t)
	if m.topic != "homeassistant/device/studio/config" {
		t.Errorf("discovery topic = %q, want homeassistant/device/studio/config", m.topic)
	}
	// Home Assistant reads discovery topics when it starts. A non-retained
	// payload is seen only by an instance that happened to be running at the
	// moment polyemesis connected -- so every restart would lose the entities.
	if !m.retain {
		t.Error("discovery was published without retain; Home Assistant would lose every entity on restart")
	}
	if m.qos != 1 {
		t.Errorf("discovery was published at QoS %d, want 1", m.qos)
	}
}

// Availability is what makes every entity go "unavailable" together when
// polyemesis stops. Without it a dead instance's entities keep showing their
// last reading indefinitely -- a dashboard that is confidently wrong, which is
// worse than one showing nothing.
func TestDiscoveryWiresAvailabilityToTheWillTopic(t *testing.T) {
	d, _ := publishDiscovery(t)
	if d.AvailabilityTopic != "polyemesis/studio/status" {
		t.Errorf("availability topic = %q, want the same status topic the will message writes", d.AvailabilityTopic)
	}
	if d.PayloadAvailable != Online || d.PayloadNotAvail != Offline {
		t.Errorf("availability payloads are (%q, %q), want (%q, %q)",
			d.PayloadAvailable, d.PayloadNotAvail, Online, Offline)
	}
}

// Every entity must point at a topic the telemetry loop actually publishes. A
// discovery payload naming a topic nothing writes produces an entity that is
// permanently "unknown", and nothing anywhere reports the mismatch.
func TestEveryDiscoveredEntityPointsAtAPublishedTopic(t *testing.T) {
	tel, f := newTelemetry(t)
	ctx := context.Background()
	if err := tel.Publish(ctx, sampleSnapshot()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	published := map[string]bool{}
	for _, m := range f.msgs {
		published[m.topic] = true
	}

	d, _ := publishDiscovery(t)
	if len(d.Component) == 0 {
		t.Fatal("discovery declared no entities")
	}
	for key, c := range d.Component {
		if !published[c.StateTopic] {
			t.Errorf("entity %q reads %q, which the telemetry loop never publishes; the entity would sit at 'unknown' forever",
				key, c.StateTopic)
		}
	}
}

// Home Assistant documents its node and object id charset as [a-zA-Z0-9_-]. A
// unique_id outside it is rejected, and the entity silently never appears.
func TestDiscoveryIdsStayInsideHomeAssistantsCharset(t *testing.T) {
	d, _ := publishDiscovery(t)
	ok := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	for key, c := range d.Component {
		if !ok.MatchString(c.UniqueID) {
			t.Errorf("entity %q has unique_id %q, outside Home Assistant's [a-zA-Z0-9_-]", key, c.UniqueID)
		}
	}
	for _, id := range d.Device.Identifiers {
		if !ok.MatchString(id) {
			t.Errorf("device identifier %q is outside Home Assistant's [a-zA-Z0-9_-]", id)
		}
	}
}

// Two entities sharing a unique_id is the collision Slug exists to prevent,
// arriving one layer later: Home Assistant keeps one and discards the other
// with no visible error.
func TestDiscoveryUniqueIDsAreUnique(t *testing.T) {
	tel, f := newTelemetry(t)
	snap := sampleSnapshot()
	// Two destinations whose names clean to the same text -- the exact pair the
	// slug hash exists for.
	snap.Sources[0].Dests = append(snap.Sources[0].Dests, DestState{
		ID: 8, Name: "Twitch [main]", Slug: Slug("Twitch [main]"), Platform: "twitch",
	})
	if err := tel.PublishDiscovery(context.Background(), snap); err != nil {
		t.Fatalf("PublishDiscovery: %v", err)
	}
	var d haDiscovery
	if err := json.Unmarshal(f.msgs[0].payload, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	seen := map[string]string{}
	for key, c := range d.Component {
		if prev, ok := seen[c.UniqueID]; ok {
			t.Errorf("entities %q and %q share unique_id %q; Home Assistant keeps one and discards the other silently",
				prev, key, c.UniqueID)
		}
		seen[c.UniqueID] = key
	}
	if len(seen) < 2 {
		t.Fatal("the fixture produced too few entities to test uniqueness")
	}
}

// The operator's original name belongs on the entity label; the slug belongs in
// the id. Swapping them gives a dashboard full of hex.
func TestDiscoveryLabelsUseTheOperatorsName(t *testing.T) {
	d, _ := publishDiscovery(t)
	var found bool
	for _, c := range d.Component {
		if c.Name == "Twitch (main)" {
			found = true
		}
		if strings.Contains(c.Name, "-"+hashOf("Twitch (main)")) {
			t.Errorf("entity label %q contains a slug hash; labels are for humans", c.Name)
		}
	}
	if !found {
		t.Error("no entity is labelled with the destination's actual name")
	}
}
