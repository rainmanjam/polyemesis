package automod

import (
	"container/list"
	"hash/fnv"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// The hard-case gate in front of the model (spec section 6).
//
// ModelScope "all" asks the model about every message the fast pass did not
// remove, which is what every install did before the pack existed. ModelScope
// "hard" asks only about the messages the free checkers could not settle AND
// that show a hard-case signal (pack/hardcase.go), within three limits that
// keep a raid from becoming a bill: one call per author per 20 s, one call per
// distinct text per 10 min, and a brake that stops asking for a minute when the
// gate is passing messages faster than a room can be saying hard things.
//
// Everything here only ever REDUCES the questions asked. A message the gate
// declines keeps its fast verdict, so a contextual hate term the model was not
// asked about stays a flag with no hold (D4), and a line with no fast finding
// goes to the overlay at once instead of waiting for a verdict that is not
// coming.

// ModelScope is which messages reach the model.
type ModelScope string

const (
	// ScopeAll asks about every message the fast pass did not remove. The
	// behaviour of every install that predates the pack (spec D11).
	ScopeAll ModelScope = "all"
	// ScopeHard asks only about hard cases.
	ScopeHard ModelScope = "hard"
)

// ParseModelScope reads a stored scope. Empty or unknown is ScopeAll: the
// choice that never stops checking a message an install used to check.
func ParseModelScope(s string) ModelScope {
	if ModelScope(s) == ScopeHard {
		return ScopeHard
	}
	return ScopeAll
}

// GateConfig bounds the gate. The zero value is the spec's numbers.
type GateConfig struct {
	// AuthorEvery is the least time between two calls about one author.
	AuthorEvery time.Duration
	// MaxAuthors bounds the per-author table.
	MaxAuthors int
	// CacheSize and CacheTTL bound the verdict cache.
	CacheSize int
	CacheTTL  time.Duration
	// RaidMax messages passing the gate inside RaidWindow trips the brake for
	// BrakeFor.
	RaidMax    int
	RaidWindow time.Duration
	BrakeFor   time.Duration
}

// DefaultGateConfig is spec section 6.3.
func DefaultGateConfig() GateConfig {
	return GateConfig{
		AuthorEvery: 20 * time.Second,
		MaxAuthors:  20000,
		CacheSize:   2048,
		CacheTTL:    10 * time.Minute,
		RaidMax:     30,
		RaidWindow:  10 * time.Second,
		BrakeFor:    60 * time.Second,
	}
}

func (c GateConfig) withDefaults() GateConfig {
	d := DefaultGateConfig()
	if c.AuthorEvery <= 0 {
		c.AuthorEvery = d.AuthorEvery
	}
	if c.MaxAuthors <= 0 {
		c.MaxAuthors = d.MaxAuthors
	}
	if c.CacheSize <= 0 {
		c.CacheSize = d.CacheSize
	}
	if c.CacheTTL <= 0 {
		c.CacheTTL = d.CacheTTL
	}
	if c.RaidMax <= 0 {
		c.RaidMax = d.RaidMax
	}
	if c.RaidWindow <= 0 {
		c.RaidWindow = d.RaidWindow
	}
	if c.BrakeFor <= 0 {
		c.BrakeFor = d.BrakeFor
	}
	return c
}

// ModelGate is the gate's long-lived state: the per-author table, the verdict
// cache and the raid brake. Created once at startup and passed in, like Budget
// and Strikes, because ApplyAutomod rebuilds the engine on every settings save
// and a save in the middle of a raid must not hand the raid a fresh allowance.
type ModelGate struct {
	cfg GateConfig
	now func() time.Time

	mu         sync.Mutex
	authors    map[string]time.Time
	cache      map[uint64]*list.Element
	order      *list.List // front = most recently used
	passes     []time.Time
	brakeUntil time.Time
	stats      GateStats
}

// GateStats is the gate's counters, for the card. Counts only: nothing here
// names a message or a person.
type GateStats struct {
	// Scope is the engine's current scope. Filled by the engine.
	Scope ModelScope `json:"scope,omitempty"`
	// Asked is how many messages the gate let through to the model.
	Asked int `json:"asked"`
	// Settled counts messages the fast pass had already decided, and NoSignal
	// those with nothing to ask about.
	Settled  int `json:"settled"`
	NoSignal int `json:"noSignal"`
	// Capped counts messages declined by the per-author cap, Cached those
	// answered from the verdict cache, and Braked those declined while the raid
	// brake was on.
	Capped int `json:"capped"`
	Cached int `json:"cached"`
	Braked int `json:"braked"`
	// BrakeUntil is when the raid brake lifts; zero while it is off.
	BrakeUntil time.Time `json:"brakeUntil,omitzero"`
}

type gateEntry struct {
	key      uint64
	at       time.Time
	findings []Finding
	by       Slot
}

// NewModelGate returns a gate with the given bounds (zero fields take the
// spec's numbers).
func NewModelGate(cfg GateConfig) *ModelGate {
	cfg = cfg.withDefaults()
	return &ModelGate{
		cfg:     cfg,
		now:     time.Now,
		authors: map[string]time.Time{},
		cache:   map[uint64]*list.Element{},
		order:   list.New(),
	}
}

// Stats reports the counters and whether the brake is on.
func (g *ModelGate) Stats() GateStats {
	if g == nil {
		return GateStats{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.stats
	if g.now().Before(g.brakeUntil) {
		s.BrakeUntil = g.brakeUntil
	}
	return s
}

// formKey hashes the pack form, so "Free  GIVEAWAY!!" and "free giveaway" are
// one question. 64 bits of FNV: a collision would serve one text another's
// cached verdict, and over 2,048 entries that is a ~1e-13 event.
func formKey(packForm string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(packForm))
	return h.Sum64()
}

// gateText is the text a question is keyed by: the pack form, or the NFKC form
// for a line of nothing but symbols, whose pack form is empty and would
// otherwise make every such line one question.
func gateText(f pack.Forms) string {
	if f.Pack != "" {
		return f.Pack
	}
	return f.NFKC
}

func (g *ModelGate) countSettled()  { g.bump(func(s *GateStats) { s.Settled++ }) }
func (g *ModelGate) countNoSignal() { g.bump(func(s *GateStats) { s.NoSignal++ }) }

func (g *ModelGate) bump(f func(*GateStats)) {
	g.mu.Lock()
	f(&g.stats)
	g.mu.Unlock()
}

// admit decides whether a message that shows a hard-case signal may be asked
// about. author is the platform+id key ("" for a message with no author id,
// which has no cap to apply).
//
// Order: the brake first (a raid is the one thing never worth a call), then the
// author cap, then the pass count. The verdict cache is NOT consulted here: it
// saves the CALL (Engine.CheckModel), and the cap applies to a cached text too,
// so one viewer pasting the same line over and over cannot count as a raid.
func (g *ModelGate) admit(author string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if now.Before(g.brakeUntil) {
		g.stats.Braked++
		return false
	}
	if author != "" {
		if last, ok := g.authors[author]; ok && now.Sub(last) < g.cfg.AuthorEvery {
			g.stats.Capped++
			return false
		}
		if len(g.authors) >= g.cfg.MaxAuthors {
			g.sweepAuthorsLocked(now)
			if len(g.authors) >= g.cfg.MaxAuthors {
				// Full of authors who each spoke inside the last 20 s: a
				// flood. Decline rather than grow, which is the cheap side
				// of the error.
				g.stats.Capped++
				return false
			}
		}
	}
	if !g.passLocked(now) {
		return false
	}
	if author != "" {
		g.authors[author] = now
	}
	g.stats.Asked++
	return true
}

// passLocked records one message the gate is letting through and reports
// whether it may go. Counted only HERE, after the per-author cap: a message the
// cap declined is not a pass, or one viewer pasting threat-shaped lines could
// trip the brake and switch the model off for the whole room. A copypasta raid
// IS counted, one pass per distinct author, since it is the pressure on the
// room, not the spend, that the brake answers.
func (g *ModelGate) passLocked(now time.Time) bool {
	cut := now.Add(-g.cfg.RaidWindow)
	n := 0
	for _, t := range g.passes {
		if t.After(cut) {
			g.passes[n] = t
			n++
		}
	}
	g.passes = append(g.passes[:n], now)
	if len(g.passes) > g.cfg.RaidMax {
		g.brakeUntil = now.Add(g.cfg.BrakeFor)
		g.passes = g.passes[:0]
		g.stats.Braked++
		return false
	}
	return true
}

func (g *ModelGate) sweepAuthorsLocked(now time.Time) {
	for k, t := range g.authors {
		if now.Sub(t) >= g.cfg.AuthorEvery {
			delete(g.authors, k)
		}
	}
}

// lookup returns a fresh cached answer for key.
func (g *ModelGate) lookup(key uint64) ([]Finding, Slot, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	el, ok := g.cache[key]
	if !ok {
		return nil, "", false
	}
	e := el.Value.(*gateEntry)
	if g.now().Sub(e.at) >= g.cfg.CacheTTL {
		g.order.Remove(el)
		delete(g.cache, key)
		return nil, "", false
	}
	g.order.MoveToFront(el)
	g.stats.Cached++
	return append([]Finding(nil), e.findings...), e.by, true
}

// store records an answer. Only a real answer is stored: an error never is, so
// an outage is not remembered for ten minutes.
func (g *ModelGate) store(key uint64, findings []Finding, by Slot) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := &gateEntry{key: key, at: g.now(), findings: append([]Finding(nil), findings...), by: by}
	if el, ok := g.cache[key]; ok {
		el.Value = e
		g.order.MoveToFront(el)
		return
	}
	g.cache[key] = g.order.PushFront(e)
	for g.order.Len() > g.cfg.CacheSize {
		back := g.order.Back()
		g.order.Remove(back)
		delete(g.cache, back.Value.(*gateEntry).key)
	}
}

// WithModelGate sets which messages reach the model, with the gate state the
// caller keeps across saves. A nil gate, or ScopeAll, leaves the engine asking
// about every message as before. Call it before the engine is shared.
func (e *Engine) WithModelGate(scope ModelScope, g *ModelGate) *Engine {
	if e == nil {
		return e
	}
	e.scope, e.gate = scope, g
	if scope == ScopeHard {
		// A lexicon that did not load leaves the scope at all: asking about
		// too much is the safe side of the error.
		hc, err := pack.DefaultHardCase()
		if err != nil || g == nil {
			e.scope, e.gate = ScopeAll, nil
			return e
		}
		e.hardcase = hc
	}
	return e
}

// WithHoldOnModelError makes a message the model was asked about and could not
// answer wait off the overlay instead of passing (a setting, off by default).
func (e *Engine) WithHoldOnModelError(on bool) *Engine {
	if e != nil {
		e.holdOnModelError = on
	}
	return e
}

// HoldOnModelError reports the setting.
func (e *Engine) HoldOnModelError() bool { return e != nil && e.holdOnModelError }

// Scope is the engine's model scope.
func (e *Engine) Scope() ModelScope {
	if e == nil || e.scope == "" {
		return ScopeAll
	}
	return e.scope
}

// settledByPack reports whether the fast pass already decided the message: a
// severe hate term, a doxxing hit or a scam hit. Those hold on their own and a
// paid opinion would not change what happens to them.
func settledByPack(fs []Finding) bool {
	for _, f := range fs {
		if f.Checker != CheckerProtection {
			continue
		}
		if f.Category == CategoryDoxxing || f.Category == CategoryScam || f.Detector == pack.GroupHateSevere {
			return true
		}
	}
	return false
}

// hardCaseInput derives what the signals need from the fast verdict.
func hardCaseInput(s Subject, fs []Finding) pack.SignalInput {
	in := pack.SignalInput{Raw: s.Text, Reply: s.ReplyToID != ""}
	for _, f := range fs {
		if f.Checker != CheckerProtection {
			continue
		}
		switch f.Detector {
		case pack.GroupHateContextual:
			in.ContextualHate, in.TermHit = true, true
		case pack.GroupHateSevere, pack.GroupProfanityMild, pack.GroupProfanityStrong:
			in.TermHit = true
		}
	}
	return in
}

// WantsModel reports whether the model should be asked about a message whose
// fast verdict was v. Under ScopeAll it is always true (the caller has already
// checked the model is on and the verdict did not remove the message). Under
// ScopeHard it is true only for an unsettled message with a hard-case signal
// that the gate's limits admit.
func (e *Engine) WantsModel(p db.Platform, s Subject, v Verdict) bool {
	if e == nil {
		return false
	}
	if e.scope != ScopeHard || e.gate == nil || e.hardcase == nil {
		return true
	}
	if settledByPack(v.Findings) {
		e.gate.countSettled()
		return false
	}
	f := pack.NewForms(s.Text)
	if len(e.hardcase.Signals(f, hardCaseInput(s, v.Findings))) == 0 {
		e.gate.countNoSignal()
		return false
	}
	author := ""
	if s.AuthorID != "" {
		author = authorKey(p, s.AuthorID)
	}
	return e.gate.admit(author)
}

// gateStats merges the gate's counters with the scope.
func (e *Engine) gateStats() *GateStats {
	if e == nil || e.scope != ScopeHard || e.gate == nil {
		return nil
	}
	s := e.gate.Stats()
	s.Scope = e.scope
	return &s
}
