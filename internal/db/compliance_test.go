package db

import (
	"strings"
	"testing"
)

func TestFacebookPrivacyIsOfferedLeastExposureFirst(t *testing.T) {
	// The safe choice must be the near one, matching PrivacyStatuses. An
	// operator scanning a list and picking the first item must not thereby
	// broadcast to everyone.
	want := []FacebookPrivacy{
		FBPrivacySelf, FBPrivacyFriends, FBPrivacyFriendsOfFriends, FBPrivacyEveryone,
	}
	if len(FacebookPrivacies) != len(want) {
		t.Fatalf("FacebookPrivacies = %v, want %v", FacebookPrivacies, want)
	}
	for i := range want {
		if FacebookPrivacies[i] != want[i] {
			t.Fatalf("FacebookPrivacies = %v, want %v", FacebookPrivacies, want)
		}
	}
}

func TestAnUnknownFacebookPrivacyIsRefusedAtSaveTime(t *testing.T) {
	c := Compliance{FacebookPrivacy: "PUBLIC"} // a YouTube word, not a Facebook one
	probs := c.Problems()
	if len(probs) == 0 {
		t.Fatal("an unknown Facebook privacy was accepted; the operator finds out " +
			"when the broadcast goes to the wrong audience")
	}
	if !strings.Contains(probs[0], "PUBLIC") {
		t.Errorf("problem %q does not name the offending value", probs[0])
	}
}

func TestComplianceWithOnlyAFacebookPrivacyIsNotEmpty(t *testing.T) {
	// Empty() gates whether a push happens at all. A Compliance carrying only
	// this field must not be mistaken for nothing to do.
	c := Compliance{FacebookPrivacy: FBPrivacySelf}
	if c.Empty() {
		t.Error("Compliance carrying a Facebook privacy reported Empty; the push " +
			"is skipped and the setting silently never applies")
	}
}
