package authgate

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// TestGateThrottlesRepeatedFailuresButNotASuccess is the #19 poka-yoke audit
// fix: nothing bounded how many credentials one peer could try per second
// before this, so a brute-force guess was limited only by handshake cost.
// This proves the gate trips after enough wrong guesses from one peer, and
// -- the part that matters more -- that a single legitimate success clears
// it, so a caller that mistyped its credential a couple of times before
// getting it right is never left blocked.
func TestGateThrottlesRepeatedFailuresButNotASuccess(t *testing.T) {
	g := New()
	const peer = "203.0.113.7"

	if g.Blocked(peer) {
		t.Fatal("a peer that has never failed is already blocked")
	}
	for i := 0; i < Threshold-1; i++ {
		if g.Fail(peer) {
			t.Fatalf("gate blocked after %d failures, want it to hold off until %d", i+1, Threshold)
		}
	}
	if g.Blocked(peer) {
		t.Fatal("gate blocked before crossing its own threshold")
	}
	if !g.Fail(peer) {
		t.Fatalf("gate did not report crossing the threshold on failure #%d", Threshold)
	}
	if !g.Blocked(peer) {
		t.Fatal("gate did not block a peer that just crossed the threshold")
	}

	// A DIFFERENT peer must never inherit this one's penalty -- that is the
	// whole reason this is scoped per peer instead of one shared counter.
	if g.Blocked("198.51.100.9") {
		t.Fatal("an unrelated peer was blocked by another peer's failures")
	}

	// The legitimate case: this peer eventually presents a real credential.
	g.Succeed(peer)
	if g.Blocked(peer) {
		t.Fatal("a successful admit did not clear the peer's penalty -- a " +
			"reconnecting caller that once mistyped its credential would " +
			"stay throttled forever")
	}
}

// TestGateFailAfterCrossingThresholdReportsFalseOnce checks that justBlocked
// is only reported on the call that actually crosses the line -- every
// subsequent failure while already inside the block window must not report
// it again, since the caller only wants to log the transition once.
func TestGateFailAfterCrossingThresholdReportsFalseOnce(t *testing.T) {
	g := New()
	const peer = "203.0.113.8"

	for i := 0; i < Threshold-1; i++ {
		g.Fail(peer)
	}
	if !g.Fail(peer) {
		t.Fatal("expected the threshold-crossing call to report justBlocked")
	}
	if g.Fail(peer) {
		t.Fatal("a call made while already blocked reported justBlocked again")
	}
}

// TestGateWindowResetsFailureCount proves a peer's failures are forgotten
// once Window has elapsed, so an attempt from long ago cannot be added to a
// fresh run of guesses and trip the gate early.
func TestGateWindowResetsFailureCount(t *testing.T) {
	g := New()
	const peer = "203.0.113.9"

	for i := 0; i < Threshold-1; i++ {
		g.Fail(peer)
	}

	// Backdate the window so the next Fail sees it as expired, without
	// waiting Window in real time.
	g.mu.Lock()
	g.state[peer].windowFrom = time.Now().Add(-Window - time.Second)
	g.mu.Unlock()

	if g.Fail(peer) {
		t.Fatal("gate blocked on the first failure of a fresh window")
	}
	if g.Blocked(peer) {
		t.Fatal("a single failure in a fresh window must not block")
	}
}

// TestGateEvictsOldestWhenFull proves MaxEntries bounds memory: once the
// state map is full, the single oldest entry (by windowFrom) is evicted to
// make room for a new peer, rather than growing without bound.
func TestGateEvictsOldestWhenFull(t *testing.T) {
	g := New()

	// Seed one entry with an artificially old windowFrom so it is the
	// guaranteed oldest, then fill the rest of the table.
	oldest := "10.0.0.1"
	g.Fail(oldest)
	g.mu.Lock()
	g.state[oldest].windowFrom = time.Now().Add(-time.Hour)
	g.mu.Unlock()

	for i := 1; i < MaxEntries; i++ {
		g.Fail("10.0.1." + strconv.Itoa(i))
	}
	if len(g.state) != MaxEntries {
		t.Fatalf("state has %d entries, want %d", len(g.state), MaxEntries)
	}

	// One more distinct peer must evict the oldest rather than grow past
	// MaxEntries.
	g.Fail("10.0.0.99")
	if len(g.state) != MaxEntries {
		t.Fatalf("state grew to %d entries, want capped at %d", len(g.state), MaxEntries)
	}
	if _, ok := g.state[oldest]; ok {
		t.Fatal("the oldest entry was not evicted to make room")
	}
}

func TestPeerHostStripsPort(t *testing.T) {
	addr := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 4242}
	if got := PeerHost(addr); got != "192.0.2.1" {
		t.Fatalf("PeerHost(%v) = %q, want %q", addr, got, "192.0.2.1")
	}
}

// TestPeerHostFallsBackWhenNoPort proves an address with no port (which
// SplitHostPort rejects) is still usable as a peer key rather than panicking
// or returning an empty key.
func TestPeerHostFallsBackWhenNoPort(t *testing.T) {
	addr := fakeAddr("no-port-here")
	if got := PeerHost(addr); got != "no-port-here" {
		t.Fatalf("PeerHost(%v) = %q, want the raw address unchanged", addr, got)
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }
