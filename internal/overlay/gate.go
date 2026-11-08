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

// heldCap and heldTTL bound the lines kept for release: the last 200, for up to
// ten minutes. A false positive worth rescuing is rescued while the moment is
// still live; a line older than this has scrolled off any overlay anyway.
const (
	heldCap = 200
	heldTTL = 10 * time.Minute
)

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
	// Held counts the lines kept off overlays by a finding's hold (also in
	// Blocked), and Released how many of those an operator then put back.
	Held     int64 `json:"held"`
	Released int64 `json:"released"`
}

// heldLine is a withheld line kept for Release.
type heldLine struct {
	line eventstream.DisplayLine
	at   time.Time
	// platform, account and author say whose line it is, so a ban or a cleared
	// room can take it out of the buffer when the hub no longer has its id.
	platform db.Platform
	account  string
	author   string
}

type held struct {
	line     eventstream.DisplayLine
	msg      chat.Message
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

	// heldLines are lines a finding's hold kept off the overlay, newest last,
	// bounded by heldCap and heldTTL. See Release.
	heldLines map[string]heldLine
	heldOrder []string
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
		heldLines: map[string]heldLine{},
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
		line := g.project(m)
		g.releaseLocked(key(m.Platform, m.Account, m.ID), line, line.At)
	case chat.DisplayPending:
		g.pending[key(m.Platform, m.Account, m.ID)] = held{line: g.project(m), msg: m, deadline: g.now().Add(MaxHold)}
	case chat.DisplayHeld:
		g.stats.Blocked++
		g.holdLocked(key(m.Platform, m.Account, m.ID), g.project(m), m)
	default:
		// Blocked, and anything this build does not know: fail closed.
		g.stats.Blocked++
	}
}

// holdLocked keeps a withheld line for Release, shedding the oldest past the
// cap. A key offered twice is kept once, at its latest position.
func (g *Gate) holdLocked(k string, line eventstream.DisplayLine, m chat.Message) {
	g.stats.Held++
	g.dropHeldLocked(k)
	g.heldLines[k] = heldLine{line: line, at: g.now(), platform: m.Platform, account: m.Account, author: m.Author.ID}
	g.heldOrder = append(g.heldOrder, k)
	for len(g.heldOrder) > heldCap {
		delete(g.heldLines, g.heldOrder[0])
		g.heldOrder = g.heldOrder[1:]
	}
}

// dropHeldLocked forgets a held line, if there is one.
func (g *Gate) dropHeldLocked(k string) {
	if _, ok := g.heldLines[k]; !ok {
		return
	}
	delete(g.heldLines, k)
	for i, o := range g.heldOrder {
		if o == k {
			g.heldOrder = append(g.heldOrder[:i], g.heldOrder[i+1:]...)
			break
		}
	}
}

// platformOf is the platform half of a key.
func platformOf(k string) string {
	for i := 0; i < len(k); i++ {
		if k[i] == 0 {
			return k[:i]
		}
	}
	return k
}

// RetractAuthor implements chat.AuthorRetractor: a person was banned or timed
// out (or, with an empty authorID, the room was cleared), so none of their held
// lines on that channel may be shown afterwards. Retract takes ids the hub's
// 500-message ring still holds; a held line older than that has none.
func (g *Gate) RetractAuthor(p db.Platform, account, authorID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, h := range g.heldLines {
		if h.platform != p || h.account != account {
			continue
		}
		if authorID != "" && h.author != authorID {
			continue
		}
		g.dropHeldLocked(k)
	}
}

// IsHeld reports whether a line is in the release buffer right now: held, not
// yet released, retracted or removed, and inside heldTTL. The review list asks
// it, so its "Show on overlay" is true to the gate and not to a flag set once.
func (g *Gate) IsHeld(p db.Platform, account, id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	h, ok := g.heldLines[key(p, account, id)]
	return ok && g.now().Sub(h.at) <= heldTTL
}

// Release puts a held line on the overlay: the "Show on overlay" for a
// misfire. It reports whether there was one to release -- false for a line that
// was never held, was removed rather than held, has aged past heldTTL, or was
// already released.
//
// The line is published as a new chat.display, so it joins what a reconnecting
// overlay is replayed like any other line, and a later retraction (the operator
// then deletes it) takes it off again.
func (g *Gate) Release(p db.Platform, account, id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := key(p, account, id)
	h, ok := g.heldLines[k]
	if !ok {
		return false
	}
	g.dropHeldLocked(k)
	if g.now().Sub(h.at) > heldTTL {
		return false
	}
	g.stats.Released++
	// The ENVELOPE is stamped now: the event hub's replay window is measured
	// from it, and the line's own time may be minutes old. The line keeps its
	// own time -- when it was said.
	g.releaseLocked(k, h.line, g.now())
	return true
}

// Judge implements chat.DisplaySink.
func (g *Gate) Judge(p db.Platform, account, id string, d chat.Display) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := key(p, account, id)
	h, ok := g.pending[k]
	if !ok {
		// Already dropped (expired or retracted), or never pending. A line the
		// fast pass held that the model then REMOVES is no longer a maybe: take
		// it out of the release buffer -- and off the screen if it was
		// released before the model answered.
		if d == chat.DisplayBlocked {
			g.dropHeldLocked(k)
			if did, shown := g.shown[k]; shown {
				delete(g.shown, k)
				g.pub.Publish(eventstream.Envelope{
					Type: eventstream.TypeChatUndisplay,
					Data: eventstream.DisplayRemoval{Platform: platformOf(k), IDs: []string{did}},
				})
			}
		}
		return
	}
	delete(g.pending, k)
	if d != chat.DisplayClear {
		g.stats.Blocked++
		if d == chat.DisplayHeld {
			g.holdLocked(k, h.line, h.msg)
		}
		return
	}
	if g.now().After(h.deadline) {
		g.stats.Expired++
		return
	}
	g.releaseLocked(k, h.line, h.line.At)
}

// Retract implements chat.DisplaySink.
func (g *Gate) Retract(p db.Platform, account string, ids []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var gone []string
	for _, id := range ids {
		k := key(p, account, id)
		// A line deleted, hidden or banned after being held must not be
		// releasable: the operator's removal outranks their earlier "maybe".
		g.dropHeldLocked(k)
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

func (g *Gate) releaseLocked(k string, line eventstream.DisplayLine, at time.Time) {
	if old := g.shownRing[g.shownAt]; old != "" {
		delete(g.shown, old)
	}
	g.shownRing[g.shownAt] = k
	g.shownAt = (g.shownAt + 1) % len(g.shownRing)
	g.shown[k] = line.ID
	g.stats.Shown++
	g.pub.Publish(eventstream.Envelope{Type: eventstream.TypeChatDisplay, At: at, Data: line})
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
	// heldOrder is oldest first, so the expired are a prefix.
	for len(g.heldOrder) > 0 && now.Sub(g.heldLines[g.heldOrder[0]].at) > heldTTL {
		delete(g.heldLines, g.heldOrder[0])
		g.heldOrder = g.heldOrder[1:]
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
