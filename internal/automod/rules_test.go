package automod

import (
	"strings"
	"testing"
)

// These are the gaps left after the coverage in checkers_test.go and
// engine_test.go, which already pin matching, disabled rules, a bad pattern
// refusing the set, unknown actions, evasion normalisation and the despacing
// heuristic. Only what those do not reach is here.

// A ZERO-VALUED Rule must answer false rather than panic.
//
// Match guards on r.re == nil, and nothing exercised that: every other test
// compiles first. A Rule that reaches Match uncompiled is a nil *regexp.Regexp
// away from taking the chat pipeline down, and it is reachable from any future
// path that builds a Rule without going through NewRuleSet.
func TestAnUncompiledRuleAnswersRatherThanPanicking(t *testing.T) {
	var zero Rule
	if zero.Match("anything") {
		t.Error("a zero-valued rule reported a match")
	}
	// Enabled but still uncompiled: the guard has two halves and this is the
	// one a caller can reach by setting the field they can see.
	enabled := Rule{Name: "r", Enabled: true, Pattern: "spam"}
	if enabled.Match("spam") {
		t.Error("an enabled but uncompiled rule matched; re is nil and this panics " +
			"if the guard is dropped")
	}
}

// A nil *RuleSet checks to nothing. This is the DEFAULT state -- no rules
// configured -- so it is on the path of every install that has not written one.
func TestANilRuleSetChecksToNothing(t *testing.T) {
	var s *RuleSet
	if got := s.Check("anything at all"); got != nil {
		t.Errorf("nil RuleSet returned %+v, want nil", got)
	}
}

// An empty or whitespace-only pattern is refused specifically.
//
// TestABadPatternRefusesTheWholeSet covers an UNPARSEABLE pattern. An empty one
// parses fine and matches EVERYTHING, so without this check a rule saved with a
// blank pattern would silently apply its action to every message that arrives.
func TestAnEmptyPatternIsRefusedBecauseItWouldMatchEverything(t *testing.T) {
	for _, p := range []string{"", "   ", "\t\n"} {
		r := Rule{Name: "blank", Enabled: true, Pattern: p, Action: ActionDelete}
		if err := r.Compile(); err == nil {
			t.Errorf("Compile accepted pattern %q; empty compiles fine and matches "+
				"every message, so the action would apply to all of them", p)
		}
	}
}

// Run-collapsing has a consequence worth stating rather than rediscovering.
//
// Normalise folds "sssspam" toward "spam", and the same rule necessarily folds
// legitimate doubles: "moon" normalises to "mon". That is an accepted cost, and
// it is only safe because Check also matches the RAW text -- so a rule written
// for "moon" still fires on "moon" even though the normalised form has lost a
// letter. Pinning it here so a future change to either half has to consider the
// other.
func TestCollapsingRunsAlsoCollapsesLegitimateDoubles(t *testing.T) {
	if got := Normalise("moon"); got != "mon" {
		t.Errorf("Normalise(\"moon\") = %q, want \"mon\" -- if this changed, check "+
			"that sssspam still folds", got)
	}
	set, err := NewRuleSet([]Rule{{
		Name: "moon", Enabled: true, Pattern: "moon", Action: ActionFlag,
	}})
	if err != nil {
		t.Fatalf("NewRuleSet: %v", err)
	}
	if got := set.Check("look at the moon"); len(got) == 0 {
		t.Error("a rule for \"moon\" missed the word \"moon\": the raw-text match is " +
			"what makes run-collapsing survivable, and it is not working")
	}
}

// The link-spam limit fires. It never had.
//
// countLinks was called on the NORMALISED message, and Normalise collapses runs
// of repeated letters -- the step that folds "sssspam" to "spam". It also folds
// "http://" to "htp://", "https://" to "htps://" and "www." to "w.", so none of
// the three strings countLinks searches for could survive. It returned 0 for
// every message ever sent, and MaxLinks -- 3 by DEFAULT, so on for everyone --
// never fired once. A limit every operator had configured and none of them had.
//
// Nothing caught it because countLinks had no test and no test mentioned links,
// so there was never an assertion for the always-zero to fail.
func TestTheLinkLimitActuallyFires(t *testing.T) {
	lim := DefaultHistoryLimits()
	lim.MaxLinks = 2
	h := NewHistory(lim)

	// Three schemed links across three messages, over a limit of two.
	h.Observe("twitch", "spammer", "check https://one.example")
	h.Observe("twitch", "spammer", "and http://two.example")
	got := h.Observe("twitch", "spammer", "and https://three.example")

	if !hasReason(got, "links") {
		t.Fatalf("three links against a limit of 2 produced no link finding: %+v.\n"+
			"If this is zero again, check whether the count is being taken from the "+
			"normalised text -- 'https://' does not survive Normalise", got)
	}
}

// The scheme-less fallback works too. Dropping "https://" is the obvious way
// past a filter that only counts schemes, which is why countLinks falls back to
// "www." -- and that fallback was the most thoroughly dead part of it, since
// "www" collapses to "w" first.
func TestBareDomainsCountAsLinks(t *testing.T) {
	lim := DefaultHistoryLimits()
	lim.MaxLinks = 1
	h := NewHistory(lim)

	h.Observe("twitch", "spammer", "www.one.example")
	got := h.Observe("twitch", "spammer", "www.two.example")

	if !hasReason(got, "links") {
		t.Errorf("two bare www. domains against a limit of 1 produced no finding: %+v", got)
	}
}

// Ordinary conversation does not trip it. A detector that fires on normal
// traffic trains an operator to ignore it.
func TestOrdinaryMessagesDoNotTripTheLinkLimit(t *testing.T) {
	lim := DefaultHistoryLimits()
	lim.MaxLinks = 3
	h := NewHistory(lim)
	for _, m := range []string{"hello there", "how is everyone", "nice stream"} {
		if got := h.Observe("twitch", "regular", m); hasReason(got, "links") {
			t.Errorf("%q produced a link finding: %+v", m, got)
		}
	}
}

func hasReason(fs []Finding, substr string) bool {
	for _, f := range fs {
		if strings.Contains(f.Reason, substr) {
			return true
		}
	}
	return false
}
