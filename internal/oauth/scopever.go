package oauth

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Whether a connected account still holds the permissions this build needs.
//
// An OAuth token carries exactly the scopes it was issued with, and adding a
// scope to the application does NOT upgrade a token somebody already holds. So
// an operator who connected before a feature shipped silently lacks permission
// for it, and finds out from a 401 in the middle of a broadcast.
//
// polyemesis previously handled this with a line in the documentation, in two
// places. This turns it into something the UI can say.

// ReconnectReason explains why an account should be reconnected, or is empty
// when it is fine.
type ReconnectReason struct {
	Needed bool   `json:"needed"`
	Reason string `json:"reason,omitempty"`
	// Missing names the scopes the stored grant lacks, when that is how the
	// verdict was reached. Empty when the verdict came from the version.
	Missing []string `json:"missing,omitempty"`
}

// AccountNeedsReconnect compares a stored account against what its provider
// now asks for.
//
// Two mechanisms, and the ORDER matters:
//
//  1. The version, when the account has one. Authoritative, cheap, and immune
//     to platforms renaming scopes or granting supersets.
//
//  2. A scope comparison, ONLY for accounts stored with version 0 -- rows that
//     predate the column. Those cannot be judged by version without accusing
//     every account an operator has, including ones connected yesterday with
//     the full set. This is the one place a scope diff is the right tool: it
//     runs once per legacy row, and a false positive costs a single needless
//     reconnect rather than a recurring prompt.
//
// A provider that is not registered yields no verdict rather than a scary one:
// an account for a platform this build no longer supports is a different
// problem, and not one a reconnect fixes.
func AccountNeedsReconnect(a db.PlatformAccount) ReconnectReason {
	p, err := Get(a.Platform)
	if err != nil {
		return ReconnectReason{}
	}
	current := p.ScopeVersion()

	if a.ScopeVer > 0 {
		if !scopeVerStale(a.ScopeVer, current) {
			return ReconnectReason{}
		}
		return ReconnectReason{
			Needed: true,
			Reason: fmt.Sprintf("this %s account was connected when polyemesis asked for fewer "+
				"permissions than it does now. Granting a scope never upgrades a token that "+
				"already exists, so it has to be reconnected once.", a.Platform) + reconnectScope(a.Platform),
		}
	}

	// Version 0: a row from before this existed. Judge it on what was granted.
	missing := missingScopes(a.Scopes, p.Scopes())
	if len(missing) == 0 {
		return ReconnectReason{}
	}
	return ReconnectReason{
		Needed: true,
		Reason: fmt.Sprintf("this %s account is missing %d permission(s) polyemesis now needs. "+
			"Granting a scope never upgrades a token that already exists, so it has to be "+
			"reconnected once.", a.Platform, len(missing)) + reconnectScope(a.Platform),
		Missing: missing,
	}
}

// scopeVerStale reports whether a stored version predates the build's.
//
// Strictly less-than, so a stored version AHEAD of the build is not stale.
// That happens when an operator downgrades polyemesis, and telling them to
// reconnect would not help: the token already carries more than this build
// asks for. Reporting it would be a prompt that cannot be satisfied.
func scopeVerStale(stored, current int) bool { return stored < current }

// missingScopes reports which of want are absent from granted.
//
// granted is whatever the platform handed back, which is not a format anyone
// standardised: space separated, comma separated, or absent entirely. An EMPTY
// granted string yields no verdict rather than "everything is missing" --
// several platforms simply do not return the list, and reporting those as
// broken would flag every account on them forever.
func missingScopes(granted string, want []string) []string {
	fields := strings.FieldsFunc(granted, func(r rune) bool {
		return r == ' ' || r == ',' || r == '+'
	})
	if len(fields) == 0 {
		return nil
	}
	have := make(map[string]bool, len(fields))
	for _, f := range fields {
		have[strings.TrimSpace(f)] = true
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	return missing
}

// ScopeNoter is implemented by providers that say what a ScopeVersion bump
// unlocks, as UI i18n keys, so the console can name it in a "reconnect to
// enable" banner. The map sits next to the provider's ScopeVersion on purpose:
// a bump without a note fails TestEveryBumpedScopeVersionHasAnUnlockNote. An
// empty value is an explicit "no banner for this bump".
type ScopeNoter interface {
	ScopeUnlocks() map[int]string
}

// ScopeUnlocksSince lists the i18n keys for every bump between the version an
// account was granted under and the provider's current one, oldest first.
// A granted version of 0 (a row from before the column) yields none: that row
// is judged on its scopes and is told to reconnect in Settings.
func ScopeUnlocksSince(p Provider, granted int) []string {
	n, ok := p.(ScopeNoter)
	if !ok || granted <= 0 {
		return nil
	}
	notes := n.ScopeUnlocks()
	var vers []int
	for v, key := range notes {
		if key != "" && v > granted && v <= p.ScopeVersion() {
			vers = append(vers, v)
		}
	}
	sort.Ints(vers)
	out := make([]string, 0, len(vers))
	for _, v := range vers {
		out = append(out, notes[v])
	}
	return out
}

// reconnectScope says what a reconnect is for, where it is for one thing only.
// X's scope version 2 added the two permissions that post clips; livestream and
// chat work on the older grant, and a livestream-only operator should not read
// the prompt as a fault.
func reconnectScope(p db.Platform) string {
	if p == db.PlatformX {
		return " Reconnecting only matters for posting clips and follow/subscription alerts on X; livestreaming and chat are unaffected."
	}
	return ""
}
