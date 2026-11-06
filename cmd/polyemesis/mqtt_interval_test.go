package main

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// A saved publish interval takes effect on the running connection: the loop
// moves to it, and the link is NOT cycled to do so (a reconnect would flap the
// availability topic for a setting that has nothing to do with the link).
func TestASavedPublishIntervalReachesTheRunningLoopWithoutAReconnect(t *testing.T) {
	r, store, ctx := reconcileFixture(t)
	r.dial = newFakeRetainBroker().dial

	enableMQTT(t, store, nil)
	r.reconcile(ctx)
	first, _ := liveClient(r)
	if first == nil {
		t.Fatal("reconcile built no client, so every assertion below would be vacuous")
	}

	enableMQTT(t, store, func(c *db.MQTTSettings) { c.IntervalSecond = 30 })
	r.reconcile(ctx)

	second, _ := liveClient(r)
	if second != first {
		t.Error("changing only the publish interval reconnected to the broker")
	}
	r.mu.Lock()
	tel := r.tel
	r.mu.Unlock()
	if got := tel.Every(); got != 30*time.Second {
		t.Errorf("the loop publishes every %v after the interval was saved as 30s", got)
	}
}
