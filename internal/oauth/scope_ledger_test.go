package oauth

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

/* AN APPEND-ONLY LEDGER OF WHAT EACH SCOPE VERSION MEANT, BECAUSE THE GOLDEN
 * CANNOT CATCH THE MISTAKE IT LOOKS LIKE IT CATCHES.
 *
 * The rule is stated at oauth.go's Provider interface and restated in x.go:
 * "ScopeVersion is bumped BY HAND whenever Scopes changes... The pairing is the
 * only thing that makes forgetting to bump it visible in review."
 *
 * That comment names its own enforcement mechanism: REVIEW. And review is what
 * it has, because TestProviderGolden records {platform, scopeVersion, scopes}
 * together and the documented way to discharge its failure is
 * `-update-oauth-golden`, WHICH REWRITES BOTH FIELDS AT ONCE. Add a scope, run
 * the update flag, commit: green, with the version unchanged and no diff
 * anywhere that says so.
 *
 * WHAT THAT COSTS AN OPERATOR, precisely. scopever.go's AccountNeedsReconnect
 * judges on the stored version and only falls back to a scope diff for legacy
 * rows where ScopeVer is 0. A scope added without a bump therefore reports
 * "fine" for every already-connected account. The operator's token cannot do
 * the new thing, nothing says so, and in scopever.go's own words they "find out
 * from a 401 during a broadcast". That is the exact failure the whole mechanism
 * exists to prevent, and capabilities.go's note about Kick's streamkey:read is
 * this repository's record of it having already happened once.
 *
 * HOW THIS FILE IS DIFFERENT FROM THE GOLDEN, WHICH IS THE ENTIRE POINT:
 *
 *   THERE IS NO -update FLAG. testdata/scope-versions.json is edited BY HAND.
 *   A tool that rewrote it would reintroduce exactly the laundering this
 *   exists to stop, so if you are reading this because you want to add one:
 *   that is the bug, not the friction.
 *
 * The mechanism is a collision. Each entry freezes what one (platform,
 * version) pair MEANT. Change Scopes() without bumping and the live set stops
 * matching the frozen entry for the live version -- and the only edit that
 * makes the test pass again is a NEW row with a NEW version, which is the bump.
 * Bump without changing scopes and it also fails, because the new version has
 * no entry; that is a real question worth being asked, since a bump forces
 * every connected account to reconnect and one made by accident is a cost paid
 * by every user for nothing.
 */

const scopeLedgerPath = "testdata/scope-versions.json"

type scopeLedgerEntry struct {
	Platform     string   `json:"platform"`
	ScopeVersion int      `json:"scopeVersion"`
	Scopes       []string `json:"scopes"`
}

func readScopeLedger(t *testing.T) []scopeLedgerEntry {
	t.Helper()
	raw, err := os.ReadFile(scopeLedgerPath)
	if err != nil {
		t.Fatalf("read %s: %v", scopeLedgerPath, err)
	}
	var out []scopeLedgerEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse %s: %v", scopeLedgerPath, err)
	}
	return out
}

// TestEveryLiveScopeSetMatchesItsFrozenLedgerEntry is the collision described
// above.
func TestEveryLiveScopeSetMatchesItsFrozenLedgerEntry(t *testing.T) {
	ledger := readScopeLedger(t)
	byKey := map[string]scopeLedgerEntry{}
	for _, e := range ledger {
		byKey[e.Platform+"/"+itoa(e.ScopeVersion)] = e
	}

	for _, p := range Providers() {
		plat := string(p.Platform())
		ver := p.ScopeVersion()
		t.Run(plat, func(t *testing.T) {
			entry, ok := byKey[plat+"/"+itoa(ver)]
			if !ok {
				t.Fatalf("%s is at ScopeVersion %d and the ledger has no entry for it.\n\n"+
					"If you just BUMPED the version: add a row to %s recording what %d "+
					"means, with the scopes it now requests. That row is the record an "+
					"operator's reconnect prompt is derived from.\n\n"+
					"If you did NOT bump it deliberately: a bump forces every connected "+
					"%s account to reconnect. One made by accident is a cost paid by "+
					"every user of this install for nothing.",
					plat, ver, scopeLedgerPath, ver, plat)
			}

			live := append([]string(nil), p.Scopes()...)
			sort.Strings(live)
			frozen := append([]string(nil), entry.Scopes...)
			sort.Strings(frozen)

			if !equalStrings(live, frozen) {
				t.Fatalf("%s requests a different scope set than ScopeVersion %d was "+
					"recorded as meaning.\n  live:   %v\n  frozen: %v\n\n"+
					"THE FIX IS ALMOST CERTAINLY A VERSION BUMP, NOT AN EDIT TO THE "+
					"LEDGER. Editing the frozen row rewrites what a version already "+
					"handed out meant, which is what this file exists to prevent: "+
					"AccountNeedsReconnect judges on the stored version, so every "+
					"already-connected account would keep reporting fine while holding "+
					"a token that cannot do the new thing -- and would find out from a "+
					"401 mid-broadcast.\n\n"+
					"Bump %s's ScopeVersion to %d and append a row for it.",
					plat, ver, live, frozen, plat, ver+1)
			}
		})
	}
}

// TestTheScopeLedgerIsWellFormed keeps the ledger itself honest: one row per
// (platform, version), no gaps that would hide a deleted record, and no entry
// for a platform that no longer exists.
func TestTheScopeLedgerIsWellFormed(t *testing.T) {
	ledger := readScopeLedger(t)
	if len(ledger) == 0 {
		t.Fatal("the ledger is empty; every provider's history has been erased")
	}

	seen := map[string]bool{}
	highest := map[string]int{}
	for _, e := range ledger {
		key := e.Platform + "/" + itoa(e.ScopeVersion)
		if seen[key] {
			t.Errorf("two entries for %s — one of them is a rewrite of history "+
				"wearing an append", key)
		}
		seen[key] = true
		if len(e.Scopes) == 0 {
			t.Errorf("%s records no scopes; an empty set is not a version, it is a "+
				"row nobody filled in", key)
		}
		if e.ScopeVersion < 1 {
			t.Errorf("%s has a version below 1; scopever.go treats 0 as \"legacy, "+
				"diff the scopes instead\", so it cannot also mean a real version", key)
		}
		if e.ScopeVersion > highest[e.Platform] {
			highest[e.Platform] = e.ScopeVersion
		}
	}

	// A platform's rows must have no HOLES above the version this ledger starts
	// at. A missing middle version is either a deleted record or a bump nobody
	// documented, and both are the thing this file is for.
	//
	// FROM ITS GENESIS, NOT FROM 1, AND THE REASON IS THAT THE HISTORY DOES NOT
	// EXIST. This ledger was seeded from testdata/provider-scopes.json, which was
	// itself created (9714b77) already holding kick v2 and twitch v4. Nothing in
	// this repository ever recorded what kick v1 or twitch v1-3 requested; git
	// history for the golden begins after those bumps had happened. Writing rows
	// for them would mean inventing scope sets and freezing the invention as
	// evidence, which is worse than an honest gap -- the whole value here is that
	// a frozen row is trustworthy.
	//
	// So the floor is per platform, and it moves only forward. Every bump made
	// from now on must be recorded, and a hole punched later still fails.
	genesis := map[string]int{}
	for _, e := range ledger {
		if cur, ok := genesis[e.Platform]; !ok || e.ScopeVersion < cur {
			genesis[e.Platform] = e.ScopeVersion
		}
	}
	for plat, top := range highest {
		for v := genesis[plat]; v <= top; v++ {
			if !seen[plat+"/"+itoa(v)] {
				t.Errorf("%s records versions %d and %d but not %d — a version handed "+
					"to real accounts and then unrecorded", plat, genesis[plat], top, v)
			}
		}
	}

	// Nothing in the ledger may name a platform that has no provider: a stale
	// row cannot fail the live check above, so it would sit here reading as
	// history while describing nothing.
	live := map[string]bool{}
	for _, p := range Providers() {
		live[string(p.Platform())] = true
	}
	for plat := range highest {
		if !live[plat] {
			t.Errorf("the ledger records %q, which is not a registered provider", plat)
		}
	}
}

// TestTheLedgerIsNotRegeneratedByAnyFlag pins the property that makes this file
// worth having.
//
// The golden's -update-oauth-golden rewrites provider-scopes.json from the live
// providers, and if it ever learned to rewrite this file too, a forgotten bump
// would become invisible again by exactly the route it is invisible today. This
// asserts the updater does not know this path.
func TestTheLedgerIsNotRegeneratedByAnyFlag(t *testing.T) {
	src, err := os.ReadFile("provider_golden_test.go")
	if err != nil {
		t.Fatalf("read provider_golden_test.go: %v", err)
	}
	if containsSubstring(string(src), scopeLedgerPath) {
		t.Fatalf("provider_golden_test.go names %s. If the golden updater can rewrite "+
			"the ledger, forgetting a ScopeVersion bump is laundered by the same "+
			"command that was supposed to surface it — which is the defect this "+
			"whole file exists to close.", scopeLedgerPath)
	}
}

// --- small local helpers, kept here so this file reads without hunting ---

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsSubstring(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// Referenced so the db import is real: Platform() returns db.Platform, and a
// future refactor that changed it should fail here rather than silently
// comparing something else.
var _ db.Platform = db.PlatformTwitch
