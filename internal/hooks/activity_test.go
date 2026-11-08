package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

func sampleActivity(kind activity.Kind) activity.Event {
	return activity.Sample(kind, "twitch", activity.Overrides{}, time.Now()).Normalise(nil)
}

// An empty Triggers list means "every lifecycle trigger", and `activity` is not
// one: a hook that never narrowed its subscription must not start receiving
// every follow on upgrade (D5). Only a hook that NAMES it gets it.
func TestActivityIsOptInOnly(t *testing.T) {
	catchAll := Hook{ID: 1, Name: "all", Enabled: true, URL: "https://example.com/h", Secret: "s"}.Normalized()
	named := Hook{ID: 2, Name: "named", Enabled: true, URL: "https://example.com/h", Secret: "s",
		Triggers: []Trigger{TriggerActivity}}.Normalized()
	other := Hook{ID: 3, Name: "other", Enabled: true, URL: "https://example.com/h", Secret: "s",
		Triggers: []Trigger{TriggerDestinationDown}}.Normalized()
	if catchAll.Wants(TriggerActivity) {
		t.Error("a catch-all hook receives activity")
	}
	if !named.Wants(TriggerActivity) || other.Wants(TriggerActivity) {
		t.Error("only a hook naming the trigger receives it")
	}
	if named.Wants(TriggerDestinationDown) {
		t.Error("naming activity must not subscribe a hook to the lifecycle triggers")
	}
	if !catchAll.Wants(TriggerDestinationDown) || !catchAll.Wants(TriggerOffloadStuck) {
		t.Error("a catch-all hook lost its lifecycle triggers")
	}
	for _, h := range []Hook{catchAll, named, other} {
		if !h.Wants(TriggerTest) {
			t.Error("the test trigger is always wanted")
		}
	}
}

func TestOptInTriggerListsAreDisjointAndKnown(t *testing.T) {
	for _, tr := range AllTriggers() {
		if IsOptIn(tr) {
			t.Errorf("%s is both lifecycle and opt-in", tr)
		}
		if tr == TriggerActivity {
			t.Error("AllTriggers must stay lifecycle-only")
		}
	}
	if got := OptInTriggers(); len(got) != 1 || got[0] != TriggerActivity {
		t.Errorf("OptInTriggers = %v", got)
	}
	if !KnownTrigger(TriggerActivity) || KnownTrigger("activity.nope") {
		t.Error("KnownTrigger must accept the opt-in trigger and refuse a typo")
	}
	h := Hook{ID: 1, Name: "n", Enabled: true, URL: "https://example.com/h", Secret: "s", Triggers: []Trigger{TriggerActivity}}.Normalized()
	if err := h.Validate(); err != nil {
		t.Errorf("a hook subscribing to activity was refused: %v", err)
	}
	if string(TriggerActivity) != "activity" {
		t.Error("the trigger's wire name is frozen")
	}
}

func TestADispatcherSendsActivityToOptInHooksOnly(t *testing.T) {
	rec := &recorder{}
	hooks := []Hook{
		Hook{ID: 1, Name: "all", Enabled: true, URL: "https://example.com/a", Secret: "s"}.Normalized(),
		Hook{ID: 2, Name: "opt", Enabled: true, URL: "https://example.com/b", Secret: "s", Triggers: []Trigger{TriggerActivity}}.Normalized(),
	}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return hooks, nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	ev := sampleActivity(activity.KindTip)
	ev.Test = false
	d.Activity(ev)
	// A fence: a lifecycle event reaches only the catch-all hook, after the
	// activity has been through fanOut.
	d.Publish(Event{Trigger: TriggerIngestPublished, At: time.Now(), Source: SourceRef{ID: 9}})
	waitFor(t, func() bool { return len(rec.seen()) >= 2 })
	time.Sleep(50 * time.Millisecond)

	var activityBodies, lifecycle int
	for _, b := range rec.seen() {
		var env Envelope
		if err := json.Unmarshal([]byte(b), &env); err != nil {
			t.Fatal(err)
		}
		switch env.Trigger {
		case TriggerActivity:
			activityBodies++
			if env.Activity == nil || env.Activity.Kind != activity.KindTip || env.Source != (SourceRef{}) || env.Test {
				t.Errorf("activity envelope: %+v", env)
			}
			if env.SpecVersion != "1" {
				t.Errorf("SpecVersion = %q", env.SpecVersion)
			}
		case TriggerIngestPublished:
			lifecycle++
			if env.Activity != nil || strings.Contains(b, `"activity"`) {
				t.Errorf("a lifecycle delivery carries an activity field: %s", b)
			}
		}
	}
	if activityBodies != 1 || lifecycle != 1 {
		t.Errorf("activity deliveries %d (want 1, only the opt-in hook), lifecycle %d (want 1)", activityBodies, lifecycle)
	}
}

func TestATestActivityMapsToEnvelopeTest(t *testing.T) {
	rec := &recorder{}
	h := Hook{ID: 1, Name: "opt", Enabled: true, URL: "https://example.com/b", Secret: "s", Triggers: []Trigger{TriggerActivity}}.Normalized()
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return []Hook{h}, nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })
	d.Activity(sampleActivity(activity.KindFollow)) // Sample sets Test
	waitFor(t, func() bool { return len(rec.seen()) == 1 })
	var env Envelope
	if err := json.Unmarshal([]byte(rec.seen()[0]), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Test || env.Activity == nil || !env.Activity.Test {
		t.Errorf("test mapping: %+v", env)
	}
}

func TestViewerTextNeverReachesTheWireUnredacted(t *testing.T) {
	dirty := "rtmps://live.twitch.tv/app/" + plantedKey + ": hi"
	rec := &recorder{}
	h := Hook{ID: 1, Name: "opt", Enabled: true, URL: "https://example.com/b", Secret: "s", Triggers: []Trigger{TriggerActivity}}.Normalized()
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return []Hook{h}, nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	ev := sampleActivity(activity.KindGift)
	ev.Message, ev.Title = dirty, dirty
	ev.Actor.Name, ev.Actor.Login = dirty, dirty
	ev.Other = &activity.Actor{Name: dirty}
	ev.Recipients = []activity.Recipient{{Name: dirty}}
	ev.Progress = &activity.Progress{Options: []activity.ProgressOption{{Label: dirty}}}
	d.Activity(ev)
	waitFor(t, func() bool { return len(rec.seen()) == 1 })
	if strings.Contains(rec.seen()[0], plantedKey) {
		t.Errorf("a stream key reached the wire: %s", rec.seen()[0])
	}
	if ev.Message != dirty {
		t.Error("redaction wrote through to the caller's event")
	}
}

func TestTheTestButtonSendsASampleActivity(t *testing.T) {
	rec := &recorder{}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return nil, nil }), WithDoer(rec))
	h := Hook{ID: 1, Name: "opt", Enabled: true, URL: "https://example.com/b", Secret: "s"}.Normalized()
	res, err := d.Test(context.Background(), h, TriggerActivity)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal([]byte(res.Body), &env); err != nil {
		t.Fatal(err)
	}
	if env.Trigger != TriggerActivity || !env.Test || env.Activity == nil || env.Activity.Kind != activity.KindTip {
		t.Errorf("test delivery: %+v", env)
	}
}

func TestNilDispatcherActivityIsSafe(t *testing.T) {
	var d *Dispatcher
	d.Activity(sampleActivity(activity.KindFollow))
}

func TestRedactActorNil(t *testing.T) {
	if redactActor(nil) != nil {
		t.Error("redactActor(nil) != nil")
	}
}

// With no hook naming the trigger, an activity is not queued, redacted or
// counted: a follow-bot costs an install with no activity hook nothing.
func TestActivityIsNotQueuedWhenNoHookWantsIt(t *testing.T) {
	rec := &recorder{}
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return oneHook(), nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })
	for i := 0; i < 5000; i++ {
		d.Activity(sampleActivity(activity.KindFollow))
	}
	if st := d.Stats(); st.Queued != 0 || st.Dropped != 0 {
		t.Errorf("an unwanted activity touched the queues: %+v", st)
	}
}

// A burst of follows must not displace a lifecycle delivery, in the dispatcher's
// intake or in the endpoint's own queue, even when the endpoint is stuck.
func TestAnActivityBurstNeverDropsALifecycleDelivery(t *testing.T) {
	rec := &recorder{block: make(chan struct{})}
	h := Hook{ID: 1, Name: "both", Enabled: true, URL: "https://example.com/h", Secret: "s",
		Triggers: []Trigger{TriggerActivity, TriggerDestinationDown}}.Normalized()
	d := NewDispatcher(testLogger(t), SourceFunc(func() ([]Hook, error) { return []Hook{h}, nil }),
		WithDoer(rec), WithReloadInterval(10*time.Millisecond))
	runDispatcher(t, d)
	waitFor(t, func() bool { return d.HasHooks() })

	d.Publish(Event{Trigger: TriggerDestinationDown, At: time.Now(), Source: SourceRef{ID: 1}})
	for i := 0; i < 2000; i++ {
		d.Activity(sampleActivity(activity.KindFollow))
	}
	d.Publish(Event{Trigger: TriggerDestinationDown, At: time.Now(), Source: SourceRef{ID: 2}})
	time.Sleep(100 * time.Millisecond)
	close(rec.block)
	waitFor(t, func() bool {
		var down int
		for _, b := range rec.seen() {
			if strings.Contains(b, `"trigger":"destination.down"`) {
				down++
			}
		}
		return down == 2
	})
}
