package automod

import (
	"context"
	"time"
)

// The Test button, and why a checker cannot be enabled without passing it.
//
// A connector that fails open on every message looks EXACTLY like a model that
// approves of everything. The spike found three real ways to get there: a
// guard model that answers {"No": null}, a reasoning model that thinks past the
// timeout every time, and a JSON mode an endpoint quietly ignores. None of them
// makes a sound in normal running -- the failure counter climbs on a card
// nobody is watching. Three canned messages, sent once, catch all three before
// the checker is trusted with a real chat.
//
// The messages are written here and are synthetic. Nothing an operator's
// viewers said is ever sent by a test.

// ProbeCase is one canned message and what a working checker must say.
type ProbeCase struct {
	Name string `json:"name"`
	Text string `json:"-"`
	// MustFlag and MustPass are the pass criteria. A case with neither is
	// reported but does not decide the result -- the spam line, because the
	// default Instruction is about abuse and does not mention spam.
	MustFlag bool `json:"mustFlag,omitempty"`
	MustPass bool `json:"mustPass,omitempty"`
}

// ProbeCases is the set. Order matters only for the report.
var ProbeCases = []ProbeCase{
	{Name: "harmless", Text: "gg everyone, that last round was close! what are we playing next?", MustPass: true},
	{Name: "abusive", Text: "you are worthless trash, nobody here wants you, go kill yourself", MustFlag: true},
	{Name: "spam", Text: "FREE CRYPTO GIVEAWAY!!! send 0.1 BTC to claim 10 BTC now at free-coins.example"},
}

// ProbeCaseResult is one case's outcome.
type ProbeCaseResult struct {
	Case       string   `json:"case"`
	OK         bool     `json:"ok"`
	Flagged    bool     `json:"flagged"`
	Action     Action   `json:"action,omitempty"`
	Category   Category `json:"category,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`
	LatencyMs  int64    `json:"latencyMs"`
	// Error is the connector's own error text: fixed sentences and a status
	// code, with any endpoint already redacted. Never a response body.
	Error string `json:"error,omitempty"`
}

// ProbeProblem is why a probe did not pass, as a code the UI translates.
type ProbeProblem string

const (
	// ProbeErrored: a case could not be answered (timeout, status, malformed).
	ProbeErrored ProbeProblem = "error"
	// ProbeHarmlessFlagged: the harmless line was flagged.
	ProbeHarmlessFlagged ProbeProblem = "harmless_flagged"
	// ProbeAbusiveMissed: the abusive line was not flagged.
	ProbeAbusiveMissed ProbeProblem = "abusive_missed"
)

// ProbeResult is the whole report.
type ProbeResult struct {
	Passed  bool              `json:"passed"`
	Problem ProbeProblem      `json:"problem,omitempty"`
	Cases   []ProbeCaseResult `json:"cases"`
}

// Probe runs every case against one checker, in order, and judges the result.
// The checker should be built for the test -- enabled, with no budget -- so a
// spent hourly allowance cannot make a working endpoint look broken.
func Probe(ctx context.Context, mc ModelChecker) ProbeResult {
	var res ProbeResult
	for _, pc := range ProbeCases {
		start := time.Now()
		findings, err := mc.Check(ctx, pc.Text)
		r := ProbeCaseResult{Case: pc.Name, LatencyMs: time.Since(start).Milliseconds()}
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			if len(findings) > 0 {
				f := findings[0]
				r.Flagged, r.Action, r.Category, r.Confidence = true, f.Action, f.Category, f.Confidence
			}
		}
		res.Cases = append(res.Cases, r)
		// The FIRST problem is the one reported, in case order, so the
		// operator fixes the most basic thing first: an error before a
		// judgement about what the answer said.
		if res.Problem != "" {
			continue
		}
		switch {
		case !r.OK:
			res.Problem = ProbeErrored
		case pc.MustPass && r.Flagged:
			res.Problem = ProbeHarmlessFlagged
		case pc.MustFlag && !r.Flagged:
			res.Problem = ProbeAbusiveMissed
		}
	}
	res.Passed = res.Problem == ""
	return res
}
