package db

import (
	"encoding/json"
	"strings"
	"testing"
)

// The wire shape of a rule that does not hold is byte-for-byte what it was:
// the two new fields are omitempty, so an existing install's stored blob and
// every response that carries one are unchanged.
func TestARuleWithoutHoldOrCategoryMarshalsAsBefore(t *testing.T) {
	b, err := json.Marshal(AutomodRule{ID: 1, Name: "n", Enabled: true, Pattern: "p", Action: "flag"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":1,"name":"n","enabled":true,"pattern":"p","action":"flag"}`
	if string(b) != want {
		t.Fatalf("rule JSON changed:\n got %s\nwant %s", b, want)
	}
}

// A blob stored before the fields existed decodes with both unset, through the
// real settings decoder; and the new fields round-trip when set.
func TestStoredRulesWithoutTheNewFieldsDecodeUnheld(t *testing.T) {
	old := `{"rules":[{"id":1,"name":"n","enabled":true,"pattern":"p","action":"flag"}]}`
	a := mergeBaseSettings().Automod
	if err := json.Unmarshal([]byte(old), &a); err != nil {
		t.Fatal(err)
	}
	if len(a.Rules) != 1 || a.Rules[0].HoldOverlay || a.Rules[0].Category != "" {
		t.Fatalf("old rule decoded as %+v", a.Rules)
	}

	set := `{"rules":[{"id":1,"name":"n","enabled":true,"pattern":"p","action":"flag","holdOverlay":true,"category":"hate_speech"}]}`
	if err := json.Unmarshal([]byte(set), &a); err != nil {
		t.Fatal(err)
	}
	if !a.Rules[0].HoldOverlay || a.Rules[0].Category != "hate_speech" {
		t.Fatalf("new fields lost: %+v", a.Rules)
	}
	// Strict fields (the PUT path) accept them; a typo is still refused.
	if err := CheckAutomodFields([]byte(set)); err != nil {
		t.Fatalf("strict decode refused the new fields: %v", err)
	}
	if err := CheckAutomodFields([]byte(strings.Replace(set, "holdOverlay", "holdOverlays", 1))); err == nil {
		t.Fatal("a misspelt holdOverlay was accepted")
	}
	// Replace, not merge: dropping the hold in a later save takes it off, rather
	// than inheriting the stored rule's true by position.
	if err := json.Unmarshal([]byte(old), &a); err != nil {
		t.Fatal(err)
	}
	if a.Rules[0].HoldOverlay || a.Rules[0].Category != "" {
		t.Fatalf("a rule saved without hold inherited it: %+v", a.Rules[0])
	}
}
