package automod

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func holdRules(t *testing.T, hold bool, cat Category) *RuleSet {
	t.Helper()
	rs, err := NewRuleSet([]Rule{{
		ID: 7, Name: "term", Enabled: true, Pattern: `forbidden`, Action: ActionFlag,
		HoldOverlay: hold, Category: cat,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// A rule that sets holdOverlay holds, with NOTHING armed: no cell, no action.
func TestARuleWithHoldOverlayHoldsWithoutArmingAnything(t *testing.T) {
	e := New(Matrix{Enabled: true}, PlatformCaps{}, holdRules(t, true, ""), nil, nil)
	v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u1", Text: "this is forbidden"})
	if !v.Hold {
		t.Fatalf("verdict not held: %+v", v)
	}
	if len(v.Act) != 0 {
		t.Fatalf("a hold armed an action: %+v", v.Act)
	}
	if len(v.Findings) != 1 || !v.Findings[0].Hold {
		t.Fatalf("finding does not carry the hold: %+v", v.Findings)
	}
}

// THE NO-BEHAVIOUR-CHANGE PIN: the same rule without holdOverlay never holds,
// under every matrix, and the finding is the one it always was.
func TestARuleWithoutHoldOverlayNeverHolds(t *testing.T) {
	for name, m := range map[string]Matrix{
		"default":  DefaultMatrix(),
		"enabled":  {Enabled: true},
		"disabled": {},
	} {
		t.Run(name, func(t *testing.T) {
			e := New(m, PlatformCaps{}, holdRules(t, false, ""), nil, nil)
			v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u1", Text: "this is forbidden"})
			if v.Hold {
				t.Fatalf("held without holdOverlay: %+v", v)
			}
			if len(v.Findings) != 1 || v.Findings[0].Hold || v.Findings[0].Category != CategoryFilterMatch {
				t.Fatalf("finding changed: %+v", v.Findings)
			}
		})
	}
}

// Hold uses the two kill switches, and only those.
func TestHoldFollowsTheGlobalAndPlatformSwitches(t *testing.T) {
	rs := holdRules(t, true, "")
	for _, tc := range []struct {
		name string
		m    Matrix
		p    db.Platform
		want bool
	}{
		{"global off", Matrix{}, db.PlatformTwitch, false},
		{"platform off", Matrix{Enabled: true, PlatformEnabled: map[db.Platform]bool{db.PlatformTwitch: false}}, db.PlatformTwitch, false},
		{"other platform off", Matrix{Enabled: true, PlatformEnabled: map[db.Platform]bool{db.PlatformKick: false}}, db.PlatformTwitch, true},
		{"platform explicitly on", Matrix{Enabled: true, PlatformEnabled: map[db.Platform]bool{db.PlatformTwitch: true}}, db.PlatformTwitch, true},
		{"no cells at all", Matrix{Enabled: true}, db.PlatformYouTube, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(tc.m, PlatformCaps{}, rs, nil, nil)
			if got := e.CheckFast(tc.p, Subject{AuthorID: "u", Text: "forbidden"}).Hold; got != tc.want {
				t.Errorf("Hold = %v, want %v", got, tc.want)
			}
		})
	}
}

// CheckText (alert text) and CheckModel's decide path set Hold the same way.
func TestHoldReachesCheckTextAndTheModelPath(t *testing.T) {
	e := New(Matrix{Enabled: true}, PlatformCaps{}, holdRules(t, true, ""), nil, nil)
	if !e.CheckText(db.PlatformTwitch, "so forbidden").Hold {
		t.Error("CheckText did not hold")
	}
	v := e.decide(db.PlatformTwitch, []Finding{{Checker: CheckerModel, Action: ActionFlag, Hold: true}})
	if !v.Hold {
		t.Error("decide did not hold a model finding that asked for it")
	}
	if (*Engine)(nil).CheckText(db.PlatformTwitch, "x").Hold {
		t.Error("nil engine held")
	}
}

func TestARuleCategoryIsCarriedAndDefaultsToFilterMatch(t *testing.T) {
	got := holdRules(t, false, CategoryHateSpeech).Check("forbidden")
	if len(got) != 1 || got[0].Category != CategoryHateSpeech {
		t.Fatalf("category = %+v", got)
	}
	if got[0].PlatformReason() != CategoryHateSpeech.Reason() {
		t.Errorf("platform reason %q does not follow the category", got[0].PlatformReason())
	}
	got = holdRules(t, false, "").Check("forbidden")
	if got[0].Category != CategoryFilterMatch {
		t.Fatalf("default category = %q", got[0].Category)
	}
}

func TestARuleWithAnUnknownCategoryIsRefused(t *testing.T) {
	_, err := NewRuleSet([]Rule{{ID: 1, Name: "x", Enabled: true, Pattern: "a", Action: ActionFlag, Category: "made_up"}})
	if err == nil {
		t.Fatal("accepted a category outside the closed set")
	}
}

// Subject carries the author to the history checker, as the positional
// argument did.
func TestSubjectAuthorReachesTheHistoryChecker(t *testing.T) {
	limits := DefaultHistoryLimits()
	limits.MaxMessages = 1
	e := New(DefaultMatrix(), PlatformCaps{}, nil, NewHistory(limits), nil)
	e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u1", Text: "one"})
	if v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u1", Text: "two"}); !v.Flagged() {
		t.Error("the second message from the same author was not counted")
	}
	if v := e.CheckFast(db.PlatformTwitch, Subject{AuthorID: "u2", Text: "one"}); v.Flagged() {
		t.Error("a different author was counted against the first")
	}
}
