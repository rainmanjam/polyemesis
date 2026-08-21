package rtmpserver

import (
	"net"
	"sync"
	"time"
)

// authGate slows a peer down once it has presented enough wrong stream keys
// to look like guessing rather than a mistake.
//
// Nothing bounded how fast a would-be publisher could try stream keys before
// this (#19, poka-yoke audit): ConstantTimeLookup closes the TIMING side
// channel, but volume was wide open, so brute-forcing a key was only ever
// bounded by TCP handshake cost.
//
// Scoped PER PEER ADDRESS, deliberately not one shared counter for the whole
// listener: a global limiter would let one attacker's flood against ONE key
// throttle every OTHER encoder's legitimate reconnect too, which trades a
// guessing problem for an outage. Per-peer means a peer that floods wrong
// keys can only ever slow down itself.
//
// Only a key that fails lookup entirely (refuseUnknownKey) counts against
// this. A found-but-disabled or found-but-not-ready target already proved the
// caller holds a real credential -- counting those would let a slow or
// not-yet-ready source punish its own legitimate operator, which is not
// guessing and must never be treated as if it were. And any successful admit
// clears a peer's count outright, so a reconnecting encoder that mistyped its
// key once and then got it right is never left carrying a penalty.
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
	// authGateThreshold is how many wrong keys from one peer inside
	// authGateWindow before that peer starts being slowed down. High enough
	// that an operator who fat-fingers a key twice while setting up an
	// encoder never sees it.
	authGateThreshold = 5
	// authGateWindow is how long a peer's failure count is remembered before
	// it resets on its own, so attempts from an hour ago cannot still be
	// counted against a peer that has long since stopped.
	authGateWindow = 30 * time.Second
	// authGateBlock is how long a peer that crossed the threshold is refused
	// outright. Short by design: this is a speed bump against automated
	// guessing, not a lockout an operator has to notice and go clear.
	authGateBlock = 5 * time.Second
	// authGateMaxEntries bounds memory against a peer that rotates its source
	// address to dodge its own history. Once full, the single oldest entry is
	// evicted to make room, so a sustained flood from many addresses cannot
	// grow this without bound.
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

// fail records one wrong-key presentation from peer and reports whether this
// call is what just crossed the threshold, so the caller can log the event
// once rather than on every attempt made while already blocked.
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
