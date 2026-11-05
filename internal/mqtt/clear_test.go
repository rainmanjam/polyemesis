package mqtt

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// #9 of the 2026-11-04 lingering-state audit. Clear was Publish(Snapshot{}): it
// sent a zeroed host payload to the state topic instead of deleting it, and no
// production code called it. Switching MQTT off left every retained topic
// saying "running" for ever.

func TestClearDeletesEveryRetainedTopicButTheStatus(t *testing.T) {
	tel, f := newTelemetry(t)
	if err := tel.Publish(context.Background(), sampleSnapshot()); err != nil {
		t.Fatal(err)
	}
	published := map[string]bool{}
	for _, m := range f.msgs {
		published[m.topic] = true
	}
	f.reset()

	if err := tel.Clear(context.Background()); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	cleared := map[string]int{}
	for _, m := range f.msgs {
		if !m.retain || len(m.payload) != 0 {
			t.Errorf("%s got retain=%v payload %q, want a zero-length retained delete", m.topic, m.retain, m.payload)
		}
		cleared[m.topic]++
	}
	for topic := range published {
		if cleared[topic] != 1 {
			t.Errorf("%s was published and then cleared %d time(s), want exactly once", topic, cleared[topic])
		}
	}
	if cleared[tel.topics.State()] != 1 {
		t.Errorf("the host state topic was not deleted")
	}
	if cleared[tel.topics.Status()] != 0 {
		t.Error("Clear touched the status topic, which must keep saying offline")
	}
	if cleared[tel.topics.Discovery()] != 0 {
		t.Error("Clear deleted the Home Assistant device; only ClearDiscovery may")
	}
}

func TestClearDiscoveryDeletesTheDevice(t *testing.T) {
	tel, f := newTelemetry(t)
	if err := tel.ClearDiscovery(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.msgs) != 1 || f.msgs[0].topic != tel.topics.Discovery() || !f.msgs[0].retain || len(f.msgs[0].payload) != 0 {
		t.Fatalf("ClearDiscovery published %+v, want one empty retained payload on %s", f.msgs, tel.topics.Discovery())
	}
}

// Omitting a component from a device payload does not remove its entity in
// Home Assistant; sending the key with only its platform does.
func TestARepublishedDiscoveryStubsTheComponentsThatWent(t *testing.T) {
	tel, f := newTelemetry(t)
	snap := sampleSnapshot()
	if err := tel.PublishDiscovery(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var removedKey string
	{
		var first struct {
			Cmps map[string]map[string]any `json:"cmps"`
		}
		if err := json.Unmarshal(f.msgs[0].payload, &first); err != nil {
			t.Fatal(err)
		}
		for key, c := range first.Cmps {
			if c["p"] == "binary_sensor" && c["name"] == "Twitch (main)" {
				removedKey = key
			}
		}
		if removedKey == "" {
			t.Fatalf("the first payload has no running entity for the destination: %s", f.msgs[0].payload)
		}
	}

	// The same set again publishes nothing.
	f.reset()
	if err := tel.PublishDiscovery(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if len(f.msgs) != 0 {
		t.Fatalf("an unchanged set was republished: %d message(s)", len(f.msgs))
	}

	// The destination goes.
	snap.Sources[0].Dests = nil
	if err := tel.PublishDiscovery(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if len(f.msgs) != 1 {
		t.Fatalf("a changed set published %d message(s), want 1", len(f.msgs))
	}
	var second struct {
		Cmps map[string]map[string]any `json:"cmps"`
	}
	if err := json.Unmarshal(f.msgs[0].payload, &second); err != nil {
		t.Fatal(err)
	}
	stub, ok := second.Cmps[removedKey]
	if !ok || len(stub) != 1 || stub["p"] != "binary_sensor" {
		t.Fatalf("the removed entity %s reads %v, want exactly {\"p\":\"binary_sensor\"}", removedKey, stub)
	}

	// After a reconnect the payload is sent again even though nothing changed.
	f.reset()
	tel.ForgetDiscovery()
	if err := tel.PublishDiscovery(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if len(f.msgs) != 1 {
		t.Fatalf("after ForgetDiscovery an unchanged set published %d message(s), want 1", len(f.msgs))
	}
}

// The runner stops the loop before it clears, but a loop stuck on a dead
// broker is given up on after a bounded wait, and the clear then runs beside
// it -- as the discovery announcer always runs beside the loop. Telemetry's
// methods are serialised, so under -race this is quiet; without the mutex the
// published map is written from two goroutines at once.
func TestTelemetryMethodsAreSafeToCallConcurrently(t *testing.T) {
	tel, f := newTelemetry(t)
	tel.pub = &lockedBroker{f: f, mu: &sync.Mutex{}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for n := 0; n < 50; n++ {
				_ = tel.Publish(context.Background(), sampleSnapshot())
				_ = tel.PublishDiscovery(context.Background(), sampleSnapshot())
				tel.Resync()
			}
		})
		wg.Go(func() {
			for n := 0; n < 50; n++ {
				_ = tel.Clear(context.Background())
				tel.ForgetDiscovery()
			}
		})
	}
	wg.Wait()
}

type lockedBroker struct {
	f  *fakeBroker
	mu *sync.Mutex
}

func (l *lockedBroker) Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Publish(ctx, topic, qos, retain, payload)
}

func (l *lockedBroker) Connected() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Connected()
}
