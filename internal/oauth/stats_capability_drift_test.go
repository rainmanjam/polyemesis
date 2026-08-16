package oauth

import "testing"

// THE MATRIX AND THE CODE COULD DISAGREE FOREVER AND NOTHING WOULD SAY SO.
//
// Four surfaces describe what a platform can do -- capabilities.go,
// ui/src/lib/capabilities.ts, docs/PLATFORMS.md, and the drift tests binding
// them -- and every one of them compares a DOCUMENT to another DOCUMENT. None
// compares any of them to the provider code. So when Twitch's Stats method
// landed, all four kept saying Twitch could not report viewers, all four
// remained internally consistent, every drift test stayed green, and the HTTP
// endpoint meanwhile answered with a real viewer count. The capability was
// implemented, tested, reachable, and invisible.
//
// The failure runs in both directions and the second is worse. A cell that says
// "Works" for a platform whose provider has no Stats method is a promise the
// UI renders and the API refuses: internal/api/oauth_handlers.go resolves the
// capability through StatsFor -- a type assertion on the provider, NOT a
// lookup in this matrix -- so it answers supported:false while the matrix
// beside it says Works. An operator reads the matrix, picks the platform for
// that reason, and finds the number never appears.
//
// This test is the join nobody wrote. It is deliberately a biconditional: the
// method and the cell are two spellings of one fact, and either without the
// other is a bug rather than a state worth allowing.
func TestTheViewerStatsCellAgreesWithWhichProvidersActuallyImplementStats(t *testing.T) {
	for _, row := range PlatformCapabilities() {
		row := row
		t.Run(row.PresetID, func(t *testing.T) {
			_, implemented := StatsFor(row.Platform)
			claimed := row.Caps[CapViewerStats] == SupportYes

			switch {
			case implemented && !claimed:
				t.Errorf("%s implements Stats but its viewerStats cell is %q.\n"+
					"The endpoint will report a real viewer count while the capability matrix, "+
					"ui/src/lib/capabilities.ts and docs/PLATFORMS.md all tell the operator it cannot. "+
					"Set the cell to SupportYes and mirror it into the other three files.",
					row.PresetID, row.Caps[CapViewerStats])
			case claimed && !implemented:
				t.Errorf("%s claims viewerStats works, but its provider has no Stats method "+
					"satisfying LiveStatter.\n"+
					"internal/api/oauth_handlers.go resolves this through StatsFor, so it will answer "+
					"supported:false to an operator the matrix told to expect a viewer count. "+
					"Either implement Stats or take the claim back.",
					row.PresetID)
			}
		})
	}
}

// The Set twin has to agree too, for the reason endpoints.go gives: a Server
// built with a stubbed Set must not fall through to a production provider for
// viewer numbers alone. A capability lookup that resolves through the package
// singleton but not through an explicitly-constructed Set is the shape of that
// bug, and it is invisible until a test double silently talks to the internet.
func TestStatsForAndItsSetTwinResolveTheSamePlatforms(t *testing.T) {
	set := NewSet()
	for _, row := range PlatformCapabilities() {
		_, viaPackage := StatsFor(row.Platform)
		_, viaSet := set.StatsFor(row.Platform)
		if viaPackage != viaSet {
			t.Errorf("%s: StatsFor says %v, Set.StatsFor says %v -- the two lookups must not disagree",
				row.PresetID, viaPackage, viaSet)
		}
	}
}
