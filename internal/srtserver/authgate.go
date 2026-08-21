package srtserver

import (
	"net"
	"sync"
	"time"
)

// authGate slows a peer down once it has presented enough wrong tokens to
// look like guessing rather than a mistake.
//
// Nothing bounded how fast a would-be publisher could try tokens before this
// (#19, poka-yoke audit): ConstantTimeLookup closes the TIMING side channel,
// but volume was wide open, so brute-forcing a token was only ever bounded by
// SRT handshake cost. Same device as internal/rtmpserver's authGate, and
// duplicated rather than shared for the reason role() and PublisherKey
// already are in both packages: these two listeners are siblings, not one
// import of the other.
//
// Scoped PER PEER ADDRESS, deliberately not one shared counter for the whole
// listener: a global limiter would let one attacker's flood against ONE token
// throttle every OTHER encoder's legitimate reconnect too, which trades a
// guessing problem for an outage. Per-peer means a peer that floods wrong
// tokens can only ever slow down itself.
//
// Only a token that fails lookup entirely counts against this. A found target
// that is then refused for being disabled, already publishing, or wanting the
// wrong encryption already proved the caller holds a real token -- counting
// those would let normal operational refusals punish a legitimate operator,
// which is not guessing and must never be treated as if it were. Any
// successful PUBLISH clears a peer's count outright.
type authGate struct {
	mu    sync.Mutex
	state map[string]*gateEntry
}

type gateEntry struct {
	fails      int
	windowFrom time.Time
	blockUntil time.Time
}

const (
	// authGateThreshold is how many wrong tokens from one peer inside
	// authGateWindow before that peer starts being slowed down.
	authGateThreshold = 5
	// authGateWindow is how long a peer's failure count is remembered before
	// it resets on its own.
	authGateWindow = 30 * time.Second
	// authGateBlock is how long a peer that crossed the threshold is refused
	// outright. Short by design: a speed bump against automated guessing, not
	// a lockout an operator has to notice and go clear.
	authGateBlock = 5 * time.Second
	// authGateMaxEntries bounds memory against a peer that rotates its source
	// address to dodge its own history.
	authGateMaxEntries = 4096
)

func newAuthGate() *authGate {
	return &authGate{state: map[string]*gateEntry{}}
}

// blocked reports whether peer is currently inside its penalty window.
func (g *authGate) blocked(peer string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.state[peer]
	if !ok {
		return false
	}
	return time.Now().Before(e.blockUntil)
}

// fail records one wrong-token presentation from peer and reports whether
// this call is what just crossed the threshold, so the caller can log the
// event once rather than on every attempt made while already blocked.
func (g *authGate) fail(peer string) (justBlocked bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	e, ok := g.state[peer]
	if !ok {
		if len(g.state) >= authGateMaxEntries {
			g.evictOldestLocked()
		}
		e = &gateEntry{windowFrom: now}
		g.state[peer] = e
	}
	if now.Sub(e.windowFrom) > authGateWindow {
		e.fails = 0
		e.windowFrom = now
	}
	e.fails++
	if e.fails >= authGateThreshold && !now.Before(e.blockUntil) {
		e.blockUntil = now.Add(authGateBlock)
		return true
	}
	return false
}

// succeed clears any penalty for peer. A legitimate encoder that
// authenticates must never carry a lingering penalty from an earlier
// unrelated bad guess on the same address.
func (g *authGate) succeed(peer string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.state, peer)
}

func (g *authGate) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	first := true
	for k, e := range g.state {
		if first || e.windowFrom.Before(oldest) {
			oldest = e.windowFrom
			oldestKey = k
			first = false
		}
	}
	if oldestKey != "" {
		delete(g.state, oldestKey)
	}
}

// peerHost strips the port, so a peer is tracked by address rather than by
// address+port, which changes on every single connection and would make this
// gate count nothing.
func peerHost(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
