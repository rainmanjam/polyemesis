package publish

import (
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// What publishing needs is a fact in the platform registry, and what sign-in
// asks for is the provider's Scopes(). Nothing tied the two, and for X they
// have never matched.
func TestPublishScopesAreAskedForAtSignIn(t *testing.T) {
	for _, c := range []struct {
		enum   string
		scopes []string
	}{
		{platform.TikTok, oauth.NewTikTok().Scopes()},
		{platform.X, oauth.NewX().Scopes()},
	} {
		for _, want := range platform.Must(c.enum).PublishScopes {
			if !slices.Contains(c.scopes, want) {
				t.Errorf("%s publishing needs %q and sign-in does not ask for it", c.enum, want)
			}
		}
	}
}

// X used to be the exception: publishing needed tweet.write and media.write and
// the sign-in grant asked for neither, so "reconnect" could never have helped.
// Sign-in now reads the scopes from the registry, so an account that reconnects
// holds them.
func TestXSignInAsksForThePublishScopes(t *testing.T) {
	granted := oauth.NewX().Scopes()
	for _, need := range platform.Must(platform.X).PublishScopes {
		if !slices.Contains(granted, need) {
			t.Errorf("X publishing needs %q and sign-in does not ask for it", need)
		}
	}
	if !slices.Equal(XScopes, platform.Must(platform.X).PublishScopes) {
		t.Errorf("XScopes = %v", XScopes)
	}
}

// YouTube needs any one of four scopes, and the sign-in already asks for one of
// them, which is why an account connected before clip publishing needs no
// reconnect.
func TestYouTubeSignInSatisfiesPublishing(t *testing.T) {
	if !HasAnyScope(joinScopes(oauth.NewYouTube().Scopes()), AcceptedYouTubeScopes) {
		t.Errorf("YouTube sign-in scopes %v satisfy none of %v", oauth.NewYouTube().Scopes(), AcceptedYouTubeScopes)
	}
}

func joinScopes(s []string) string { return strings.Join(s, " ") }
