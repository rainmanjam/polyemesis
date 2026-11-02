package mqtt

import (
	"context"
	"encoding/json"
	"testing"
)

// Owner decision on Wave 4 #10: each offload storage target publishes its
// stuck-upload count, and Home Assistant gets one sensor per target for it.
func TestEachStorageTargetPublishesItsStuckCountAndHasASensor(t *testing.T) {
	tel, f := newTelemetry(t)
	snap := sampleSnapshot()
	if err := tel.Publish(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	topic := tel.topics.Storage(Slug("Archive (R2)"))
	var got StorageState
	found := false
	for _, m := range f.msgs {
		if m.topic == topic {
			found = true
			if err := json.Unmarshal(m.payload, &got); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !found || got.Stuck != 3 || got.Name != "Archive (R2)" {
		t.Fatalf("storage topic %s: found=%v %+v", topic, found, got)
	}
	d, _ := publishDiscovery(t)
	c, ok := d.Component["storage_"+Slug("Archive (R2)")+"_stuck"]
	if !ok || c.StateTopic != topic || c.ValueTemplate != "{{ value_json.stuck }}" || c.Platform != "sensor" {
		t.Fatalf("discovery sensor %+v ok=%v", c, ok)
	}

	// A target that goes away has its retained topic cleared.
	snap.Storage = nil
	f.reset()
	if err := tel.Publish(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	cleared := false
	for _, m := range f.msgs {
		cleared = cleared || (m.topic == topic && len(m.payload) == 0)
	}
	if !cleared {
		t.Fatal("a deleted target's retained topic was left behind")
	}
}
