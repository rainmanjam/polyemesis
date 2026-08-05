// Compliance conflict resolution, tested against the functions themselves.
//
// The rule that decides what lives here: these exercise complianceByAccount,
// complianceByTarget and complianceFields directly and never build a request.
// A compliance test that goes through an endpoint belongs in metadata_test.go
// with the rest of the push.
package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

func TestTwoDestinationsOnOneAccountWithDifferentComplianceAreRefused(t *testing.T) {
	acct := int64(7)
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "main", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "backup", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	})
	if len(conflicts) == 0 {
		t.Fatal("two destinations asked one broadcast to be two things and it was allowed; " +
			"one of the operator's declarations would be discarded with nothing saying so")
	}
	// BOTH names, because "there is a conflict" the operator cannot locate is
	// barely better than the silence this replaces.
	if !strings.Contains(conflicts[0], "main") || !strings.Contains(conflicts[0], "backup") {
		t.Errorf("conflict %q does not name both destinations", conflicts[0])
	}
	// A refusal that still hands the account a value is not a refusal: the
	// caller has no way to tell "refused" from "resolved to the first one
	// seen" unless the absence of the entry IS the signal.
	if _, ok := got[acct]; ok {
		t.Errorf("account %d is still present in the result despite being reported as a conflict; "+
			"a caller that reads the map without checking conflicts would silently apply it", acct)
	}
}

// TestComplianceByTargetKeepsAConflictingAccountOutOfTheResolvedMap is the
// second line of defence, tested as such.
//
// handlePushMetadata refuses a conflict outright, so this can never be reached
// through the handler and no call-site guard can see it — which is exactly why
// it needs its own. The delete is what makes reading the map without checking
// conflicts fail safe: a caller that skipped the check would otherwise push the
// first destination's settings while reporting the push refused, which looks
// handled and is worse than no detection at all.
func TestComplianceByTargetKeepsAConflictingAccountOutOfTheResolvedMap(t *testing.T) {
	messy, tidy := int64(7), int64(9)
	got, conflicts := complianceByTarget([]db.Destination{
		{Name: "main", AccountID: &messy, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "backup", AccountID: &messy, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "elsewhere", AccountID: &tidy, Compliance: db.Compliance{Privacy: db.PrivacyUnlisted}},
	})

	if _, ok := got[messy]; ok {
		t.Errorf("the conflicting account is still in the resolved map as %+v; a caller that "+
			"read it without checking conflicts would silently apply one of two "+
			"declarations", got[messy])
	}
	if len(conflicts[messy]) == 0 {
		t.Error("the conflict was not reported against the account it is about, so a push " +
			"cannot tell whose problem it is")
	} else if !strings.Contains(conflicts[messy][0], "main") ||
		!strings.Contains(conflicts[messy][0], "backup") {
		t.Errorf("conflict %q does not name both destinations", conflicts[messy][0])
	}

	// The account that never disagreed is untouched by its neighbour's problem.
	if got[tidy].Compliance.Privacy != db.PrivacyUnlisted {
		t.Errorf("resolved privacy for the innocent account = %q, want unlisted",
			got[tidy].Compliance.Privacy)
	}
	if len(conflicts[tidy]) != 0 {
		t.Errorf("the innocent account was reported as conflicting: %v", conflicts[tidy])
	}
}

// TestThreeDisagreeingDestinationsAreAllWithheldAndAllNamed is the case two
// destinations could never show.
//
// The original loop asked the RESULT map whether it had seen this account, and
// removed the entry on conflict. With three disagreeing destinations the third
// therefore found nothing, read as the first one seen, and re-inserted: the
// account came back present, carrying the LAST destination's settings, with one
// conflict message naming only the first two. Present-and-resolved-anyway is
// exactly the "looks handled" outcome the removal exists to prevent, and the
// messier three-destination configuration is the likelier one to hit it.
func TestThreeDisagreeingDestinationsAreAllWithheldAndAllNamed(t *testing.T) {
	acct := int64(7)
	got, conflicts := complianceByTarget([]db.Destination{
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyUnlisted}},
	})

	if ac, ok := got[acct]; ok {
		t.Errorf("the account is present carrying %q's settings despite three destinations "+
			"disagreeing; a caller reading the map would apply one of them", ac.Destination)
	}
	// Every disagreeing destination, not just the first pair. An operator told
	// only about a-vs-b fixes it, pushes, and is refused over c.
	joined := strings.Join(conflicts[acct], " ")
	for _, name := range []string{`"a"`, `"b"`, `"c"`} {
		if !strings.Contains(joined, name) {
			t.Errorf("destination %s is never named in %v, so fixing what is reported still "+
				"leaves the push refused", name, conflicts[acct])
		}
	}
}

// TestEveryDestinationIsComparedAgainstTheFirstNotItsNeighbour is the guard
// against overcorrecting the above, and against a subtler shape: comparing each
// destination against the one BEFORE it rather than against the first.
//
// Agreement first — three destinations that all agree are ordinary, not three
// conflicts. Then the distinguishing case: with a, a, c the anchor is "a", so
// the message must name "a" and "c". A previous-neighbour comparison would name
// "b" and "c" instead, blaming a destination that agreed with everything and
// leaving the operator editing the wrong row. An all-agreeing fixture cannot
// tell those two implementations apart, which is why this one does not use one.
func TestEveryDestinationIsComparedAgainstTheFirstNotItsNeighbour(t *testing.T) {
	acct := int64(7)

	got, conflicts := complianceByTarget([]db.Destination{
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
	})
	if len(conflicts[acct]) != 0 {
		t.Errorf("three destinations that all agree were reported as conflicting: %v",
			conflicts[acct])
	}
	if got[acct].Compliance.Privacy != db.PrivacyPrivate {
		t.Errorf("resolved privacy = %q, want private", got[acct].Compliance.Privacy)
	}

	_, conflicts = complianceByTarget([]db.Destination{
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	})
	if len(conflicts[acct]) != 1 {
		t.Fatalf("got %d conflicts, want exactly one: only %q disagrees with the anchor",
			len(conflicts[acct]), "c")
	}
	if !strings.HasPrefix(conflicts[acct][0], `"a" and "c"`) {
		t.Errorf("conflict = %q, want it to open with %q: the disagreement is with the first "+
			"destination, and naming %q blames one that agreed with everything",
			conflicts[acct][0], `"a" and "c"`, "b")
	}
}

// TestComplianceByAccountDoesNotReorderItsCallersSlice guards the defensive
// copy. The function sorts by name to make its messages deterministic; doing
// that in place would reorder a slice the caller still owns, which is the kind
// of side effect that is invisible until something downstream depends on store
// order.
func TestComplianceByAccountDoesNotReorderItsCallersSlice(t *testing.T) {
	acct := int64(7)
	dests := []db.Destination{
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	}
	complianceByAccount(dests)
	for i, want := range []string{"c", "a", "b"} {
		if dests[i].Name != want {
			t.Fatalf("the caller's slice was reordered to %q...; complianceByAccount sorted "+
				"in place", dests[i].Name)
		}
	}
}

// TestComplianceFieldsNamesOnlyWhatWasSet pins the mapping a failed or skipped
// compliance write reports through. Its call sites are guarded elsewhere; what
// they cannot see is a single clause going missing, and the FacebookPrivacy one
// in particular — a Facebook-only failure would then name no skipped field at
// all, which combined with an unconfirmed write is the whole of Facebook's
// failure reporting gone.
func TestComplianceFieldsNamesOnlyWhatWasSet(t *testing.T) {
	kids := false
	tests := []struct {
		name string
		in   db.Compliance
		want []oauth.MetadataField
	}{
		{"nothing set", db.Compliance{}, nil},
		{"youtube privacy", db.Compliance{Privacy: db.PrivacyPrivate},
			[]oauth.MetadataField{oauth.FieldPrivacy}},
		{"facebook privacy is privacy too", db.Compliance{FacebookPrivacy: db.FBPrivacySelf},
			[]oauth.MetadataField{oauth.FieldPrivacy}},
		{"an explicit false is still a declaration", db.Compliance{MadeForKids: &kids},
			[]oauth.MetadataField{oauth.FieldMadeForKids}},
		{"twitch labels", db.Compliance{Labels: map[string]bool{"Gambling": true}},
			[]oauth.MetadataField{oauth.FieldLabels}},
		{"all of it", db.Compliance{
			Privacy: db.PrivacyPrivate, MadeForKids: &kids,
			Labels: map[string]bool{"Gambling": true}, FacebookPrivacy: db.FBPrivacySelf},
			[]oauth.MetadataField{oauth.FieldPrivacy, oauth.FieldMadeForKids, oauth.FieldLabels}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := complianceFields(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("complianceFields(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestConflictNamesDestinationsInSortedOrderNotStoreOrder(t *testing.T) {
	// Store order is c, a, b -- deliberately not sorted. Without the sort,
	// the first pair compared is (c, a), and the conflict message would name
	// "c" and "a". The function must sort by name first, so the pair actually
	// compared is (a, b) and the message names "a" before "b". Using c/a/b
	// rather than something subtler makes the difference unmistakable: the
	// two possible messages don't share a first name.
	acct := int64(7)
	_, conflicts := complianceByAccount([]db.Destination{
		{Name: "c", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
		{Name: "a", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "b", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPublic}},
	})
	//
	// Two messages, not one: "a" is the anchor and both "b" and "c" disagree
	// with it, so both are named. This assertion used to demand exactly one,
	// which quietly codified a bug -- the loop compared "c" against a map entry
	// that the "b" conflict had already removed, so "c" read as the first
	// destination seen and was never reported at all. What this test is about
	// is the ORDER, so it checks the first message opens with the sorted pair
	// and leaves the count to
	// TestThreeDisagreeingDestinationsAreAllWithheldAndAllNamed.
	if len(conflicts) == 0 {
		t.Fatal("three destinations, two of them disagreeing with the first, produced no conflict")
	}
	if !strings.HasPrefix(conflicts[0], `"a" and "b"`) {
		t.Errorf("conflict = %q, want it to open with %q (sorted order), not store order", conflicts[0], `"a" and "b"`)
	}
}

func TestTwoDestinationsAgreeingIsNotAConflict(t *testing.T) {
	// The rule refuses disagreement, not duplication. An install with a primary
	// and a backup destination on one account is ordinary.
	acct := int64(7)
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "main", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "backup", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
	})
	if len(conflicts) != 0 {
		t.Fatalf("identical compliance was refused: %v", conflicts)
	}
	if got[acct].Compliance.Privacy != db.PrivacyPrivate {
		t.Errorf("resolved privacy = %q, want private", got[acct].Compliance.Privacy)
	}
}

func TestADestinationWithNoComplianceContributesNothing(t *testing.T) {
	acct := int64(7)
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "configured", AccountID: &acct, Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
		{Name: "untouched", AccountID: &acct},
	})
	if len(conflicts) != 0 {
		t.Fatalf("an empty compliance was treated as disagreement: %v", conflicts)
	}
	if got[acct].Compliance.Privacy != db.PrivacyPrivate {
		t.Errorf("resolved privacy = %q, want the configured one", got[acct].Compliance.Privacy)
	}
}

func TestADestinationWithNoAccountIsIgnored(t *testing.T) {
	// A hand-typed destination has no token to push with, so it must
	// contribute nothing to the resolved map and raise no conflict --
	// there is no account for it to conflict over.
	got, conflicts := complianceByAccount([]db.Destination{
		{Name: "manual", Compliance: db.Compliance{Privacy: db.PrivacyPrivate}},
	})
	if len(got) != 0 {
		t.Errorf("got %v, want no account to receive an unowned destination's compliance", got)
	}
	if len(conflicts) != 0 {
		t.Errorf("got conflicts %v, want none: a destination with no account has nothing to conflict with", conflicts)
	}
}
