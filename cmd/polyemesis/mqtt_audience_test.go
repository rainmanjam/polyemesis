package main

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/mqtt"
)

// TestMQTTSnapshotCarriesTheAudienceOnceTheFeedIsSet: the runner starts before
// the API server that owns the sampler, so the feed is empty at first and the
// snapshot then carries no audience; once set, every snapshot reads it.
func TestMQTTSnapshotCarriesTheAudienceOnceTheFeedIsSet(t *testing.T) {
	feed := &mqttAudienceFeed{}
	r := &mqttRunner{version: "test", engines: func() []mqttEngine { return nil }, audience: feed}
	if got := r.snapshot().Audience; got != nil {
		t.Fatalf("audience before the feed was set = %+v, want nil", got)
	}
	n := 7
	feed.set(func() *mqtt.AudienceState { return &mqtt.AudienceState{OnAir: true, Total: &n} })
	got := r.snapshot().Audience
	if got == nil || got.Total == nil || *got.Total != 7 {
		t.Fatalf("audience after set = %+v", got)
	}
	// A runner a test built without a feed at all.
	bare := &mqttRunner{version: "test", engines: func() []mqttEngine { return nil }}
	if bare.snapshot().Audience != nil {
		t.Fatal("a runner with no feed published an audience")
	}
}
