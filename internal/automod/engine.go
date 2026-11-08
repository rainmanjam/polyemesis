package automod

import (
	"context"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Engine runs the checkers and applies the matrix.
//
// It decides; it never acts. internal/chat's Hub performs the action, and
// keeping the two apart is what makes this testable without a network and keeps
// every action flowing through one audited path.
type Engine struct {
	rules   *RuleSet
	history *History
	model   *Chain
	matrix  Matrix
	caps    Capabilities
	// protection is the recommended-protection pack, nil unless an install has
	// it on. Set by WithProtection after New, so New's signature (and every
	// caller of it) is unchanged.
	protection *Protection
	// ladder is the strike policy, nil unless the pack is on. See strikes.go.
	ladder *Ladder
	// scope, gate and hardcase decide which messages reach the model (gate.go).
	// The zero value asks about every message, as before the pack.
	scope ModelScope
	gate  *ModelGate
	// epoch is the gate's verdict-cache epoch this engine was built in.
	epoch    uint64
	hardcase *pack.HardCase
	// holdOnModelError holds a message off the overlay when the model was asked
	// and could not answer. Off by default.
	holdOnModelError bool
	now              func() time.Time
}

// New returns an engine. Any checker may be nil, and a nil one simply
// contributes nothing -- an operator with no rules configured and the model off
// still gets the history detectors, which is the useful default.
//
// model is a *Chain when two checkers are configured. A single connector (a
// *Model or a *Jev) is accepted too, and is the primary of a chain with no
// secondary -- which is exactly what the engine was before there were two.
func New(matrix Matrix, caps Capabilities, rules *RuleSet, history *History, model ModelChecker) *Engine {
	if caps == nil {
		caps = PlatformCaps{}
	}
	var chain *Chain
	switch m := model.(type) {
	case nil:
	case *Chain:
		chain = m
	default:
		chain = NewChain(m, nil)
	}
	return &Engine{rules: rules, history: history, model: chain, matrix: matrix, caps: caps, now: time.Now}
}

// WithProtection attaches the recommended-protection pack and returns the
// engine. A nil p leaves the engine as it was. Call it before the engine is
// shared: it is not synchronised with CheckFast.
func (e *Engine) WithProtection(p *Protection) *Engine {
	if e != nil {
		e.protection = p
	}
	return e
}

// clock is the engine's time source. An Engine built as a struct literal rather
// than through New has no now, and must not panic on the hot path for it.
func (e *Engine) clock() time.Time {
	if e.now == nil {
		return time.Now()
	}
	return e.now()
}

// Protection returns the attached pack, or nil.
func (e *Engine) Protection() *Protection {
	if e == nil {
		return nil
	}
	return e.protection
}

// LearnProtectedName tells the pack about a broadcaster or moderator name, so a
// look-alike account is caught. Called by internal/chat for messages that are
// otherwise exempt from automod. A no-op without the pack.
func (e *Engine) LearnProtectedName(name string) {
	if e != nil {
		e.protection.LearnProtectedName(name)
	}
}

// Subject is everything the fast checkers are told about one message.
//
// A struct rather than a growing argument list: the detectors planned for it
// (emote floods, impersonation, replies) each need one more fact about the
// message, and every added positional argument would be an edit to the
// Moderator interface and to every fake of it. Only AuthorID and Text are read
// today; the rest are carried so those detectors arrive without another
// signature change.
type Subject struct {
	AuthorID   string
	AuthorName string
	Text       string
	// EmoteCount is how many emotes the platform marked in Text.
	EmoteCount int
	// ReplyToID is the id of the message this one replies to, if any.
	ReplyToID string
}

// CheckFast runs only the free, deterministic checkers.
//
// This is the hot path, called for every message. It never blocks on a network
// and never allocates a request: the model is asked separately, off the path
// that decides whether a message is displayed.
func (e *Engine) CheckFast(p db.Platform, s Subject) Verdict {
	if e == nil {
		return Verdict{}
	}
	var findings []Finding
	findings = append(findings, e.rules.Check(s.Text)...)
	hist := e.protection.markHistory(e.history.Observe(p, s.AuthorID, s.Text))
	findings = append(findings, hist...)
	pf := e.protection.check(p, s, e.clock(), hist)
	findings = append(findings, pf...)
	// One strike per message, not per finding, and only for a message the pack
	// itself flagged. Explain, CheckText and CheckModel never reach this: they
	// record nothing, and the model has its own category table (D3).
	if e.ladder != nil && len(pf) > 0 && s.AuthorID != "" {
		strike := e.ladder.Strikes.Record(p, s.AuthorID, e.clock(), e.ladder.window())
		findings = e.climb(p, findings, strike)
	}
	return e.decide(p, sortByConsequence(findings))
}

// CheckText is CheckFast for text that has no author: the rules, plus the
// history's per-message shape detectors, with nothing recorded. See
// History.CheckText.
func (e *Engine) CheckText(p db.Platform, text string) Verdict {
	if e == nil {
		return Verdict{}
	}
	var findings []Finding
	findings = append(findings, e.rules.Check(text)...)
	findings = append(findings, e.protection.markHistory(e.history.CheckText(text))...)
	findings = append(findings, e.protection.checkText(p, text)...)
	return e.decide(p, sortByConsequence(findings))
}

// CheckModel asks the model, for a caller that has decided this message is
// worth the call.
//
// Separate from CheckFast rather than a flag on it, because the two have
// completely different costs and failure modes and a caller must be made to
// choose. An error is the operator's business, not a reason to act: the verdict
// returned alongside it is empty, and the message passes.
func (e *Engine) CheckModel(ctx context.Context, p db.Platform, text string) (Verdict, error) {
	if e == nil {
		return Verdict{}, nil
	}
	// Under the hard-case scope an answer is remembered for ten minutes by the
	// pack form of the text, so a copypasta raid costs one call. Only there:
	// scope "all" asks every time, as it always has.
	var key uint64
	cached := e.scope == ScopeHard && e.gate != nil
	if cached {
		key = formKey(gateText(pack.NewForms(text)))
		if f, by, ok := e.gate.lookup(key); ok {
			v := e.decide(p, f)
			v.DecidedBy = by
			return v, nil
		}
	}
	// The secondary is asked inside Decide, and only when the primary failed.
	findings, by, err := e.model.Decide(ctx, text)
	if err != nil {
		// Fail open. Both checkers failed (or the only one did), the verdict
		// is empty, so nothing is acted on, and the caller says so out loud.
		return Verdict{}, err
	}
	if cached && by != "" {
		e.gate.storeAt(e.epoch, key, findings, by)
	}
	v := e.decide(p, findings)
	v.DecidedBy = by
	return v, nil
}

// decide splits findings into what was found and what the matrix permits.
func (e *Engine) decide(p db.Platform, findings []Finding) Verdict {
	v := Verdict{Findings: findings}
	for _, f := range findings {
		k := Key{Platform: p, Action: f.Action, Checker: f.Checker}
		if e.matrix.Allows(e.caps, k) {
			v.Act = append(v.Act, f)
		}
		if f.Hold && e.holdAllowed(p) {
			v.Hold = true
		}
	}
	return v
}

// holdAllowed reports whether a finding's overlay hold may take effect on this
// platform: the global switch is on and the platform's is not off. The same two
// switches Matrix.Allows checks, but NOT the per-cell set or the capability
// table -- a hold performs nothing on any platform, so there is no cell to arm
// and nothing a platform could be unable to do.
func (e *Engine) holdAllowed(p db.Platform) bool {
	if !e.matrix.Enabled {
		return false
	}
	on, ok := e.matrix.PlatformEnabled[p]
	return !ok || on
}

// Matrix returns the current matrix, for the API to render.
func (e *Engine) Matrix() Matrix { return e.matrix }

func (e *Engine) SetMatrix(m Matrix) {
	e.matrix = m
	e.syncArming()
}

// Capabilities returns the gate, so the API can render unavailable cells with
// their reason rather than the UI guessing at them.
func (e *Engine) Capabilities() Capabilities { return e.caps }

// ModelStats reports spend and health.
func (e *Engine) ModelStats() ModelStats {
	if e == nil {
		return ModelStats{}
	}
	st := e.model.Stats()
	st.Gate = e.gateStats()
	st.CeilingSpent = st.Ceiling > 0 && st.CallsThisHour >= st.Ceiling
	return st
}

// ModelEnabled reports whether the paid checker is configured and on.
//
// Exposed so the Hub can decide whether a message is worth queueing for the
// model at all, rather than queueing every message and discovering inside the
// connector that there is nothing to ask.
func (e *Engine) ModelEnabled() bool {
	return e != nil && e.model.Enabled()
}
