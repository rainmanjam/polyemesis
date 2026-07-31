package automod

import (
	"context"

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
	model   *Model
	matrix  Matrix
	caps    Capabilities
}

// New returns an engine. Any checker may be nil, and a nil one simply
// contributes nothing -- an operator with no rules configured and the model off
// still gets the history detectors, which is the useful default.
func New(matrix Matrix, caps Capabilities, rules *RuleSet, history *History, model *Model) *Engine {
	if caps == nil {
		caps = PlatformCaps{}
	}
	return &Engine{rules: rules, history: history, model: model, matrix: matrix, caps: caps}
}

// CheckFast runs only the free, deterministic checkers.
//
// This is the hot path, called for every message. It never blocks on a network
// and never allocates a request: the model is asked separately, off the path
// that decides whether a message is displayed.
func (e *Engine) CheckFast(p db.Platform, authorID, text string) Verdict {
	if e == nil {
		return Verdict{}
	}
	var findings []Finding
	findings = append(findings, e.rules.Check(text)...)
	findings = append(findings, e.history.Observe(p, authorID, text)...)
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
	findings, err := e.model.Check(ctx, text)
	if err != nil {
		// Fail open. The verdict is empty, so nothing is acted on, and the
		// caller flags the message for a human instead.
		return Verdict{}, err
	}
	return e.decide(p, findings), nil
}

// decide splits findings into what was found and what the matrix permits.
func (e *Engine) decide(p db.Platform, findings []Finding) Verdict {
	v := Verdict{Findings: findings}
	for _, f := range findings {
		k := Key{Platform: p, Action: f.Action, Checker: f.Checker}
		if e.matrix.Allows(e.caps, k) {
			v.Act = append(v.Act, f)
		}
	}
	return v
}

// Matrix returns the current matrix, for the API to render.
func (e *Engine) Matrix() Matrix { return e.matrix }

// SetMatrix replaces the matrix.
func (e *Engine) SetMatrix(m Matrix) { e.matrix = m }

// Capabilities returns the gate, so the API can render unavailable cells with
// their reason rather than the UI guessing at them.
func (e *Engine) Capabilities() Capabilities { return e.caps }

// ModelStats reports spend and health.
func (e *Engine) ModelStats() ModelStats {
	if e == nil {
		return ModelStats{}
	}
	return e.model.Stats()
}

// ModelEnabled reports whether the paid checker is configured and on.
//
// Exposed so the Hub can decide whether a message is worth queueing for the
// model at all, rather than queueing every message and discovering inside the
// connector that there is nothing to ask.
func (e *Engine) ModelEnabled() bool {
	return e != nil && e.model != nil && e.model.cfg.Enabled
}
