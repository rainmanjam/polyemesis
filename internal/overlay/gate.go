// Package overlay is the on-screen chat overlay: the gate that decides when a
// chat message may be composited into a broadcast, the projection that strips
// it to what an overlay shows, and the style an overlay is drawn with.
//
// WHY A GATE. The console shows a chat message as soon as it arrives and
// retracts it if automod objects (internal/chat, deliver). That is right for an
// operator and wrong for an OBS scene: whatever is on the overlay is on air, so
// "removed 300 ms later" means "broadcast". The gate therefore holds each
// message until automod has ruled on it:
//
//   - automod off, or the fast checkers pass it and no model is configured:
//     released at once, on the adapter's goroutine. No delay.
//   - the fast checkers will remove it: never released.
//   - the model checker is on: held until the model answers, for at most
//     MaxHold. Past that it is DROPPED, not shown. A verdict that never comes
//     (the automod queue was full, the model hung, the settings were re-saved
//     mid-question) is not evidence the message is fine.
//
// A line already on screen still leaves it when an operator hides, deletes or
// bans: chat.undisplay, published from the same retraction the console gets.
package overlay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// MaxHold bounds how long a message waits for the model checker's verdict.
//
// Four seconds: long enough for a hosted model's usual one-to-three-second
// answer, short enough that a line still reads as live chat on stream. A
// message not judged by then is dropped from overlays (the console still has
// it). Only installs with the model checker on ever wait at all.
const MaxHold = 4 * time.Second

// sweepEvery is how often expired holds are dropped. It only bounds memory and
// the Expired counter: a late verdict is refused by Judge itself.
const sweepEvery = 250 * time.Millisecond

// shownCap bounds how many on-screen lines the gate remembers for retraction.
// Far above any overlay's line count; a retraction for a line older than this
// is for a line that has long since scrolled or faded off every overlay.
const shownCap = 4096

// Publisher is where display events go: the events hub.
type Publisher interface {
	Publish(eventstream.Envelope)
}

// Option tunes a Gate.
type Option func(*Gate)

// WithClock replaces time.Now.
func WithClock(now func() time.Time) Option { return func(g *Gate) { g.now = now } }

// Stats counts what the gate did, for the overlays settings card.
type Stats struct {
	Shown   int64 `json:"shown"`
	Blocked int64 `json:"blocked"`
	Expired int64 `json:"expired"`
}

type held struct {
	line     eventstream.DisplayLine
	deadline time.Time
}

// Gate implements chat.DisplaySink.
type Gate struct {
	pub  Publisher
	now  func() time.Time
	salt []byte

	// mu is held across Publish so a line's chat.display is always on the
	// wire before its chat.undisplay. The events hub never calls out while
	// holding its own lock, so this cannot deadlock.
	mu        sync.Mutex
	pending   map[string]held
	shown     map[string]string
	shownRing []string
	shownAt   int
	stats     Stats
}

var _ chat.DisplaySink = (*Gate)(nil)

// New builds a gate publishing into pub.
func New(pub Publisher, opts ...Option) *Gate {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	g := &Gate{
		pub:       pub,
		now:       time.Now,
		salt:      salt,
		pending:   map[string]held{},
		shown:     map[string]string{},
		shownRing: make([]string, shownCap),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

func key(p db.Platform, account, id string) string {
	return string(p) + "\x00" + account + "\x00" + id
}

// displayID is the opaque id a line carries on the wire. Salted per process, so
// it says nothing about the platform's message id or the operator's account.
func (g *Gate) displayID(p db.Platform, account, id string) string {
	h := sha256.New()
	h.Write(g.salt)
	h.Write([]byte(key(p, account, id)))
	return hex.EncodeToString(h.Sum(nil)[:10])
}

// project is the allowlist: every field an overlay receives is named here.
func (g *Gate) project(m chat.Message) eventstream.DisplayLine {
	line := eventstream.DisplayLine{
		ID:          g.displayID(m.Platform, m.Account, m.ID),
		Platform:    string(m.Platform),
		Name:        m.Author.Name,
		Color:       m.Author.Color,
		Moderator:   m.Author.Moderator,
		Subscriber:  m.Author.Subscriber,
		Broadcaster: m.Author.Broadcaster,
		Text:        m.Text,
		Action:      m.Action,
		At:          m.At,
	}
	for _, b := range m.Author.Badges {
		line.Badges = append(line.Badges, eventstream.DisplayBadge{ID: b.ID, Version: b.Version, Label: b.Label})
	}
	return line
}

// Offer implements chat.DisplaySink.
func (g *Gate) Offer(m chat.Message, d chat.Display) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch d {
	case chat.DisplayClear:
		g.releaseLocked(key(m.Platform, m.Account, m.ID), g.project(m))
	case chat.DisplayPending:
		g.pending[key(m.Platform, m.Account, m.ID)] = held{line: g.project(m), deadline: g.now().Add(MaxHold)}
	default:
		// Blocked, and anything this build does not know: fail closed.
		g.stats.Blocked++
	}
}

// Judge implements chat.DisplaySink.
func (g *Gate) Judge(p db.Platform, account, id string, d chat.Display) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := key(p, account, id)
	h, ok := g.pending[k]
	if !ok {
		// Already dropped (expired or retracted), or never held.
		return
	}
	delete(g.pending, k)
	if d != chat.DisplayClear {
		g.stats.Blocked++
		return
	}
	if g.now().After(h.deadline) {
		g.stats.Expired++
		return
	}
	g.releaseLocked(k, h.line)
}

// Retract implements chat.DisplaySink.
func (g *Gate) Retract(p db.Platform, account string, ids []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var gone []string
	for _, id := range ids {
		k := key(p, account, id)
		if _, ok := g.pending[k]; ok {
			delete(g.pending, k)
			g.stats.Blocked++
			continue
		}
		if did, ok := g.shown[k]; ok {
			delete(g.shown, k)
			gone = append(gone, did)
		}
	}
	if len(gone) > 0 {
		g.pub.Publish(eventstream.Envelope{
			Type: eventstream.TypeChatUndisplay,
			Data: eventstream.DisplayRemoval{Platform: string(p), IDs: gone},
		})
	}
}

func (g *Gate) releaseLocked(k string, line eventstream.DisplayLine) {
	if old := g.shownRing[g.shownAt]; old != "" {
		delete(g.shown, old)
	}
	g.shownRing[g.shownAt] = k
	g.shownAt = (g.shownAt + 1) % len(g.shownRing)
	g.shown[k] = line.ID
	g.stats.Shown++
	g.pub.Publish(eventstream.Envelope{Type: eventstream.TypeChatDisplay, At: line.At, Data: line})
}

// Sweep drops every hold past its deadline.
func (g *Gate) Sweep() {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for k, h := range g.pending {
		if now.After(h.deadline) {
			delete(g.pending, k)
			g.stats.Expired++
		}
	}
}

// Stats returns the counters.
func (g *Gate) Stats() Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// Run sweeps until ctx ends.
func (g *Gate) Run(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Sweep()
		}
	}
}
