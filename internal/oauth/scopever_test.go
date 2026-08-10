package oauth

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The ordinary case: an account connected by this build is current.
func TestAnAccountConnectedNowNeedsNothing(t *testing.T) {
	for p, prov := range Providers() {
		got := AccountNeedsReconnect(db.PlatformAccount{
			Platform: p,
			ScopeVer: prov.ScopeVersion(),
			Scopes:   strings.Join(prov.Scopes(), " "),
		})
		if got.Needed {
			t.Errorf("%s: a freshly connected account was told to reconnect: %s", p, got.Reason)
		}
	}
}

// The case the whole mechanism exists for.
func TestATokenIssuedBeforeAScopeWasAddedIsFlagged(t *testing.T) {
	// THIS SKIP WAS FIRING ON MAIN. It read
	//
	//	if (&Twitch{}).ScopeVersion() != 1 { t.Skip("...needs updating") }
	//
	// and Twitch has been at 4 for some time, so the test whose own comment
	// calls it "the case the whole mechanism exists for" has been passing by
	// declining to run. That is the worst species of skip: it fires BECAUSE
	// the thing under test changed, which is precisely the moment somebody
	// needed to look.
	//
	// The version is pinned in testdata/provider-scopes.json now, and drift
	// FAILS TestProviderGolden with a diff. Here the case is written against
	// whatever the current version is, so it can never go stale again.
	cur := (&Twitch{}).ScopeVersion()
	got := AccountNeedsReconnect(db.PlatformAccount{
		Platform: db.PlatformTwitch,
		ScopeVer: cur,
		Scopes:   strings.Join((&Twitch{}).Scopes(), " "),
	})
	if got.Needed {
		t.Errorf("an account at the CURRENT scope version (%d) holding every current "+
			"scope was flagged: %s", cur, got.Reason)
	}

	// AND THE CASE THE MECHANISM ACTUALLY EXISTS FOR, which nothing in this
	// function asserted while the skip was live: a token minted at an EARLIER
	// scope version must be flagged. The old code built exactly this account,
	// then skipped before looking at it, then rebuilt it at version 0 and threw
	// the result away with `_ = got`. Three lines that computed the answer and
	// never asked it.
	if cur < 2 {
		t.Fatalf("Twitch is at scope version %d, so there is no earlier version to "+
			"test against. Bumping past 1 is what this case is for; a build that has "+
			"not is a build where this assertion needs rewriting, not switching off.", cur)
	}
	stale := AccountNeedsReconnect(db.PlatformAccount{
		Platform: db.PlatformTwitch,
		ScopeVer: cur - 1,
		Scopes:   strings.Join((&Twitch{}).Scopes(), " "),
	})
	if !stale.Needed {
		t.Errorf("an account minted at scope version %d was NOT told to reconnect "+
			"against a build at %d. This is the whole mechanism: a granted consent is "+
			"not upgraded by the server asking for more later.", cur-1, cur)
	}

	// A stored version BELOW the provider's current one is the direct case.
	// Simulated by claiming a version the provider has not reached going
	// backwards is impossible, so this asserts the comparison itself.
	if !scopeVerStale(1, 2) {
		t.Error("version 1 against a current 2 did not read as stale")
	}
	if scopeVerStale(2, 2) {
		t.Error("an equal version read as stale")
	}
	// A version AHEAD of the build is not stale. It means the operator
	// downgraded polyemesis, and telling them to reconnect would not help.
	if scopeVerStale(3, 2) {
		t.Error("a version ahead of the build read as stale, which a reconnect cannot fix")
	}
}

// Legacy rows -- version 0 -- must NOT all be accused.
//
// This is the migration's whole design point: bumping every stored account to
// a stale version would show a reconnect prompt on every account an operator
// has, including ones connected yesterday with the full set, and a prompt that
// is wrong the first time is a prompt nobody reads the second time.
func TestALegacyRowWithEveryScopeIsLeftAlone(t *testing.T) {
	tw := &Twitch{}
	got := AccountNeedsReconnect(db.PlatformAccount{
		Platform: db.PlatformTwitch,
		ScopeVer: 0,
		Scopes:   strings.Join(tw.Scopes(), " "),
	})
	if got.Needed {
		t.Errorf("a legacy account holding every current scope was told to reconnect: %s", got.Reason)
	}
}

// And a legacy row that genuinely lacks a scope IS flagged, naming which.
func TestALegacyRowMissingAScopeIsFlaggedWithTheScopeNamed(t *testing.T) {
	tw := &Twitch{}
	all := tw.Scopes()
	// Was a t.Skip. A test whose subject is "a legacy row missing ONE of
	// several scopes" cannot be satisfied by declining to run when there is
	// only one scope: that is the configuration in which the case it covers
	// has silently stopped existing, and it should say so.
	if len(all) < 2 {
		t.Fatalf("Twitch asks for %d scope(s), and this case needs at least two to "+
			"remove one and still have a list. The scope list shrinking that far is a "+
			"real change -- see testdata/provider-scopes.json -- and this test must be "+
			"rewritten for it rather than switched off by it.", len(all))
	}
	// Everything except the first.
	got := AccountNeedsReconnect(db.PlatformAccount{
		Platform: db.PlatformTwitch,
		ScopeVer: 0,
		Scopes:   strings.Join(all[1:], " "),
	})
	if !got.Needed {
		t.Fatal("a legacy account missing a scope was not flagged")
	}
	if len(got.Missing) != 1 || got.Missing[0] != all[0] {
		t.Errorf("Missing = %v, want exactly %q so the operator can see what changed", got.Missing, all[0])
	}
	if !strings.Contains(got.Reason, "never upgrades") {
		t.Errorf("the reason does not explain WHY reconnecting is required: %q", got.Reason)
	}
}

// A platform that returns no scope list at all must not be reported as broken
// forever. Several simply do not echo the grant back.
func TestAnEmptyGrantedScopeListIsNotAVerdict(t *testing.T) {
	got := AccountNeedsReconnect(db.PlatformAccount{
		Platform: db.PlatformTwitch,
		ScopeVer: 0,
		Scopes:   "",
	})
	if got.Needed {
		t.Errorf("an account whose platform returned no scope list was flagged: %s", got.Reason)
	}
}

// Platforms delimit the granted list differently, and none of them agreed to
// a standard. A parser that only handled spaces would flag every account on a
// comma-separated platform.
func TestGrantedScopesParseWhateverTheSeparatorIs(t *testing.T) {
	want := []string{"a", "b", "c"}
	for _, granted := range []string{"a b c", "a,b,c", "a+b+c", "a, b,  c", " a b c "} {
		if m := missingScopes(granted, want); len(m) != 0 {
			t.Errorf("granted %q reported %v as missing", granted, m)
		}
	}
	if m := missingScopes("a b", want); len(m) != 1 || m[0] != "c" {
		t.Errorf("missingScopes(\"a b\") = %v, want [c]", m)
	}
}

// An account for a platform this build no longer supports yields no verdict.
// That is a different problem and reconnecting does not fix it.
func TestAnUnknownPlatformYieldsNoVerdict(t *testing.T) {
	got := AccountNeedsReconnect(db.PlatformAccount{Platform: "myspace", ScopeVer: 0})
	if got.Needed {
		t.Errorf("an unsupported platform was told to reconnect: %s", got.Reason)
	}
}

// Every provider must declare a POSITIVE version. Zero would make every
// account it ever connects look like a legacy row and silently disable the
// mechanism for that platform.
func TestEveryProviderDeclaresAPositiveScopeVersion(t *testing.T) {
	for p, prov := range Providers() {
		if prov.ScopeVersion() < 1 {
			t.Errorf("%s declares ScopeVersion %d; it must be >= 1 or the mechanism "+
				"never fires for that platform", p, prov.ScopeVersion())
		}
		if len(prov.Scopes()) == 0 {
			t.Errorf("%s requests no scopes at all", p)
		}
	}
}
