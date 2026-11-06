package publish

import (
	"slices"
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
	} {
		for _, want := range platform.Must(c.enum).PublishScopes {
			if !slices.Contains(c.scopes, want) {
				t.Errorf("%s publishing needs %q and sign-in does not ask for it", c.enum, want)
			}
		}
	}
}

// X is the recorded exception: publishing needs tweet.write and media.write,
// and the sign-in grant asks for neither. Ready tells the operator to reconnect,
// which cannot help. This test pins the gap so that closing it is a visible
// change; when oauth/x.go starts asking for them, flip it.
func TestXPublishScopesAreNotAskedForAtSignIn(t *testing.T) {
	granted := oauth.NewX().Scopes()
	for _, need := range platform.Must(platform.X).PublishScopes {
		if slices.Contains(granted, need) {
			t.Errorf("X sign-in now asks for %q: update Ready's advice and this test", need)
		}
	}
	if !slices.Equal(XScopes, platform.Must(platform.X).PublishScopes) {
		t.Errorf("XScopes = %v", XScopes)
	}
}
