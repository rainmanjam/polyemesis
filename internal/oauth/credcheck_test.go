package oauth

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// NOTE: the whole-registry categorisation guard
// (TestEveryProviderIsEitherCheckableOrDeclaredUnverifiable) lives in Task 2,
// not here. It cannot pass until Twitch, Kick and Facebook have their methods,
// and committing a knowingly-red test would leave one commit on this branch
// that does not build green -- breaking git bisect for no benefit, since Task 2
// follows immediately. The guard still lands before any code depends on it.

// A provider declared unverifiable must say WHY. The reason is rendered to the
// operator as the explanation for an "unverified" badge, so an empty one is a
// blank space in the UI.
func TestUnverifiableProvidersAllCarryAReason(t *testing.T) {
	for platform, reason := range unverifiableProviders {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s is declared unverifiable with an empty reason; the reason "+
				"is the point -- it is what the UI shows instead of a verdict", platform)
		}
	}
}

func TestUnverifiableProvidersOnlyNamesRegisteredPlatforms(t *testing.T) {
	for platform := range unverifiableProviders {
		if _, ok := Providers()[platform]; !ok {
			t.Errorf("unverifiableProviders names %q, which is not a registered provider", platform)
		}
	}
}

func TestYouTubeIsTheDeclaredUnverifiableOne(t *testing.T) {
	// Pins the specific fact the UI depends on. If Google ever ships a
	// credential-check endpoint, this failing is the prompt to implement it.
	if _, ok := unverifiableProviders[db.PlatformYouTube]; !ok {
		t.Fatal("YouTube is expected to be unverifiable: Google offers no way to " +
			"validate a client ID/secret pair without a user consent round-trip")
	}
}
