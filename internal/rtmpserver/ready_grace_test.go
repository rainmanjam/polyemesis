package rtmpserver

import (
	"sync/atomic"
	"testing"
	"time"
)

// A publisher whose ingest child is mid-restart is WAITED FOR, not refused.
//
// Ready means a subscriber is attached, and the ingest child that provides one
// is a supervised FFmpeg carrying no reconnect flags: it exits whenever its
// publisher does, and the supervisor respawns it on a 500ms-5s backoff. So an
// encoder reconnecting after a network blip -- the most ordinary failure there
// is -- arrives precisely while nothing is subscribed.
//
// Refusing there turned a recoverable hiccup into a dropped broadcast: RTMP
// carries no typed rejection, so the encoder sees only a failed connect, and an
// encoder without aggressive retry stays down until someone notices.
func TestAPublisherIsHeldWhileItsIngestChildComesBack(t *testing.T) {
	// atomic, not a plain bool: awaitReady polls from this goroutine while the
	// one below flips the flag, which is exactly the shape -race exists to
	// catch. The subscriber attaching really is concurrent with the wait.
	var ready atomic.Bool
	s := &Server{
		lookup: func(string) (Target, bool) {
			return Target{SourceID: 1, Name: "Main", Enabled: true, Ready: ready.Load()}, true
		},
	}

	// Becomes ready well inside the grace, as a respawning child would.
	go func() {
		time.Sleep(300 * time.Millisecond)
		ready.Store(true)
	}()

	start := time.Now()
	got, ok := s.awaitReady("tok", 3*time.Second)
	if !ok {
		t.Fatal("a publisher was refused while its ingest child was restarting; " +
			"that is an ordinary encoder reconnect, not a fault")
	}
	if !got.Ready {
		t.Error("awaitReady returned a target that is still not ready")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waited %v for a subscriber that attached in 300ms; the poll is "+
			"not seeing the transition promptly", elapsed)
	}
}

// The grace is BOUNDED. A source that is genuinely not on RTMP -- no ingest
// child will ever dial in -- must still be refused, or a listener could be held
// open indefinitely by anyone holding a valid key for the wrong protocol.
func TestTheGraceExpiresWhenNothingEverSubscribes(t *testing.T) {
	s := &Server{
		lookup: func(string) (Target, bool) {
			return Target{SourceID: 1, Name: "Main", Enabled: true, Ready: false}, true
		},
	}
	start := time.Now()
	if _, ok := s.awaitReady("tok", 400*time.Millisecond); ok {
		t.Error("awaitReady admitted a publisher that never gained a subscriber")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the wait ran %v past a 400ms grace; it must be bounded", elapsed)
	}
}

// Only "not ready" is waited on. An unknown key or a disabled source is answered
// at once -- otherwise the grace becomes a way to hold connections open against
// the listener with any well-formed key.
func TestOnlyTheNotReadyVerdictIsWorthWaitingOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		t     Target
		found bool
		want  verdict
	}{
		{"unknown key", Target{}, false, refuseUnknownKey},
		{"disabled source", Target{SourceID: 1, Enabled: false, Ready: true}, true, refuseDisabled},
		{"enabled but no subscriber", Target{SourceID: 1, Enabled: true, Ready: false}, true, refuseNotReady},
		{"ready", Target{SourceID: 1, Enabled: true, Ready: true}, true, admitPublish},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := admit(tc.t, tc.found); got != tc.want {
				t.Errorf("admit = %v, want %v", got, tc.want)
			}
		})
	}
}
