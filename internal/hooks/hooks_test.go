package hooks

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The trigger strings are stored configuration: a hook subscribes by name and
// the name lives in the database. Renaming one silently unsubscribes every
// hook that used it, with no error anywhere. This pins the wire values.
func TestTriggerStringsAreFrozen(t *testing.T) {
	want := map[Trigger]string{
		TriggerIngestPublished:    "ingest.published",
		TriggerIngestDisconnected: "ingest.disconnected",
		TriggerDestinationUp:      "destination.up",
		TriggerDestinationDown:    "destination.down",
		TriggerTest:               "test",
	}
	for tr, s := range want {
		if string(tr) != s {
			t.Errorf("trigger renamed: %q, want %q -- every stored hook that "+
				"subscribed to it has just been silently unsubscribed", tr, s)
		}
	}
}

// TriggerTest must not be subscribable, for the same reason alerts.TypeTest is
// not: a test delivery that a subscription filter swallows teaches the
// operator that their endpoint is broken when it is not.
func TestAllTriggersExcludesTest(t *testing.T) {
	for _, tr := range AllTriggers() {
		if tr == TriggerTest {
			t.Fatal("TriggerTest is in AllTriggers; a test button must bypass " +
				"the subscription filter, not appear in it")
		}
	}
	// Six since destination.rolledover joined it. The number is asserted rather
	// than derived for this test's original reason -- a test that asks
	// AllTriggers what is in AllTriggers cannot fail -- and raising it is the
	// step that makes somebody go and write the docs row. It worked: this
	// failing is what sent the docs row below back to be written.
	if len(AllTriggers()) != 6 {
		t.Fatalf("AllTriggers has %d entries, want 6 -- add the new one to this "+
			"test and to the docs before shipping it", len(AllTriggers()))
	}
}

func TestHookWantsEmptySubscriptionMeansEverything(t *testing.T) {
	h := Hook{}.Normalized()
	for _, tr := range AllTriggers() {
		if !h.Wants(tr) {
			t.Errorf("a hook with no explicit triggers ignored %s; empty must "+
				"mean every trigger, matching alerts.Rule.Wants", tr)
		}
	}
}

func TestHookWantsAlwaysAcceptsTest(t *testing.T) {
	h := Hook{Triggers: []Trigger{TriggerDestinationUp}}.Normalized()
	if !h.Wants(TriggerTest) {
		t.Fatal("a narrow subscription swallowed a test delivery")
	}
}

func TestHookNeverMarshalsItsURLOrItsSecret(t *testing.T) {
	h := Hook{
		ID: 1, Name: "deploy",
		URL:    "https://hooks.example.com/services/T0/B1/XXXXsecretXXXX",
		Secret: "sh_ZZZZsigningZZZZ",
	}.Normalized()

	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if strings.Contains(body, "XXXXsecretXXXX") {
		t.Errorf("the endpoint path leaked: %s", body)
	}
	if strings.Contains(body, "ZZZZsigningZZZZ") {
		t.Errorf("the signing secret leaked: %s", body)
	}
	if !strings.Contains(body, "[redacted]") {
		t.Errorf("no masked endpoint; the UI has nothing to render: %s", body)
	}
	if !strings.Contains(body, `"hasSecret":true`) {
		t.Errorf("no hasSecret flag; the UI cannot tell a signed hook from an "+
			"unsigned one: %s", body)
	}
}

func TestHookValidate(t *testing.T) {
	tests := []struct {
		name string
		hook Hook
		want string // substring; "" means valid
	}{
		{"good", Hook{Name: "deploy", URL: "https://example.com/h"}, ""},
		{"no name", Hook{URL: "https://example.com/h"}, "needs a name"},
		{"no url", Hook{Name: "deploy"}, "needs a URL"},
		{"ftp", Hook{Name: "deploy", URL: "ftp://example.com/h"}, "http or https"},
		{"no host", Hook{Name: "deploy", URL: "https:///h"}, "no host"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Validate runs on the normalized value everywhere it is called,
			// so that is the order driven here.
			err := tc.hook.Normalized().Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("rejected a valid hook: %v", err)
			case tc.want == "":
			case err == nil:
				t.Fatalf("accepted %s", tc.name)
			case !strings.Contains(err.Error(), tc.want):
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// An unparseable URL must not be quoted back. A webhook URL carries its secret
// in the path, so an error message that echoes it puts the secret in the log
// the operator then pastes into an issue.
func TestValidateNeverQuotesTheURL(t *testing.T) {
	h := Hook{Name: "deploy", URL: "ftp://example.com/SECRETPATH"}.Normalized()
	err := h.Normalized().Validate()
	if err == nil {
		t.Fatal("accepted an ftp endpoint")
	}
	if strings.Contains(err.Error(), "SECRETPATH") {
		t.Fatalf("the error quoted the URL path: %v", err)
	}
}

// A hook saved with a mistyped trigger stored an empty Triggers list, and an
// empty list means EVERY trigger. The script got the whole firehose while its
// author believed it was subscribed to one transition, because Normalized had
// deleted the mistyped name before Validate could name it back.
//
// Normalized-then-Validate is the order every write path in the tree uses, so
// that is the order driven here; driving Validate on the raw value would prove
// nothing about the path an operator reaches.
func TestATypoedTriggerIsRefusedByNameRatherThanWidenedToEverything(t *testing.T) {
	raw := Hook{
		Name: "deploy", URL: "https://example.com/h",
		Triggers: []Trigger{TriggerDestinationUp, "ingest.exploded"},
	}

	norm := raw.Normalized()
	if len(norm.Triggers) != 2 {
		t.Fatalf("triggers = %v, want the mistyped name kept -- dropping it is how "+
			"a narrow subscription silently became every trigger", norm.Triggers)
	}
	err := norm.Validate()
	if err == nil {
		t.Fatal("Validate accepted a subscription to a trigger that does not exist")
	}
	if !strings.Contains(err.Error(), "ingest.exploded") {
		t.Fatalf("error = %q, want it to name the trigger the operator mistyped", err)
	}

	// The control. A validator that refuses every trigger name would satisfy the
	// assertions above while making the settings page unusable, so every name
	// AllTriggers offers has to be accepted.
	for _, tr := range AllTriggers() {
		ok := Hook{Name: "deploy", URL: "https://example.com/h", Triggers: []Trigger{tr}}
		if err := ok.Normalized().Validate(); err != nil {
			t.Errorf("Validate refused %q, which AllTriggers offers in the picker: %v", tr, err)
		}
	}
}

// The upgrade case, and the reason the refusal is at save and not at load.
// db.scanHook runs Normalized and never Validate, so a row written by a newer
// release still loads on this binary -- and it must stay NARROW while it does.
// Under the old code the unknown name was deleted on read, the list went empty,
// and a hook nobody had touched started delivering every transition to a script
// that had asked for one.
func TestAHookStoredAgainstARetiredTriggerStillLoadsAndStaysNarrow(t *testing.T) {
	stored := Hook{
		Name: "deploy", URL: "https://example.com/h",
		Triggers: []Trigger{TriggerDestinationUp, "destination.retired.in.a.later.version"},
	}.Normalized()

	if len(stored.Triggers) != 2 {
		t.Fatalf("triggers = %v, want the retired name kept so the list cannot go "+
			"empty and mean every trigger", stored.Triggers)
	}
	if !stored.Wants(TriggerDestinationUp) {
		t.Error("the hook stopped delivering the trigger it does name; a retired " +
			"name must cost that one subscription, not the whole hook")
	}
	if stored.Wants(TriggerIngestPublished) {
		t.Error("the hook now wants a trigger it never subscribed to; this is the " +
			"silent widening the retired name was supposed to cost nothing for")
	}
}

func TestCheckNameUniqueRefusesANameAlreadyInUse(t *testing.T) {
	existing := []Hook{
		{ID: 1, Name: "deploy"},
		{ID: 2, Name: "Archive"},
	}
	tests := []struct {
		name      string
		candidate Hook
		wantDup   bool
	}{
		// The controls. A checker that refuses every name would pass every
		// wantDup case below and lock the operator out of creating anything.
		{name: "a new name is free", candidate: Hook{Name: "mirror"}},
		{
			name:      "an existing hook keeping its own name is not its own duplicate",
			candidate: Hook{ID: 1, Name: "deploy"},
		},
		{name: "an exact duplicate", candidate: Hook{Name: "deploy"}, wantDup: true},
		{
			// "Deploy " and "deploy" render identically in a settings list, so
			// treating them as distinct hands the operator exactly the ambiguity
			// the check exists to remove.
			name:      "a duplicate in different case, which the list cannot show apart",
			candidate: Hook{Name: " Deploy "},
			wantDup:   true,
		},
		{
			name:      "renaming one hook onto another hook's name",
			candidate: Hook{ID: 2, Name: "deploy"},
			wantDup:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckNameUnique(tt.candidate, existing)
			if !tt.wantDup {
				if err != nil {
					t.Fatalf("CheckNameUnique = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("CheckNameUnique accepted a name another hook already " +
					"answers to; the operator cannot tell the two apart in the list")
			}
			if !errors.Is(err, ErrDuplicateHookName) {
				t.Errorf("error = %v, want it to wrap ErrDuplicateHookName so the "+
					"HTTP layer can answer 409 rather than 400", err)
			}
			if !strings.Contains(err.Error(), "deploy") {
				t.Errorf("error = %q, want it to name the hook already using it", err)
			}
		})
	}
}
