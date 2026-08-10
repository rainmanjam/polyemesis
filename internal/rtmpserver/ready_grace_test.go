package rtmpserver

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
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

// The grace has to be WIRED IN, not merely present.
//
// The tests above call awaitReady directly, so every one of them would still
// pass if the call were deleted from the serve path -- which is exactly what
// happened: a mutant that removed the call site failed to apply, nothing went
// red, and the gap was found by noticing the mutant had not applied rather than
// by any assertion. A helper nothing calls is dead code that looks like a fix.
//
// Source-level because staging it behaviourally needs a real RTMP handshake
// against a listener whose engine is mid-respawn, and the value here is pinning
// the three properties that make the wait safe rather than re-proving the
// handshake works.
func TestTheGraceIsWiredIntoTheAdmissionPath(t *testing.T) {
	b, err := os.ReadFile("rtmpserver.go")
	if err != nil {
		t.Fatalf("read rtmpserver.go: %v", err)
	}
	src := string(b)

	if !strings.Contains(src, "s.awaitReady(key, readyGrace)") {
		t.Error("the serve path no longer waits for a subscriber before refusing. " +
			"awaitReady exists but nothing calls it, so a reconnecting encoder is " +
			"refused for the length of its ingest child's respawn")
	}
	// ONLY on refuseNotReady, and only for a target whose subscriber is actually
	// expected, and only within the per-key waiter cap. An unknown key, a
	// disabled source, or a source that is not on RTMP at all must be answered
	// at once, or the grace becomes a way to hold connections open against the
	// listener with any well-formed key.
	for _, clause := range []string{
		"verdict == refuseNotReady",
		"target.Pending",
		"s.enterWait(target.Key())",
	} {
		if !strings.Contains(src, clause) {
			t.Errorf("the wait is no longer gated on %s; a refusal that can never "+
				"become an admission would hold a connection open for the grace", clause)
		}
	}
	// Every claimed waiter slot must be given back, or the cap leaks shut and
	// the grace stops working entirely after maxWaitersPerKey reconnects.
	if !strings.Contains(src, "s.leaveWait(target.Key())") {
		t.Error("the waiter slot is never released; after maxWaitersPerKey reconnects " +
			"the grace would be permanently unavailable for that source")
	}
	// The handshake deadline must be pushed out first. It is set before the
	// handshake and not cleared until admission succeeds, so waiting spends the
	// same budget the handshake already drew on: a slow handshake plus a full
	// grace blows it, and the session is admitted and then fails its first read
	// with i/o timeout -- worse than the refusal being fixed.
	wait := strings.Index(src, "s.awaitReady(key, readyGrace)")
	gate := strings.Index(src, "verdict == refuseNotReady")
	deadline := strings.Index(src, "handshakeTimeout + readyGrace")
	if deadline < 0 {
		t.Error("the handshake deadline is no longer extended before the wait; a slow " +
			"handshake plus a full grace expires it, and the publisher is admitted " +
			"and then fails its first read with i/o timeout")
	} else if gate < 0 || deadline < gate || deadline > wait {
		t.Errorf("the deadline extension is not between the refuseNotReady gate and "+
			"the wait (gate=%d deadline=%d wait=%d); it has to happen before the "+
			"wait spends the budget", gate, deadline, wait)
	}
}

// The grace, proved through a real publisher rather than through the source.
//
// This is the test the source-level one above is a stand-in for. A live FFmpeg
// connects to a listener whose target is NOT ready -- the state an ingest child
// mid-respawn leaves behind -- and a subscriber appears a second later. If the
// grace is wired into the serve path the publisher is admitted and keeps
// running; without it the connection is refused and FFmpeg exits at once.
//
// Deliberately end-to-end: the previous version of this coverage called
// awaitReady directly and would have passed with the call site deleted, which
// is exactly the mistake that produced the gap.
func TestARealPublisherSurvivesAnIngestChildRespawn(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real FFmpeg")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}

	// Not ready when the publisher arrives, ready shortly after -- a supervised
	// ingest child coming back on its 500ms-5s backoff.
	var ready atomic.Bool
	s := New(quiet(), "127.0.0.1:0", func(string) (Target, bool) {
		return Target{SourceID: 1, Name: "Main", Enabled: true, Ready: ready.Load(), Pending: true}, true
	})
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()

	go func() {
		time.Sleep(1200 * time.Millisecond)
		ready.Store(true)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	pub := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-pix_fmt", "yuv420p", "-b:v", "300k", "-c:a", "aac", "-b:a", "64k",
		"-t", "6", "-f", "flv", "rtmp://"+addr+"/live/k")
	out, err := pub.CombinedOutput()
	if err != nil {
		t.Fatalf("a publisher arriving while its ingest child was respawning was "+
			"REFUSED. That is an ordinary encoder reconnect after a blip, and RTMP "+
			"carries no typed rejection, so the encoder sees only a failed connect: "+
			"%v\n%s", err, out)
	}

	// It was admitted because a subscriber appeared, not because Ready was
	// ignored: the flag really did start false.
	if !ready.Load() {
		t.Error("the publisher was admitted without the target ever becoming ready")
	}
}

// An SRT-mode source's token must be refused AT ONCE, not held for the grace.
//
// This is the hole the grace shipped with. A target is registered for every
// source whatever its ingest mode, so any valid token for an SRT source was
// found, enabled, and not ready -- the one verdict the grace waits on -- for a
// state no amount of waiting could change. Every connect burned the full six
// seconds, in parallel, against a listener whose comment claimed this could not
// happen. Target.Pending is what tells the two apart.
func TestASourceThatWillNeverBecomeReadyIsRefusedImmediately(t *testing.T) {
	srt := Target{SourceID: 1, Name: "Main", Enabled: true, Ready: false, Pending: false}
	if admit(srt, true) != refuseNotReady {
		t.Fatalf("precondition: an SRT-mode source's token still lands on refuseNotReady")
	}
	if srt.Pending {
		t.Error("a source with no RTMP subscriber on its way must not be marked Pending; " +
			"the listener would wait out the full grace for it on every connect")
	}
}

// The waiter cap: one reconnect gets its grace, a flood does not multiply it.
func TestTheGraceIsCappedPerPublisherSlot(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", nil)
	k := PublisherKey{SourceID: 7}

	for i := 0; i < maxWaitersPerKey; i++ {
		if !s.enterWait(k) {
			t.Fatalf("waiter %d was refused a slot inside the cap", i+1)
		}
	}
	if s.enterWait(k) {
		t.Errorf("a %dth concurrent waiter was admitted; the cap is %d, and past it "+
			"one valid key multiplies into arbitrarily many held sockets",
			maxWaitersPerKey+1, maxWaitersPerKey)
	}

	// A different slot is unaffected: one source's flood must not starve another.
	if !s.enterWait(PublisherKey{SourceID: 8}) {
		t.Error("a different source was refused a waiter slot; the cap is per-slot")
	}
	// And the backup is a different slot from its own primary.
	if !s.enterWait(PublisherKey{SourceID: 7, Backup: true}) {
		t.Error("the backup slot was refused because its primary was full; they are " +
			"two independent sessions and exist in order to run at the same time")
	}

	s.leaveWait(k)
	if !s.enterWait(k) {
		t.Error("a slot released by leaveWait was not reusable")
	}
}

// The counter must not leak, in either direction.
func TestWaiterSlotsAreReturnedExactly(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", nil)
	k := PublisherKey{SourceID: 1}

	for i := 0; i < 50; i++ {
		if !s.enterWait(k) {
			t.Fatalf("iteration %d: a slot was refused after every prior one was returned", i)
		}
		s.leaveWait(k)
	}
	s.mu.Lock()
	n, present := s.waiters[k]
	s.mu.Unlock()
	if present {
		t.Errorf("the waiters map still holds an entry for a drained slot (count %d); "+
			"a long-lived install accumulates one per source that ever reconnected", n)
	}
}

// enterWait and leaveWait are called from separate connection goroutines.
func TestWaiterAccountingIsRaceFree(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", nil)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := PublisherKey{SourceID: int64(i % 4)}
			if s.enterWait(k) {
				s.leaveWait(k)
			}
		}(i)
	}
	wg.Wait()
	s.mu.Lock()
	left := len(s.waiters)
	s.mu.Unlock()
	if left != 0 {
		t.Errorf("%d waiter entries survived a balanced run", left)
	}
}
