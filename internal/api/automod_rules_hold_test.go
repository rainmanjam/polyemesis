package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
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

// Saving a self_harm rule above flag is refused with 400; the load path clamps.
func TestSavingSelfHarmRuleAboveFlagIs400(t *testing.T) {
	h, _, sign := sourceServer(t)
	var before map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &before)
	am := before["automod"].(map[string]any)
	am["enabled"] = true
	am["rules"] = []map[string]any{
		{"id": 1, "name": "sh", "enabled": true, "pattern": "crisis", "action": "timeout", "timeoutSeconds": 60, "category": "self_harm"},
	}
	send(t, h, sign, http.MethodPut, "/api/v1/settings", before, http.StatusBadRequest)
}

// The load path, through ApplyAutomod: a stored self_harm+timeout row (written
// before the ceiling existed) must not take the other rules down with it, and
// the clamp is logged once per distinct set, not on every settings save.
func TestStoredSelfHarmTimeoutRowDoesNotDisarmTheOtherRules(t *testing.T) {
	store := dbtest.OpenCheap(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	doc := db.DefaultSettings()
	if err := store.PutSettings(doc); err != nil {
		t.Fatal(err)
	}
	a := doc.Automod
	a.Enabled = true
	a.Rules = []db.AutomodRule{
		{ID: 1, Name: "sh", Enabled: true, Pattern: "crisis", Action: "timeout", TimeoutSeconds: 60, Category: "self_harm"},
		{ID: 2, Name: "other", Enabled: true, Pattern: "spamword", Action: "delete"},
	}
	hub := newTestHub(t)
	ps := automod.NewPersistent()
	pb, sb := automod.NewBudget(), automod.NewBudget()
	ApplyAutomod(AutomodApply{Hub: hub, Store: store, Log: log, Settings: a, Budget: pb, SecondaryBudget: sb, Persist: ps})
	mod := hub.Moderator()
	if mod == nil {
		t.Fatal("no moderator attached: the stored row disarmed automod")
	}
	check := func(text string) []automod.Finding {
		return mod.CheckFast(db.PlatformTwitch, automod.Subject{AuthorID: "u", AuthorName: "v", Text: text}).Findings
	}
	if f := check("buy spamword"); len(f) != 1 || f[0].Action != automod.ActionDelete {
		t.Errorf("the other rule stopped firing: %+v", f)
	}
	if f := check("in crisis"); len(f) != 1 || f[0].Action != automod.ActionFlag {
		t.Errorf("self_harm rule not clamped to flag: %+v", f)
	}
	// Three more saves with the same rows: still one warning.
	for i := 0; i < 3; i++ {
		ApplyAutomod(AutomodApply{Hub: hub, Store: store, Log: log, Settings: a, Budget: pb, SecondaryBudget: sb, Persist: ps})
	}
	if n := strings.Count(buf.String(), "clamped to flag"); n != 1 {
		t.Errorf("%d clamp warnings across 4 saves, want 1:\n%s", n, buf.String())
	}
	// A different clamped set is news again.
	a.Rules = append(a.Rules, db.AutomodRule{ID: 3, Name: "sh2", Enabled: true, Pattern: "hurt", Action: "ban", Category: "self_harm"})
	ApplyAutomod(AutomodApply{Hub: hub, Store: store, Log: log, Settings: a, Budget: pb, SecondaryBudget: sb, Persist: ps})
	if n := strings.Count(buf.String(), "clamped to flag"); n != 2 {
		t.Errorf("%d clamp warnings after the set changed, want 2", n)
	}
}
