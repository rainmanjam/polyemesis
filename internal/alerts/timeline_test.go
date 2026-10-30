package alerts

import (
	"context"
	"testing"
	"time"
)

// The timeline is a second reader of the same event stream the webhooks use,
// so it has to hear every event the notifier accepts, even when no rule is
// configured. A recorder that only ran for delivered events would leave the
// history empty for every install that has not wired a webhook.
func TestRecorderHearsEveryAcceptedEventWithoutAnyRule(t *testing.T) {
	got := make(chan Event, 8)
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }),
		WithRecorder(func(ev Event) { got <- ev }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)

	n.Publish(Event{Type: TypeFailoverSwitched, Key: "failover", Title: "Failover: primary lost", At: base})

	select {
	case ev := <-got:
		if ev.Type != TypeFailoverSwitched || ev.Title != "Failover: primary lost" {
			t.Fatalf("recorder got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recorder never heard the event")
	}
}

// Publish is called from the reconcile loop and must never wait on a sink. A
// recorder that is wedged on a slow database has to cost the timeline events,
// not the stream.
func TestPublishDoesNotWaitOnAStuckRecorder(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	n := New(quietLog(), RuleFunc(func() ([]Rule, error) { return nil, nil }),
		WithRecorder(func(Event) { <-block }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 2000; i++ {
			n.Publish(downEvent("a", base))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked behind a stuck recorder")
	}
	// Publish drops rather than waits, so returning is not enough: the run
	// loop itself must keep draining its queue. A loop wedged on the recorder
	// leaves the queue full forever.
	deadline := time.Now().Add(5 * time.Second)
	for len(n.events) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("run loop is wedged: %d events still queued", len(n.events))
		}
		time.Sleep(time.Millisecond)
	}
}

func TestIsTimelineTypeCoversTheOperatorHistoryAndNothingElse(t *testing.T) {
	for _, ty := range []Type{
		TypeFailoverSwitched, TypeDestinationDown, TypeDestinationRecovered,
		TypeIngestLost, TypeIngestRecovered, TypeDestinationFallingBehind, TypeDestinationCaughtUp,
		TypeDestinationHeld,
	} {
		if !IsTimelineType(ty) {
			t.Errorf("%s should be on the timeline", ty)
		}
	}
	for _, ty := range []Type{TypeLoginFailed, TypeSettingsChanged, TypeAPITokenCreated, TypeClipping} {
		if IsTimelineType(ty) {
			t.Errorf("%s is not operator history and must stay off the timeline", ty)
		}
	}
}
