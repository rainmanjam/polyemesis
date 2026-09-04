package routing

import (
	"strings"
	"testing"
)

// Every platform db knows about must have a decision recorded here. #713.
//
// internal/db declares eight platforms; platformPolicies carries rows for five
// of them plus `custom`. PolicyFor returns the custom row for anything
// unlisted, and that fallback is right -- guessing "exclude" would delete audio
// from a mix, which policy.go argues at length.
//
// The hazard is not the fallback. It is that NOTHING DISTINGUISHES "custom by
// choice" from "custom because this table forgot you". A platform added to db
// and not here silently loses its loudness target: meters/compliance.go reads
// pol.TargetLUFS, gets 0 from the custom row, and Evaluate returns
// VerdictUnknown -- so that platform's streams are not loudness-checked while
// YouTube's, Twitch's, Kick's and Facebook's are, and nothing says so.
//
// THE OPT-OUT LIST IS THE POINT, not a weakening of the test. Rumble, Trovo and
// Vimeo take the custom row deliberately, and internal/db says why at each
// constant: Rumble "exists for CHAT and for nothing else", Vimeo "for SIGN-IN",
// and Trovo can fetch a stream key but not the ingest URL beside it, so all
// three are pasted by hand and behave as custom destinations do. Recording that
// here turns three silent omissions into three stated decisions -- and makes a
// FOURTH omission fail, which is the part that was missing.
//
// Detection rather than Control: Control would need db to own the table, and
// the dependency runs the other way (policy.go:24).
var platformsDeliberatelyCustom = map[string]string{
	"rumble": "chat only; its ingest URL and key are pasted by hand and the destination preset does not carry it",
	"trovo":  "the key is fetchable, the ingest hostname is not; the preset carries an empty URL",
	"vimeo":  "sign-in only; the live API is Enterprise-gated, so ingest is pasted by hand",
}

// dbPlatforms mirrors internal/db's Platform constants.
//
// Duplicated rather than imported because internal/db imports internal/routing
// and the cycle is not worth breaking for a test. The drift THIS list can
// suffer is caught by the count assertion below plus db's own Validate switch,
// which enumerates the same eight.
var dbPlatforms = []string{
	"custom", "youtube", "twitch", "kick", "facebook", "rumble", "trovo", "vimeo",
}

func TestEveryPlatformHasAStatedRoutingDecision(t *testing.T) {
	if len(dbPlatforms) != 8 {
		t.Fatalf("dbPlatforms has %d entries; internal/db declares eight, so this "+
			"list has drifted and every assertion below is against the wrong set",
			len(dbPlatforms))
	}

	rowFor := map[string]bool{}
	for _, pol := range platformPolicies {
		rowFor[string(pol.Platform)] = true
	}

	for _, p := range dbPlatforms {
		hasRow := rowFor[p]
		why, optedOut := platformsDeliberatelyCustom[p]

		switch {
		case hasRow && optedOut:
			t.Errorf("%s has a policy row AND an opt-out entry saying it takes the "+
				"custom row (%q). One of the two is stale.", p, why)
		case !hasRow && !optedOut:
			t.Errorf("%s is a platform internal/db knows about with no routing policy "+
				"row and no recorded reason for taking the custom one.\n"+
				"    A destination on this platform silently gets TargetLUFS 0, so "+
				"Evaluate returns VerdictUnknown and it is not loudness-checked, while "+
				"YouTube and Twitch are.\n"+
				"    Either add a row to platformPolicies, or add an entry to "+
				"platformsDeliberatelyCustom saying why custom is right for it.", p)
		}
	}
}

// The opt-out list must not become a way to silence this test without thinking.
func TestTheCustomOptOutsExplainThemselves(t *testing.T) {
	for p, why := range platformsDeliberatelyCustom {
		if len(strings.TrimSpace(why)) < 25 {
			t.Errorf("%s opts out of a routing policy with the reason %q, which is too "+
				"short to be one. The entry exists so the decision is readable, not so "+
				"the test passes.", p, why)
		}
	}
	if len(platformsDeliberatelyCustom) > 4 {
		t.Errorf("%d platforms now opt out of having a routing policy. That is most of "+
			"them, and at that point the table is the exception rather than the rule -- "+
			"worth asking whether PolicyFor's fallback should still be silent.",
			len(platformsDeliberatelyCustom))
	}
}
