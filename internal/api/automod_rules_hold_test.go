package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// FINDING 4: the settings -> engine copy. A rule stored with holdOverlay and a
// category must come out of rulesFromSettings holding, carrying that category;
// a rule stored without them must come out exactly as it always did.
func TestRulesFromSettingsCarriesHoldAndCategory(t *testing.T) {
	a := db.AutomodSettings{Rules: []db.AutomodRule{
		{ID: 1, Name: "held", Enabled: true, Pattern: "maybeslur", Action: "flag", HoldOverlay: true, Category: "hate_speech"},
		{ID: 2, Name: "plain", Enabled: true, Pattern: "justflag", Action: "flag"},
	}}
	rs, err := rulesFromSettings(a)
	if err != nil {
		t.Fatal(err)
	}
	e := automod.New(automod.Matrix{Enabled: true}, automod.PlatformCaps{}, rs, nil, nil)

	v := e.CheckFast(db.PlatformTwitch, automod.Subject{AuthorID: "u", Text: "so maybeslur"})
	if !v.Hold || len(v.Findings) != 1 || !v.Findings[0].Hold || v.Findings[0].Category != automod.CategoryHateSpeech {
		t.Fatalf("held rule: %+v", v)
	}
	if len(v.Act) != 0 {
		t.Errorf("a hold armed an action: %+v", v.Act)
	}

	v = e.CheckFast(db.PlatformTwitch, automod.Subject{AuthorID: "u", Text: "just justflag"})
	if v.Hold || len(v.Findings) != 1 || v.Findings[0].Hold || v.Findings[0].Category != automod.CategoryFilterMatch {
		t.Fatalf("plain rule changed: %+v", v)
	}

	// A category outside the closed set refuses the whole set, as a bad pattern does.
	if _, err := rulesFromSettings(db.AutomodSettings{Rules: []db.AutomodRule{
		{ID: 3, Name: "x", Enabled: true, Pattern: "a", Action: "flag", Category: "nope"},
	}}); err == nil {
		t.Error("an unknown category was accepted")
	}
}
