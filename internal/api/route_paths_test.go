package api

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// The URLs the server hands out are wire values; these are the bytes the
// hand-built strings produced before the builders existed.
func TestRoutePathBuildersProduceTheirWireValues(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{oauthStartPath("youtube"), "/api/v1/oauth/youtube/start"},
		{oauth.CallbackPath("youtube"), apiBase + strings.Replace(routeOAuthCallback, "{platform}", "youtube", 1)},
		{kickWebhookPath("s3cret"), "/api/v1/chat/kick/s3cret"},
		{libraryMediaBase(42), "/api/v1/library/recordings/42/media/"},
		{settingsRedirect([2]string{oauthErrorKey, "x%20y"}), "/settings?tab=platforms&oauth_error=x%20y"},
		{settingsRedirect([2]string{oauthOKKey, "a"}, [2]string{oauthWarnKey, "b"}), "/settings?tab=platforms&oauth_ok=a&oauth_warn=b"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// Each builder is for a route the router serves, at the pattern it builds
// from: registering a different one fails here.
func TestRoutePathBuildersNameRegisteredRoutes(t *testing.T) {
	s, _, _ := zeroSourceServer(t)
	enumerated, _ := enumerateRoutes(t, s)
	have := map[string]bool{}
	for _, r := range enumerated {
		have[r.Pattern] = true
	}
	for _, p := range []string{routeOAuthStart, routeOAuthCallback, routeKickWebhook, routeLibraryMedia} {
		if !have[apiBase+p] {
			t.Errorf("no route registered at %s", apiBase+p)
		}
	}
}
