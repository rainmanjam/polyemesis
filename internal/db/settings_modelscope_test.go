package db

import (
	"fmt"
	"strings"
	"testing"
)

// D11: a fresh install asks the model about hard cases only; an install that
// predates the field keeps asking about every message, so an upgrade never
// stops checking what it used to check.
func TestModelScopeFreshIsHardAndAnUpgradeKeepsAll(t *testing.T) {
	d := testDB(t)
	fresh, err := d.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.Automod.Protection.ModelScope; got != ModelScopeHard {
		t.Fatalf("a fresh install has scope %q, want hard", got)
	}

	for name, blob := range map[string]string{
		"no automod block":               `{}`,
		"no protection block":            `{"automod":{"enabled":true,"model":{"enabled":true}}}`,
		"protection without a scope":     `{"automod":{"protection":{"state":"on"}}}`,
		"protection undecided, no scope": `{"automod":{"protection":{}}}`,
	} {
		if _, err := d.SQL().Exec(`INSERT OR REPLACE INTO settings (id, json) VALUES (1, ?)`, blob); err != nil {
			t.Fatal(err)
		}
		s, err := d.GetSettings()
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Automod.Protection.ModelScope; got != ModelScopeAll {
			t.Errorf("%s: scope %q, want all", name, got)
		}
	}

	// An answer is kept, whichever way it points.
	for _, sc := range []string{ModelScopeAll, ModelScopeHard} {
		if _, err := d.SQL().Exec(`UPDATE settings SET json = ? WHERE id = 1`, `{"automod":{"protection":{"modelScope":"`+sc+`"}}}`); err != nil {
			t.Fatal(err)
		}
		if s, _ := d.GetSettings(); s.Automod.Protection.ModelScope != sc {
			t.Fatalf("stored %q reads %q", sc, s.Automod.Protection.ModelScope)
		}
	}
}

// A client that sends only the pack's state must not move the scope, and a
// client that sends only the scope must not move the state.
func TestSavingOneProtectionFieldLeavesTheScope(t *testing.T) {
	base := mergeBaseSettings().Automod
	got := decodeAutomod(t, base, `{"protection":{"state":"on"}}`)
	if got.Protection.ModelScope != ModelScopeAll {
		t.Fatalf("accepting the pack moved the scope to %q", got.Protection.ModelScope)
	}
	got = decodeAutomod(t, base, `{"protection":{"modelScope":"hard"}}`)
	if got.Protection.ModelScope != ModelScopeHard || got.Protection.State != "" {
		t.Fatalf("choosing the scope moved something else: %+v", got.Protection)
	}
}

func TestModelScopeIsValidated(t *testing.T) {
	for scope, ok := range map[string]bool{"": true, "all": true, "hard": true, "none": false, "HARD": false} {
		var probs []string
		AutomodProtection{ModelScope: scope}.problems(func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) })
		if got := !strings.Contains(strings.Join(probs, ";"), "modelScope"); got != ok {
			t.Errorf("scope %q: accepted=%v, want %v (%v)", scope, got, ok, probs)
		}
	}
}

func TestHoldWhenUnavailableDefaultsOffAndRoundTrips(t *testing.T) {
	if DefaultSettings().Automod.Model.HoldWhenUnavailable || DefaultSettings().Automod.Secondary.HoldWhenUnavailable {
		t.Fatal("the hold-on-failure setting is on by default")
	}
	got := decodeAutomod(t, mergeBaseSettings().Automod, `{"model":{"holdWhenUnavailable":true}}`)
	if !got.Model.HoldWhenUnavailable {
		t.Fatal("the setting did not decode")
	}
	// Unsent keeps it; sent false clears it.
	got = decodeAutomod(t, got, `{"model":{"timeoutSeconds":5}}`)
	if !got.Model.HoldWhenUnavailable {
		t.Fatal("an unrelated save cleared the setting")
	}
	got = decodeAutomod(t, got, `{"model":{"holdWhenUnavailable":false}}`)
	if got.Model.HoldWhenUnavailable {
		t.Fatal("an explicit false did not clear the setting")
	}
	if err := CheckAutomodFields([]byte(`{"model":{"holdWhenUnavailable":true}}`)); err != nil {
		t.Fatalf("the strict field check refuses the new field: %v", err)
	}
}
