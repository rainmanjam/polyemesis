package mqtt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func audienceSnapshot() Snapshot {
	s := sampleSnapshot()
	n, p := 12, 3
	total := 15
	s.Audience = &AudienceState{OnAir: true, Total: &total, Reported: 2, Twitch: &n, Player: &p,
		At: time.Unix(1700000000, 0).UTC()}
	return s
}

// TestAudienceTopicLeavesAnUnreportedPlatformOut: a platform with no count is
// an absent key on the retained topic, never a 0.
func TestAudienceTopicLeavesAnUnreportedPlatformOut(t *testing.T) {
	tel, f := newTelemetry(t)
	if err := tel.Publish(context.Background(), audienceSnapshot()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	var body []byte
	for _, m := range f.msgs {
		if m.topic == "polyemesis/studio/audience" {
			body = m.payload
			if !m.retain {
				t.Error("the audience topic was not retained")
			}
		}
	}
	if body == nil {
		t.Fatalf("no audience topic among %d messages", len(f.msgs))
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["total"] != float64(15) || got["twitch"] != float64(12) || got["player"] != float64(3) {
		t.Fatalf("audience payload = %v", got)
	}
	for _, k := range []string{"youtube", "kick"} {
		if _, present := got[k]; present {
			t.Errorf("%s was not reported and is on the topic as %v; it must be absent", k, got[k])
		}
	}
}

func TestNoAudienceTopicWithoutASampler(t *testing.T) {
	tel, f := newTelemetry(t)
	if err := tel.Publish(context.Background(), sampleSnapshot()); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.msgs {
		if strings.HasSuffix(m.topic, "/audience") {
			t.Fatalf("published %s with no sampler", m.topic)
		}
	}
}

// TestAudienceEntitiesReadUnknownNotZero: the discovery value template must
// answer None for an absent key, which Home Assistant shows as "unknown".
func TestAudienceEntitiesReadUnknownNotZero(t *testing.T) {
	tel, f := newTelemetry(t)
	if err := tel.PublishDiscovery(context.Background(), audienceSnapshot()); err != nil {
		t.Fatal(err)
	}
	var d haDiscovery
	if err := json.Unmarshal(f.msgs[0].payload, &d); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"viewers_total", "viewers_youtube", "viewers_twitch", "viewers_kick", "viewers_player"} {
		c, ok := d.Component[key]
		if !ok {
			t.Errorf("no %s entity", key)
			continue
		}
		if c.StateTopic != "polyemesis/studio/audience" || c.Platform != "sensor" {
			t.Errorf("%s: topic %q platform %q", key, c.StateTopic, c.Platform)
		}
		if !strings.Contains(c.ValueTemplate, "else None") || strings.Contains(c.ValueTemplate, "default(0)") {
			t.Errorf("%s: value template %q must read unknown, not 0, for a missing count", key, c.ValueTemplate)
		}
		if c.UniqueID != "polyemesis_studio_"+key {
			t.Errorf("%s: unique id %q", key, c.UniqueID)
		}
	}

	// Without a sampler there are no viewer entities to go permanently unknown.
	tel2, f2 := newTelemetry(t)
	if err := tel2.PublishDiscovery(context.Background(), sampleSnapshot()); err != nil {
		t.Fatal(err)
	}
	var d2 haDiscovery
	_ = json.Unmarshal(f2.msgs[0].payload, &d2)
	if _, ok := d2.Component["viewers_total"]; ok {
		t.Fatal("viewer entities were announced with no sampler")
	}
}
