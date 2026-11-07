package activityhub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
	"github.com/rainmanjam/polyemesis/internal/eventstream/streamtest"
	"github.com/rainmanjam/polyemesis/internal/overlay"
)

// The real chain, end to end and with no platform connected: the hub, the bus,
// eventstream.BridgeBus, the events hub, and the alerts overlay's gate. This is
// the producer the catalogue's emission test requires for both activity rows.
func TestEveryKindOfTestAlertReachesTheEventsStreamAndTheOverlay(t *testing.T) {
	bus := events.NewBroker()
	stream := eventstream.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventstream.BridgeBus(ctx, bus, stream)
	waitBridge(t, bus)

	gate := overlay.NewActivityGate(stream, nil)
	// Reactions on: the Facebook tally is the one kind off by default, and a
	// test alert obeys the settings like a real event.
	hub := New(WithBus(bus), WithDisplay(gate), WithSettings(func() db.ActivitySettings {
		s := db.DefaultActivitySettings()
		s.Reactions = true
		return s
	}))
	defer hub.Close()

	admin := streamtest.Subscribe(t, stream)
	read, _, _ := stream.Subscribe(eventstream.Filter{ReadScoped: true}, "")
	defer read.Close()
	ov, _, _ := stream.Subscribe(eventstream.Filter{Overlay: &eventstream.OverlayView{Platforms: []string{"twitch"}}}, "")
	defer ov.Close()
	other, _, _ := stream.Subscribe(eventstream.Filter{Overlay: &eventstream.OverlayView{Platforms: []string{"kick"}}}, "")
	defer other.Close()

	for _, kind := range activity.AllKinds() {
		res := hub.Submit(activity.Sample(kind, "twitch", activity.Overrides{}, time.Now()))
		if !res.Accepted {
			t.Fatalf("%s: %+v", kind, res)
		}
		env := streamtest.Expect(t, admin, eventstream.TypeActivity)
		got, ok := env.Data.(activity.Event)
		if !ok || got.Kind != kind || !got.Test || got.Source.Type != activity.TestSourceType {
			t.Fatalf("%s: envelope data %#v", kind, env.Data)
		}
		if res.Event.OverlayVisible() {
			d := streamtest.Expect(t, ov, eventstream.TypeActivityDisplay)
			disp := d.Data.(eventstream.ActivityDisplay)
			if disp.Kind != string(kind) || disp.Platform != "twitch" || !disp.Test {
				t.Errorf("%s: display %+v", kind, disp)
			}
			b, _ := json.Marshal(disp)
			if strings.Contains(string(b), "test-viewer") || strings.Contains(string(b), "polyemesis.test") {
				t.Errorf("%s: the overlay projection leaks an id: %s", kind, b)
			}
		}
	}
	// The overlay keyed to another platform saw none of it, and nothing but
	// activity.display ever reached an overlay.
	streamtest.None(t, other, eventstream.TypeActivityDisplay, 100*time.Millisecond)
	streamtest.None(t, ov, eventstream.TypeActivity, 50*time.Millisecond)

	// A read token is sent the platform's stream report and no viewer's name.
	var kinds []activity.Kind
	timeout := time.After(time.Second)
drain:
	for {
		select {
		case env := <-read.C:
			if env.Type != eventstream.TypeActivity {
				t.Errorf("a read token was sent %s", env.Type)
				continue
			}
			kinds = append(kinds, env.Data.(activity.Event).Kind)
		case <-timeout:
			break drain
		}
	}
	if len(kinds) != 1 || kinds[0] != activity.KindStream {
		t.Errorf("a read token was sent kinds %v, want only stream", kinds)
	}
}

// What the events stream carries is redacted like every other event: a
// viewer's pasted key never reaches a subscriber or the replay ring.
func TestViewerTextIsRedactedBeforeItIsNumbered(t *testing.T) {
	bus := events.NewBroker()
	stream := eventstream.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventstream.BridgeBus(ctx, bus, stream)
	waitBridge(t, bus)
	hub := New(WithBus(bus))
	defer hub.Close()
	sub := streamtest.Subscribe(t, stream)

	ev := activity.Sample(activity.KindTip, "twitch", activity.Overrides{
		Message: "my key is rtmp://live.example.com/app/live_123456789_abcdefghijklmnopqrstuvwxyz0123",
	}, time.Now())
	hub.Submit(ev)
	env := streamtest.Expect(t, sub, eventstream.TypeActivity)
	if strings.Contains(env.Data.(activity.Event).Message, "abcdefghijklmnopqrstuvwxyz0123") {
		t.Errorf("the stream carries an unredacted key: %q", env.Data.(activity.Event).Message)
	}
}

func TestADisabledKindNeverReachesTheStream(t *testing.T) {
	bus := events.NewBroker()
	stream := eventstream.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventstream.BridgeBus(ctx, bus, stream)
	waitBridge(t, bus)
	hub := New(WithBus(bus), WithSettings(func() db.ActivitySettings {
		s := db.DefaultActivitySettings()
		s.Kinds = map[string][]string{"twitch": {"follow"}}
		return s
	}))
	defer hub.Close()
	sub := streamtest.Subscribe(t, stream)
	hub.Submit(activity.Sample(activity.KindTip, "twitch", activity.Overrides{}, time.Now()))
	streamtest.None(t, sub, eventstream.TypeActivity, 150*time.Millisecond)
}

// waitBridge blocks until BridgeBus has subscribed to the bus: a publish before
// that is dropped by design, which would make these tests race their own setup.
func waitBridge(t *testing.T, bus *events.Broker) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n, _ := bus.Stats(); n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the bridge never subscribed to the bus")
		}
		time.Sleep(time.Millisecond)
	}
}
