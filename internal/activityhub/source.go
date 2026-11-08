package activityhub

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/backoff"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/redact"
)

// THE EXTENSION POINT FOR A PLATFORM THAT HAS ITS OWN TRANSPORT.
//
// A platform whose activity rides its chat connection (YouTube, Rumble, Kick,
// X, Facebook) needs none of this: its adapter calls emitActivity on the sink
// it was handed, and reports its kinds by implementing ActivityKinds (see
// internal/api/activity_wiring.go). A platform that needs a connection of its
// own -- Twitch EventSub is the first -- implements Source and is attached with
// Hub.Attach under the server's lifetime; the supervisor owns the goroutine, the
// restart backoff, the panic recovery and the health words.

// Source is one platform account's own activity transport.
type Source interface {
	Platform() string
	Account() string
	// Run connects and emits until ctx ends or the connection is lost, in
	// which case it returns an error and the supervisor reconnects after a
	// jittered backoff. It must return promptly when ctx is cancelled. emit
	// stamps Platform and Account when the event leaves them empty; it never
	// blocks.
	Run(ctx context.Context, emit func(activity.Event)) error
}

// Healther is the optional self-report, with the contract of chat.Healther: a
// plain sentence in the operator's words, never a token. A Source that does not
// implement it is live while Run is running.
type Healther interface {
	Health() chat.Health
}

// KindReporter is the optional per-kind report: which of the platform's kinds
// are on, unavailable to this account (not an Affiliate, not monetised), or
// need a reconnect to grant a scope. An unavailable kind is NOT a failure; the
// source stays live.
type KindReporter interface {
	Kinds() []activity.KindStatus
}

// SourceStatus is one source's condition, for GET /api/v1/activity/sources.
type SourceStatus struct {
	Platform string     `json:"platform"`
	Account  string     `json:"account,omitempty"`
	State    chat.State `json:"state"`
	// Detail is one sentence for a human. It never contains a token.
	Detail string `json:"detail,omitempty"`
	// Transport is "own" for an activityhub.Source and "chat" for a platform
	// whose activity rides its chat connection.
	Transport string                `json:"transport"`
	Kinds     []activity.KindStatus `json:"kinds"`
	Events    int64                 `json:"events"`
}

// Transports.
const (
	TransportOwn  = "own"
	TransportChat = "chat"
)

// Restart backoff, the same shape as the chat hub's.
const (
	DefaultBackoff    = time.Second
	DefaultMaxBackoff = 2 * time.Minute
	// stableAfter is how long a Run must have lasted for the next failure to
	// start the backoff over rather than continue it.
	stableAfter = time.Minute
	// detachWait bounds how long Detach waits for a Source that ignores its
	// context.
	detachWait = 5 * time.Second
)

func defaultBackoff(attempt int) time.Duration {
	d := backoff.Exponential(attempt, DefaultBackoff, DefaultMaxBackoff)
	// Jitter only ever subtracts, so the ceiling stays a ceiling.
	return d - time.Duration(rand.Int64N(int64(d/5)+1))
}

// sources is the supervisor's state, embedded in the Hub.
type sources struct {
	hub *Hub

	mu        sync.Mutex
	running   map[string]*supervised
	providers []func() []SourceStatus
	closed    bool
}

func (s *sources) init(h *Hub) {
	s.hub = h
	s.running = map[string]*supervised{}
}

func sourceKey(platform, account string) string { return platform + "\x00" + account }

type supervised struct {
	src    Source
	cancel context.CancelFunc
	done   chan struct{}
	events atomic.Int64

	mu       sync.Mutex
	state    chat.State
	detail   string
	restarts int
}

func (v *supervised) set(st chat.State, detail string) {
	v.mu.Lock()
	v.state, v.detail = st, detail
	v.mu.Unlock()
}

// ErrDuplicateSource is what Attach answers for a (platform, account) that
// already has a source.
var ErrDuplicateSource = errors.New("activityhub: that account already has an activity source")

// Attach starts supervising src under ctx. One source per (platform, account):
// a second is refused rather than silently replacing the first, because the
// usual cause is a double-start and the symptom would be every event twice.
func (h *Hub) Attach(ctx context.Context, src Source) error {
	if h == nil || src == nil {
		return errors.New("activityhub: nil source")
	}
	s := &h.sources
	key := sourceKey(src.Platform(), src.Account())
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("activityhub: hub is closed")
	}
	if _, dup := s.running[key]; dup {
		s.mu.Unlock()
		return ErrDuplicateSource
	}
	rctx, cancel := context.WithCancel(ctx)
	v := &supervised{src: src, cancel: cancel, done: make(chan struct{}), state: chat.StateConnecting}
	s.running[key] = v
	s.mu.Unlock()

	go s.loop(rctx, v)
	return nil
}

// Detach stops the source for (platform, account) and waits for it, bounded.
// Nothing is an error: detaching what is not attached is the normal answer to
// an account that never had activity.
func (h *Hub) Detach(platform, account string) {
	if h == nil {
		return
	}
	s := &h.sources
	key := sourceKey(platform, account)
	s.mu.Lock()
	v := s.running[key]
	delete(s.running, key)
	s.mu.Unlock()
	if v == nil {
		return
	}
	v.cancel()
	select {
	case <-v.done:
	case <-time.After(detachWait):
		h.log.Warn("activity source is slow to stop; it is detached and will finish in the background",
			"platform", platform, "account", account)
	}
}

// Close stops every source and refuses new ones.
func (h *Hub) Close() {
	if h == nil {
		return
	}
	s := &h.sources
	s.mu.Lock()
	s.closed = true
	all := make([]*supervised, 0, len(s.running))
	for k, v := range s.running {
		all = append(all, v)
		delete(s.running, k)
	}
	s.mu.Unlock()
	for _, v := range all {
		v.cancel()
	}
	for _, v := range all {
		select {
		case <-v.done:
		case <-time.After(detachWait):
		}
	}
}

// AddStatusProvider registers a function whose sources are listed beside the
// supervised ones: how a platform whose activity rides its chat connection
// reports its kinds without being a Source.
func (h *Hub) AddStatusProvider(fn func() []SourceStatus) {
	if h == nil || fn == nil {
		return
	}
	h.sources.mu.Lock()
	h.sources.providers = append(h.sources.providers, fn)
	h.sources.mu.Unlock()
}

// Statuses lists every source, ordered by platform then account.
func (h *Hub) Statuses() []SourceStatus {
	if h == nil {
		return []SourceStatus{}
	}
	s := &h.sources
	s.mu.Lock()
	vs := make([]*supervised, 0, len(s.running))
	for _, v := range s.running {
		vs = append(vs, v)
	}
	providers := append([]func() []SourceStatus(nil), s.providers...)
	s.mu.Unlock()

	out := make([]SourceStatus, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.status())
	}
	// Outside the lock: a provider reads the chat hub, which has its own.
	for _, p := range providers {
		out = append(out, p()...)
	}
	for i := range out {
		if out[i].Kinds == nil {
			out[i].Kinds = []activity.KindStatus{}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		return out[i].Account < out[j].Account
	})
	return out
}

func (v *supervised) status() SourceStatus {
	v.mu.Lock()
	st, detail := v.state, v.detail
	v.mu.Unlock()
	if hl, ok := v.src.(Healther); ok {
		if h := hl.Health(); h.State != "" {
			st, detail = h.State, h.Detail
		}
	}
	out := SourceStatus{
		Platform: v.src.Platform(), Account: v.src.Account(),
		State: st, Detail: redact.Redact(detail), Transport: TransportOwn,
		Events: v.events.Load(),
	}
	if kr, ok := v.src.(KindReporter); ok {
		out.Kinds = kr.Kinds()
	}
	return out
}

// loop runs the source until its context ends, restarting it with backoff.
func (s *sources) loop(ctx context.Context, v *supervised) {
	defer close(v.done)
	emit := func(ev activity.Event) {
		if ev.Platform == "" {
			ev.Platform = v.src.Platform()
		}
		if ev.Account == "" {
			ev.Account = v.src.Account()
		}
		v.events.Add(1)
		s.hub.Submit(ev)
	}
	attempt := 0
	for ctx.Err() == nil {
		v.set(chat.StateConnecting, "")
		started := s.hub.now()
		err := s.runOnce(ctx, v, emit)
		if ctx.Err() != nil {
			v.set(chat.StateStopped, "")
			return
		}
		if s.hub.now().Sub(started) >= stableAfter {
			attempt = 0
		}
		attempt++
		v.mu.Lock()
		v.restarts++
		v.mu.Unlock()
		wait := s.hub.backoff(attempt)
		v.set(chat.StateFailed, fmt.Sprintf("%s activity stopped (%s); trying again in %s",
			v.src.Platform(), reasonOf(err), wait.Round(time.Second)))
		s.hub.log.Warn("activity source stopped", "platform", v.src.Platform(), "account", v.src.Account(),
			"err", redact.Redact(fmt.Sprint(err)), "retry_in", wait)
		if !backoff.SleepCtx(ctx, wait) {
			v.set(chat.StateStopped, "")
			return
		}
	}
	v.set(chat.StateStopped, "")
}

// runOnce is one Run, with a panic turned into an error: a source that panics
// restarts and nothing else notices, as chat adapters do.
func (s *sources) runOnce(ctx context.Context, v *supervised, emit func(activity.Event)) (err error) {
	defer func() {
		if p := recover(); p != nil {
			s.hub.log.Error("activity source panicked",
				"platform", v.src.Platform(), "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("an internal error (%v)", p)
		}
	}()
	// Live while running, unless the source says otherwise through Healther.
	v.set(chat.StateLive, "")
	return v.src.Run(ctx, emit)
}

func reasonOf(err error) string {
	if err == nil {
		return "it ended"
	}
	return redact.Redact(err.Error())
}
