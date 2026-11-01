package captions

import (
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// CueSink receives one caption cue for one destination, at the moment that
// destination's broadcast delay says the viewers there see it. A sink is the
// per-output consumer (the SEI injector, the YouTube POST transport); none is
// built in this package, so the engine's production sink is inert until one of
// those lands.
type CueSink interface {
	Deliver(destination int64, cue transcribe.LiveCaption)
}

// CueTarget is one captioned destination and the broadcast delay it runs with.
type CueTarget struct {
	ID    int64
	Delay time.Duration
}

// AfterFunc schedules f to run after d and returns a function that cancels it
// and reports whether the cancel stopped a run that had not yet started. It is
// time.AfterFunc's shape, so tests can supply a fake clock.
type AfterFunc func(d time.Duration, f func()) (stop func() bool)

// ReleaseAt is the wall-clock time a cue reaches a destination with the given
// delay. LiveCaption.LagMS is measured from the END of the speech to the live
// edge, and At is the time the line was produced at that live edge. So the
// speech started at At - LagMS - (EndMS - StartMS), and the destination shows it
// delay later. Measuring from At alone would release every caption 2 to 5
// seconds late, which is the lag the delay is meant to cancel.
func ReleaseAt(c transcribe.LiveCaption, delay time.Duration) time.Time {
	return c.SpokenAt().Add(delay)
}

// CueRouter fans captions out to destinations behind their own broadcast delay
// (#11). A cue reaches a destination with delay D at ReleaseAt(cue, D), so a
// destination with no delay gets it as soon as it is recognised and a 30 s
// destination waits until 30 s after the words were spoken. Late
// cues, ones whose release time has already passed, are delivered at once rather
// than dropped: a late caption is still a caption.
//
// Emit runs on the captioner's goroutine while SetTargets runs on the reconcile
// goroutine, so every field is behind mu. Timer callbacks re-check their own
// generation under mu, so a cancelled or replaced delivery never reaches the sink.
type CueRouter struct {
	mu      sync.Mutex
	sink    CueSink
	after   AfterFunc
	now     func() time.Time
	targets map[int64]*routeTarget
	closed  bool
	seq     uint64
}

// routeTarget is one destination's live delay and its not-yet-fired timers.
type routeTarget struct {
	delay   time.Duration
	pending map[*pendingCue]func() bool
}

// pendingCue identifies one scheduled delivery so a timer callback can find and
// forget itself. It carries a sequence number because pointers to distinct
// zero-size values may compare equal in Go.
type pendingCue struct{ seq uint64 }

// NewCueRouter returns a router with no targets. A nil after or now uses the
// real clock; a nil sink drops everything, which is the safe default.
func NewCueRouter(sink CueSink, after AfterFunc, now func() time.Time) *CueRouter {
	if after == nil {
		after = func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		}
	}
	if now == nil {
		now = time.Now
	}
	return &CueRouter{sink: sink, after: after, now: now, targets: map[int64]*routeTarget{}}
}

// SetTargets replaces the destination set. A destination that leaves the set, or
// whose delay changes, has its pending deliveries cancelled: a caption queued
// for the old delay must not arrive at the new one.
func (r *CueRouter) SetTargets(targets []CueTarget) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	want := make(map[int64]time.Duration, len(targets))
	for _, t := range targets {
		if t.Delay < 0 {
			t.Delay = 0
		}
		want[t.ID] = t.Delay
	}
	for id, rt := range r.targets {
		delay, keep := want[id]
		if keep && delay == rt.delay {
			continue
		}
		cancelAll(rt)
		delete(r.targets, id)
	}
	for id, delay := range want {
		if _, ok := r.targets[id]; !ok {
			r.targets[id] = &routeTarget{delay: delay, pending: map[*pendingCue]func() bool{}}
		}
	}
}

// Emit sends a cue to every target. The cue is copied per target, so a sink that
// edits its copy cannot change what another destination receives.
func (r *CueRouter) Emit(c transcribe.LiveCaption) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	for id, rt := range r.targets {
		wait := ReleaseAt(c, rt.delay).Sub(r.now())
		if wait <= 0 {
			r.deliver(id, c)
			continue
		}
		r.schedule(id, rt, c, wait)
	}
}

// schedule arms one timer for one destination. The callback takes mu before it
// looks at the table, so a cancel that wins the race is honoured.
func (r *CueRouter) schedule(id int64, rt *routeTarget, c transcribe.LiveCaption, wait time.Duration) {
	r.seq++
	key := &pendingCue{seq: r.seq}
	var stop func() bool
	stop = r.after(wait, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		cur, ok := r.targets[id]
		if !ok || cur != rt {
			return
		}
		if _, live := cur.pending[key]; !live {
			return
		}
		delete(cur.pending, key)
		if !r.closed {
			r.deliver(id, c)
		}
	})
	rt.pending[key] = stop
}

// deliver hands one cue to the sink. The caller holds mu, so the sink must not
// call back into the router.
func (r *CueRouter) deliver(id int64, c transcribe.LiveCaption) {
	if r.sink != nil {
		r.sink.Deliver(id, c)
	}
}

// cancelAll stops every pending timer on one target.
func cancelAll(rt *routeTarget) {
	for k, stop := range rt.pending {
		if stop != nil {
			stop()
		}
		delete(rt.pending, k)
	}
}

// Close cancels every pending delivery and refuses later cues. The engine calls
// it when the captioner comes down, so a stopped run leaves no timer behind.
func (r *CueRouter) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for id, rt := range r.targets {
		cancelAll(rt)
		delete(r.targets, id)
	}
}

// Pending reports how many deliveries are still waiting on a timer. Tests use it
// to prove a cancel left nothing behind.
func (r *CueRouter) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rt := range r.targets {
		n += len(rt.pending)
	}
	return n
}

// Fanout is the emit boundary for live captions. The policy is applied exactly
// once, here, so the WebSocket, the sidecar file and every destination show the
// same words. The sidecar and the bus are undelayed; only the router holds cues
// back for a destination's delay.
//
// PolicyNow, when set, is asked for the policy on every cue and wins over
// Policy. It is how a policy saved while the captioner runs reaches the next
// caption without a restart of speech recognition.
type Fanout struct {
	Policy    TextPolicy
	PolicyNow func() TextPolicy
	Sidecar   func(transcribe.LiveCaption) error
	Publish   func(transcribe.LiveCaption)
	Route     *CueRouter
}

// Emit applies the policy to one cue and sends it on every path.
func (f *Fanout) Emit(c transcribe.LiveCaption) {
	pol := f.Policy
	if f.PolicyNow != nil {
		pol = f.PolicyNow()
	}
	c.Text = pol.Apply(c.Text)
	if f.Sidecar != nil {
		_ = f.Sidecar(c)
	}
	if f.Publish != nil {
		f.Publish(c)
	}
	if f.Route != nil {
		f.Route.Emit(c)
	}
}
