// Package authgate is a per-peer-IP auth backoff: it slows a peer down once
// it has presented enough wrong credentials to look like guessing rather
// than a mistake.
//
// Nothing bounded how fast a would-be publisher could try credentials before
// this (#19, poka-yoke audit): a constant-time lookup closes the TIMING side
// channel, but volume was wide open, so brute-forcing a credential was only
// ever bounded by handshake cost.
//
// This is shared by internal/rtmpserver and internal/srtserver rather than
// forked per listener. A rate limiter keyed by peer IP is not protocol
// specific -- it is generic infrastructure, unlike role() or PublisherKey,
// which genuinely differ per protocol. Duplicating a security control is its
// own hazard: a future fix to the backoff (a bug, a bypass) would land in
// one copy and silently not the other, which is exactly the class of
// recurring mistake the audit was about. Each caller supplies its own wording
// ("stream keys", "tokens") in its own log lines; the gate itself is neutral.
//
// Scoped PER PEER ADDRESS, deliberately not one shared counter for the whole
// listener: a global limiter would let one attacker's flood against ONE
// credential throttle every OTHER encoder's legitimate reconnect too, which
// trades a guessing problem for an outage. Per-peer means a peer that floods
// wrong credentials can only ever slow down itself.
//
// Only a credential that fails lookup entirely counts against this. A found
// target that is then refused for some other reason -- disabled, not ready,
// already publishing, wrong encryption -- already proved the caller holds a
// real credential; counting those would let a normal operational refusal
// punish a legitimate operator, which is not guessing and must never be
// treated as if it were. Any successful admit clears a peer's count outright,
// so a reconnecting encoder that mistyped its credential once and then got it
// right is never left carrying a penalty.
package authgate

import (
	"net"
	"sync"
	"time"
)

// Gate is one listener's per-peer backoff state.
type Gate struct {
	mu    sync.Mutex
	state map[string]*gateEntry
}

type gateEntry struct {
	fails      int
	windowFrom time.Time
	blockUntil time.Time
}

const (
	// Threshold is how many wrong credentials from one peer inside Window
	// before that peer starts being slowed down. High enough that an
	// operator who fat-fingers a credential twice while setting up an
	// encoder never sees it.
	Threshold = 5
	// Window is how long a peer's failure count is remembered before it
	// resets on its own, so attempts from an hour ago cannot still be
	// counted against a peer that has long since stopped.
	Window = 30 * time.Second
	// Block is how long a peer that crossed Threshold is refused outright.
	// Short by design: this is a speed bump against automated guessing, not
	// a lockout an operator has to notice and go clear.
	Block = 5 * time.Second
	// MaxEntries bounds memory against a peer that rotates its source
	// address to dodge its own history. Once full, the single oldest entry
	// is evicted to make room, so a sustained flood from many addresses
	// cannot grow this without bound.
	MaxEntries = 4096
)

// New builds an empty Gate.
func New() *Gate {
	return &Gate{state: map[string]*gateEntry{}}
}

// Blocked reports whether peer is currently inside its penalty window.
func (g *Gate) Blocked(peer string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.state[peer]
	if !ok {
		return false
	}
	return time.Now().Before(e.blockUntil)
}

// Fail records one wrong-credential presentation from peer and reports
// whether this call is what just crossed Threshold, so the caller can log
// the event once rather than on every attempt made while already blocked.
func (g *Gate) Fail(peer string) (justBlocked bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	e, ok := g.state[peer]
	if !ok {
		if len(g.state) >= MaxEntries {
			g.evictOldestLocked()
		}
		e = &gateEntry{windowFrom: now}
		g.state[peer] = e
	}
	if now.Sub(e.windowFrom) > Window {
		e.fails = 0
		e.windowFrom = now
	}
	e.fails++
	if e.fails >= Threshold && !now.Before(e.blockUntil) {
		e.blockUntil = now.Add(Block)
		return true
	}
	return false
}

// Succeed clears any penalty for peer. A legitimate encoder that
// authenticates must never carry a lingering penalty from an earlier
// unrelated bad guess on the same address.
func (g *Gate) Succeed(peer string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.state, peer)
}

func (g *Gate) evictOldestLocked() {
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

// PeerHost strips the port, so a peer is tracked by address rather than by
// address+port, which changes on every single connection and would make
// this gate count nothing.
func PeerHost(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
