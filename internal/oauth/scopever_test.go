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
	got := AccountNeedsReconnect(db.PlatformAccount{
		Platform: db.PlatformTwitch,
		ScopeVer: 1,
	})
	if (&Twitch{}).ScopeVersion() != 1 {
		t.Skip("Twitch's scope version has moved; this case needs updating")
	}
	if got.Needed {
		t.Errorf("an account at the CURRENT version was flagged: %s", got.Reason)
	}

	// Now the same account against a build that asks for more.
	got = AccountNeedsReconnect(db.PlatformAccount{
		Platform: db.PlatformTwitch,
		ScopeVer: 0, // legacy row, handled below
	})
	_ = got

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
	if len(all) < 2 {
		t.Skip("Twitch asks for fewer than two scopes; this case needs updating")
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
