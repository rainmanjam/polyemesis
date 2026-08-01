package hooks

import (
	"encoding/json"
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
	if len(AllTriggers()) != 4 {
		t.Fatalf("AllTriggers has %d entries, want 4 -- add the new one to this "+
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
			// because Normalized is what drops the unknown triggers a stored
			// row might carry from an older release.
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

// An unknown trigger has two correct behaviours depending on the path, and
// conflating them hides one of them.
//
// Through Normalized -- which is how every caller reaches Validate -- it is
// DROPPED rather than rejected. That is what lets a row written by a newer
// release load on an older binary instead of making the hook unopenable: it
// loses the trigger it cannot honour and keeps working for the rest.
//
// Called directly, Validate still rejects it. That branch is unreachable via
// the normal path and exists for a caller that skips normalisation, so it is
// driven directly here rather than through a Normalized that would have thrown
// the input away first.
func TestAnUnknownTriggerIsDroppedByNormalizeAndRejectedByValidate(t *testing.T) {
	raw := Hook{
		Name: "deploy", URL: "https://example.com/h",
		Triggers: []Trigger{TriggerDestinationUp, "ingest.exploded"},
	}

	norm := raw.Normalized()
	if err := norm.Validate(); err != nil {
		t.Fatalf("a hook carrying an unknown trigger failed to normalize into a "+
			"valid one: %v -- a row from a newer release must degrade, not break", err)
	}
	if len(norm.Triggers) != 1 || norm.Triggers[0] != TriggerDestinationUp {
		t.Fatalf("triggers = %v, want only the known one kept", norm.Triggers)
	}

	if err := raw.Validate(); err == nil {
		t.Fatal("Validate accepted an unknown trigger when called directly; the " +
			"branch exists for callers that skip Normalized and must still bite")
	} else if !strings.Contains(err.Error(), "unknown trigger") {
		t.Fatalf("error = %q, want it to name the unknown trigger", err)
	}
}
