package automod

import "testing"

// A stored self_harm rule above flag must not fail the whole set on load
// (that left automod running with no rules at all on upgrade). It is clamped
// to flag and every other rule still fires.
func TestStoredSelfHarmRuleIsClampedNotFatalOnLoad(t *testing.T) {
	rules := []Rule{
		{ID: 1, Name: "sh", Enabled: true, Pattern: `crisis`, Action: ActionTimeout, TimeoutSeconds: 60, Category: CategorySelfHarm},
		{ID: 2, Name: "other", Enabled: true, Pattern: `spamword`, Action: ActionDelete},
	}
	if _, err := NewRuleSet(rules); err == nil {
		t.Fatal("the strict (save) path accepted self_harm+timeout")
	}
	set, clamped, err := LoadRuleSet(rules)
	if err != nil {
		t.Fatalf("LoadRuleSet failed the whole set: %v", err)
	}
	if len(clamped) != 1 || clamped[0] != 1 {
		t.Fatalf("clamped = %v, want [1]", clamped)
	}
	got := set.Check("this is a crisis")
	if len(got) != 1 || got[0].Action != ActionFlag {
		t.Fatalf("self_harm rule not clamped to flag: %+v", got)
	}
	got = set.Check("buy spamword now")
	if len(got) != 1 || got[0].Action != ActionDelete {
		t.Fatalf("the other rule stopped firing: %+v", got)
	}
}
