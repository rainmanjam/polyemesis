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
	//
	// SERVER-SIDE. It carries the operator's own words -- a rule name, a
	// counted repetition -- and it is not what leaves for a platform. See
	// PlatformReason.
	Reason string `json:"reason"`
	// Category is the closed-set reason, and the ONLY thing that reaches a
	// third-party moderation record. See category.go and #495.
	Category Category `json:"category,omitempty"`
	// Note is free text from the model, kept for the operator's log because a
	// probabilistic verdict is much easier to review when you can read what the
	// model thought it saw.
	//
	// UNTRUSTED, and `json:"-"` is load-bearing: the model has just been handed
	// a viewer's message, so anything here may be that viewer's words wearing
	// the model's voice. It must not be serialised anywhere a client can read
	// it, and it must never be handed to an adapter. #495.
	Note string `json:"-"`
	// RuleID names the rule, when a rule produced this.
	RuleID int64 `json:"ruleId,omitempty"`
	// Confidence is only meaningful for the model checker, which is the only
	// one that is not deterministic. For a Jev checker it is the probability
	// that the message breaks the operator's rule.
	Confidence float64 `json:"confidence,omitempty"`
	// DecidedBy is which model checker answered: the primary, or the
	// secondary because the primary could not. Empty for the rules and history
	// checkers. Added rather than folded into Checker, so the matrix's single
	// "model" column -- and every stored cell -- stays as it was.
	DecidedBy Slot `json:"decidedBy,omitempty"`
	// Provider is the preset ID of the checker that answered (jev, cerebras,
	// ollama, ...).
	Provider string `json:"provider,omitempty"`
	// Hold asks that the message be kept off every on-screen overlay, whatever
	// Action says. An ATTRIBUTE, not an action: a finding asks for exactly one
	// action, and "ban if that is armed, and hold in any case" is two things.
	// Holding is local and reversible like flagging -- the console still shows
	// the message, nothing goes to a platform -- so it needs no matrix cell and
	// no capability row. See Engine.decide for the two switches that bound it.
	Hold bool `json:"hold,omitempty"`
	// Detector names which detector inside a checker fired, for a policy that
	// differs per detector: history's "repeats" and "caps", the pack checker's
	// group ("doxxing/phone"). Empty for the rules and model checkers. Display
	// and policy only; nothing acts on it.
	Detector string `json:"detector,omitempty"`
}

// PlatformReason is the only text this package will let out to a third party.
//
// A pure function of Category, with no path to any free-text field at all --
// not Reason, not Note, not a rule name. That is what makes it a device rather
// than a habit: there is nothing to remember at the call site, because there is
// no argument a caller could get wrong.
//
// The cost, taken deliberately: a rule-based ban records "matched a chat
// filter" on Twitch rather than the operator's rule name. That detail is still
// in Reason, in the server log and the review queue, which is where an operator
// reviews their own moderation anyway. Twitch and Kick keep this string for
// ever, so the bar for what may reach it is a fixed sentence, not a useful one.
func (f Finding) PlatformReason() string { return f.Category.Reason() }

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
	// DecidedBy is which model checker answered, set on a model verdict even
	// when it found nothing -- "the secondary looked and it was fine" is
	// still worth knowing when the primary is down.
	DecidedBy Slot `json:"decidedBy,omitempty"`
	// Hold is set when any finding asked to be held off the overlay and the
	// global and platform switches allow it. Not part of Act: nothing is
	// performed on a platform for it.
	Hold bool `json:"hold,omitempty"`
}

// Flagged reports whether anything at all was found, which is what puts a
// message in front of a human regardless of whether anything was automatic.
func (v Verdict) Flagged() bool { return len(v.Findings) > 0 }
