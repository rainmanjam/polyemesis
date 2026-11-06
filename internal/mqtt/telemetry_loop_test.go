package mqtt

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// upPublisher is a link that is always up and takes every publish.
type upPublisher struct{}

func (upPublisher) Publish(context.Context, string, byte, bool, []byte) error { return nil }
func (upPublisher) Connected() bool                                           { return true }

// A saved publish interval has to reach a loop that is already waiting on the
// old one. Before, the ticker was built once from the value the connection was
// opened with, so the new interval did nothing until a reconnect.
func TestALoopWaitingOnALongIntervalPicksUpAShorterOne(t *testing.T) {
	tel := NewTelemetry(upPublisher{}, newTopics(t, "", "studio"), quietLog())
	tel.SetEvery(time.Hour)

	var ticks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tel.Loop(ctx, func() Snapshot { ticks.Add(1); return sampleSnapshot() })
	}()
	t.Cleanup(func() { cancel(); <-done })

	tel.SetEvery(5 * time.Millisecond)

	deadline := time.After(5 * time.Second)
	for ticks.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("the loop is still on the old hour-long interval after SetEvery (ticks=%d)", ticks.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestEveryReportsTheIntervalInForce(t *testing.T) {
	tel := NewTelemetry(upPublisher{}, newTopics(t, "", "studio"), quietLog())
	if got := tel.Every(); got != DefaultEvery {
		t.Errorf("a new Telemetry publishes every %v, want %v", got, DefaultEvery)
	}
	tel.SetEvery(30 * time.Second)
	if got := tel.Every(); got != 30*time.Second {
		t.Errorf("Every = %v, want 30s", got)
	}
	tel.SetEvery(0) // never a zero ticker
	if got := tel.Every(); got != DefaultEvery {
		t.Errorf("a zero interval gave %v, want the default %v", got, DefaultEvery)
	}
}
