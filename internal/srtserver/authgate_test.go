package srtserver

import "testing"

// TestAuthGateThrottlesRepeatedWrongTokensButNotASuccess is the #19
// poka-yoke audit fix: nothing bounded how many tokens one peer could try
// per second before this, so a brute-force guess against the SRT listener
// was limited only by handshake cost. This proves the gate trips after
// enough wrong guesses from one peer, and -- the part that matters more --
// that a single legitimate success clears it, so an encoder that mistyped
// its token a couple of times before getting it right is never left blocked.
func TestAuthGateThrottlesRepeatedWrongTokensButNotASuccess(t *testing.T) {
	g := newAuthGate()
	const peer = "203.0.113.7"

	if g.blocked(peer) {
		t.Fatal("a peer that has never failed is already blocked")
	}
	for i := 0; i < authGateThreshold-1; i++ {
		if g.fail(peer) {
			t.Fatalf("gate blocked after %d failures, want it to hold off until %d", i+1, authGateThreshold)
		}
	}
	if g.blocked(peer) {
		t.Fatal("gate blocked before crossing its own threshold")
	}
	if !g.fail(peer) {
		t.Fatalf("gate did not report crossing the threshold on failure #%d", authGateThreshold)
	}
	if !g.blocked(peer) {
		t.Fatal("gate did not block a peer that just crossed the threshold")
	}

	// A DIFFERENT peer must never inherit this one's penalty -- that is the
	// whole reason this is scoped per peer instead of one shared counter.
	if g.blocked("198.51.100.9") {
		t.Fatal("an unrelated peer was blocked by another peer's failures")
	}

	// The legitimate case: this peer eventually presents a real token.
	g.succeed(peer)
	if g.blocked(peer) {
		t.Fatal("a successful admit did not clear the peer's penalty -- a " +
			"reconnecting encoder that once mistyped its token would stay " +
			"throttled forever")
	}
}
