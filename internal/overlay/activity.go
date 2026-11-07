package overlay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// ActivityGate decides what of a viewer activity may go on air, and projects it
// to what an alerts overlay draws (eventstream.ActivityDisplay).
//
// THE SAME RULE AS THE CHAT GATE, for the same reason: whatever is on the
// overlay is on air. An alert's name and message are written by a stranger --
// hate raids use slur usernames, a Super Chat carries whatever the donor
// typed -- so:
//
//   - the MESSAGE is offered to automod. Released and passed: shown. Removed by
//     automod, or no verdict within MaxHold: the alert STILL shows, without the
//     message. A tip is not withheld because of what its sender said.
//   - the NAME is checked against the blocked terms by the fast checkers only.
//     A name that fails is blanked, and the overlay says "Someone".
//   - with the operator's overlayMessages off the hub strips the message before
//     it gets here.
//
// Unlike the chat gate there is nothing to retract: an alert is a moment, not a
// line that stays until it scrolls off, so there is no undisplay and no shown
// ring. Fed directly by the activity hub (activityhub.Display), not through the
// bus, for the reason the chat gate is: the bus drops for a slow subscriber.
type ActivityGate struct {
	pub    Publisher
	screen Screener
	now    func() time.Time
	salt   []byte

	mu      sync.Mutex
	seq     uint64
	pending map[string]heldAlert
	stats   ActivityStats
}

// Screener is the slice of the chat hub the gate asks. *chat.Hub satisfies it.
type Screener interface {
	// ScreenText judges viewer-written text: an immediate verdict, or
	// DisplayPending and a later call to done (see chat.Hub.ScreenText).
	ScreenText(p db.Platform, text string, done func(chat.Display)) chat.Display
	// ScreenName judges a name against the blocked terms, immediately.
	ScreenName(p db.Platform, name string) chat.Display
}

// ActivityStats counts what the gate did, for the overlays settings card.
type ActivityStats struct {
	Shown int64 `json:"shown"`
	// Unworded is how many alerts were shown without the viewer's message
	// because automod removed it or gave no verdict in time.
	Unworded int64 `json:"unworded"`
	// Renamed is how many names were blanked for failing the blocked terms.
	Renamed int64 `json:"renamed"`
}

type heldAlert struct {
	display  eventstream.ActivityDisplay
	message  string
	deadline time.Time
}

// ActivityOption tunes an ActivityGate.
type ActivityOption func(*ActivityGate)

// WithActivityClock replaces time.Now.
func WithActivityClock(now func() time.Time) ActivityOption {
	return func(g *ActivityGate) { g.now = now }
}

// NewActivityGate builds a gate publishing into pub and asking screen. A nil
// screen means nothing is moderated, which is automod being off.
func NewActivityGate(pub Publisher, screen Screener, opts ...ActivityOption) *ActivityGate {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	g := &ActivityGate{pub: pub, screen: screen, now: time.Now, salt: salt, pending: map[string]heldAlert{}}
	for _, o := range opts {
		o(g)
	}
	return g
}

// displayID is the opaque id an alert carries on the wire: salted per process,
// and per OFFER, so a replay from the console is a new alert to the overlay's
// queue rather than a duplicate of one it already drew.
func (g *ActivityGate) displayID(ev activity.Event, seq uint64) string {
	h := sha256.New()
	h.Write(g.salt)
	h.Write([]byte(ev.Key()))
	h.Write([]byte(strconv.FormatUint(seq, 10)))
	return hex.EncodeToString(h.Sum(nil)[:10])
}

// Offer implements activityhub.Display.
func (g *ActivityGate) Offer(ev activity.Event) {
	if !ev.OverlayVisible() {
		return
	}
	p := db.Platform(ev.Platform)
	g.mu.Lock()
	g.seq++
	seq := g.seq
	g.mu.Unlock()

	d := g.project(ev, seq)
	if g.screen != nil {
		if d.Name != "" && g.screen.ScreenName(p, d.Name) != chat.DisplayClear {
			d.Name = ""
			g.bump(func(s *ActivityStats) { s.Renamed++ })
		}
		if d.Recipient != "" && g.screen.ScreenName(p, d.Recipient) != chat.DisplayClear {
			d.Recipient = ""
			g.bump(func(s *ActivityStats) { s.Renamed++ })
		}
	}
	if ev.Message == "" {
		g.publish(d, "", false)
		return
	}
	if g.screen == nil {
		g.publish(d, ev.Message, false)
		return
	}
	// Held BEFORE the question is asked: a model that answers on another
	// goroutine before Offer returns must find the hold waiting.
	id := d.ID
	g.mu.Lock()
	g.pending[id] = heldAlert{display: d, message: ev.Message, deadline: g.now().Add(MaxHold)}
	g.mu.Unlock()
	switch g.screen.ScreenText(p, ev.Message, func(v chat.Display) { g.judge(id, v) }) {
	case chat.DisplayClear:
		g.judge(id, chat.DisplayClear)
	case chat.DisplayPending:
		// judge or Sweep will resolve it.
	default:
		g.judge(id, chat.DisplayBlocked)
	}
}

// project is the allowlist: every field an overlay receives is named here, and
// nothing viewer-written is in it until the message is released.
func (g *ActivityGate) project(ev activity.Event, seq uint64) eventstream.ActivityDisplay {
	d := eventstream.ActivityDisplay{
		ID:       g.displayID(ev, seq),
		Platform: ev.Platform,
		Kind:     string(ev.Kind),
		Name:     ev.DisplayName(),
		Amount:   ev.Amount,
		Tier:     ev.Tier,
		Months:   ev.Months,
		Count:    ev.Count,
		Title:    ev.Title,
		Viewers:  ev.Viewers,
		Level:    ev.Level,
		Test:     ev.Test,
		At:       ev.At,
	}
	if ev.Amount != nil {
		a := *ev.Amount
		d.Amount = &a
	}
	if ev.Count == 1 && len(ev.Recipients) == 1 {
		d.Recipient = ev.Recipients[0].Name
	}
	return d
}

// judge resolves a held alert with automod's verdict on its message.
func (g *ActivityGate) judge(id string, v chat.Display) {
	g.mu.Lock()
	h, ok := g.pending[id]
	if !ok {
		// Already resolved (expired, or answered twice).
		g.mu.Unlock()
		return
	}
	delete(g.pending, id)
	expired := g.now().After(h.deadline)
	g.mu.Unlock()
	if v == chat.DisplayClear && !expired {
		g.publish(h.display, h.message, false)
		return
	}
	g.publish(h.display, "", true)
}

// publish sends the projection. withheld says the alert had words that are not
// going out with it, for the counter.
func (g *ActivityGate) publish(d eventstream.ActivityDisplay, message string, withheld bool) {
	d.Message = message
	g.mu.Lock()
	g.stats.Shown++
	if withheld {
		g.stats.Unworded++
	}
	g.mu.Unlock()
	// At on the envelope is left zero: the hub stamps the time it was published,
	// which is what the overlay replay cutoff reads. The event's own time is in
	// d.At.
	g.pub.Publish(eventstream.Envelope{Type: eventstream.TypeActivityDisplay, Data: d})
}

func (g *ActivityGate) bump(fn func(*ActivityStats)) {
	g.mu.Lock()
	fn(&g.stats)
	g.mu.Unlock()
}

// Sweep resolves every hold past its deadline: the alert goes out without its
// message. The model gave no verdict, and no verdict is not a pass.
func (g *ActivityGate) Sweep() {
	g.mu.Lock()
	now := g.now()
	var due []heldAlert
	for id, h := range g.pending {
		if now.After(h.deadline) {
			delete(g.pending, id)
			due = append(due, h)
		}
	}
	g.mu.Unlock()
	for _, h := range due {
		g.publish(h.display, "", true)
	}
}

// Stats returns the counters.
func (g *ActivityGate) Stats() ActivityStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// Run sweeps until ctx ends.
func (g *ActivityGate) Run(ctx context.Context) {
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
