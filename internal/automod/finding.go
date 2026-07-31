package automod

import "sort"

// Finding is one checker's opinion about one message.
//
// An opinion, not an instruction. Whether it becomes an action is the matrix's
// decision, and keeping those separate is what lets the same finding be
// automatic on one platform and merely flagged on another.
type Finding struct {
	Checker Checker `json:"checker"`
	// Action is what this finding asks for.
	Action Action `json:"action"`
	// TimeoutSeconds applies to ActionTimeout. Always seconds: Kick counts
	// minutes and its adapter converts at the last moment, so everything
	// upstream of the adapters speaks one unit. A "600" that means ten minutes
	// on two platforms and seven days on a third is the trap this avoids.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// Reason is shown to the operator and stored with any action taken, so a
	// disputed moderation decision can be explained afterwards. A decision
	// nobody can account for is worse than none.
	Reason string `json:"reason"`
	// RuleID names the rule, when a rule produced this.
	RuleID int64 `json:"ruleId,omitempty"`
	// Confidence is only meaningful for the model checker, which is the only
	// one that is not deterministic.
	Confidence float64 `json:"confidence,omitempty"`
}

// sortByConsequence orders findings worst-first, so a caller taking only the
// first acts on the most serious thing found rather than the first one noticed.
func sortByConsequence(f []Finding) []Finding {
	sort.SliceStable(f, func(i, j int) bool {
		return actionRank(f[i].Action) > actionRank(f[j].Action)
	})
	return f
}

// Verdict is everything the checkers concluded about one message, plus what the
// matrix permits doing about it.
type Verdict struct {
	// Findings is every opinion, kept whole even when nothing is permitted --
	// this is what the review queue shows a human.
	Findings []Finding `json:"findings"`
	// Act is the subset the matrix allows to happen automatically. Empty is the
	// normal case on a fresh install, and it is not a failure.
	Act []Finding `json:"act"`
}

// Flagged reports whether anything at all was found, which is what puts a
// message in front of a human regardless of whether anything was automatic.
func (v Verdict) Flagged() bool { return len(v.Findings) > 0 }
