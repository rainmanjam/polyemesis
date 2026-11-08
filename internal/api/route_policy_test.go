package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rainmanjam/polyemesis/internal/config"
)

func registeredPolicy(t *testing.T) *routePolicy {
	t.Helper()
	s, _, _ := testServer(t, config.Config{})
	p := s.currentPolicy()
	if p == nil {
		t.Fatal("registerRoutes published no policy")
	}
	return p
}

// readDeniedPatterns is the set of patterns a read-scoped token is refused,
// derived from the registrations.
func readDeniedPatterns(t *testing.T) map[string]bool {
	t.Helper()
	return registeredPolicy(t).patterns(readDenied)
}

func patternList(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

// The policy is declared on the registrations, so what the middleware consults
// is exactly the routes that were flagged. These are the sets the parallel
// tables held before the flags replaced them.
//
// Mutation: drop a flag from its registration in registerRoutes, or flag a
// route that should not be.
func TestRoutePolicyIsWhatWasRegistered(t *testing.T) {
	p := registeredPolicy(t)

	if got, want := patternList(p.patterns(readWrite)), []string{
		"/api/v1/routing/compile", "/api/v1/version/check",
	}; !equalStrings(got, want) {
		t.Errorf("readWrite = %v, want %v", got, want)
	}
	if got, want := patternList(p.patterns(obsScope)), []string{"/api/v1/obs/events"}; !equalStrings(got, want) {
		t.Errorf("obsScope = %v, want %v", got, want)
	}
	if !p.route(http.MethodPost, "/api/v1/obs/events", obsScope) || p.route(http.MethodGet, "/api/v1/obs/events", obsScope) {
		t.Error("the obs scope is keyed by method and pattern: POST only")
	}
	if got, want := patternList(p.patterns(logSkip)), []string{
		"/api/v1/health", "/api/v1/metrics", "/hls/*", "/hls/{source:[0-9]+}/*", "/playout/*",
	}; !equalStrings(got, want) {
		t.Errorf("logSkip = %v, want %v", got, want)
	}
	if got, want := patternList(p.patterns(logSkipUnless5xx)), []string{"/api/v1/playout/beacon"}; !equalStrings(got, want) {
		t.Errorf("logSkipUnless5xx = %v, want %v", got, want)
	}
	if got, want := patternList(p.patterns(secretInPath)), []string{"/watch/*"}; !equalStrings(got, want) {
		t.Errorf("secretInPath = %v, want %v", got, want)
	}
	if got, want := patternList(p.patterns(readDenied)), []string{
		"/api/v1/activity",
		"/api/v1/activity/sources",
		"/api/v1/analytics/live",
		"/api/v1/analytics/streams",
		"/api/v1/analytics/streams/{id}",
		"/api/v1/chat",
		"/api/v1/chat/export",
		"/api/v1/chat/flags",
		"/api/v1/chat/messages",
		"/api/v1/chat/moderators",
		"/api/v1/chat/mutes",
		"/api/v1/chat/search",
		"/api/v1/chat/settings",
		"/api/v1/chat/twitch/{resource}",
		"/api/v1/chat/users",
		"/api/v1/chat/users/notes",
		"/api/v1/clipper/jobs/{id}/download",
		"/api/v1/clipper/recordings/{id}/highlights",
		"/api/v1/clipper/recordings/{id}/keyframes",
		"/api/v1/clipper/recordings/{id}/transcript",
		"/api/v1/clipper/templates",
		"/api/v1/clips/{name}/download",
		"/api/v1/destinations/{id}/expert",
		"/api/v1/destinations/{id}/expert/preview",
		"/api/v1/destinations/{id}/facebook/end-broadcast",
		"/api/v1/destinations/{id}/facebook/stream-health",
		"/api/v1/destinations/{id}/youtube-vertical",
		"/api/v1/library/recordings/{id}/media/{file}",
		"/api/v1/library/recordings/{id}/transcript",
		"/api/v1/library/recordings/{id}/transcript/export",
		"/api/v1/library/search",
		"/api/v1/metadata/broadcast-window",
		"/api/v1/platforms/accounts/{id}/stats",
		"/api/v1/recordings/stems/{name}/download",
		"/api/v1/recordings/{id}/chat",
		"/api/v1/recordings/{id}/download",
	}; !equalStrings(got, want) {
		t.Errorf("readDenied = %v\nwant %v\n(docs/API.md's refused-route table says the same)", got, want)
	}
}

// Every recorded pattern is a route chi serves under that exact name, so a
// wrong prefix at a registration (the policy would then guard nothing) fails.
func TestEveryPolicyPatternIsAServedRoute(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	served := map[string]bool{}
	err := chi.Walk(s.Handler().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// By pattern: a catch-all Handle is recorded under each method but chi
		// may report it as one entry, so the pattern is what must be served.
		served[strings.TrimSuffix(route, "/")] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range s.currentPolicy().byRoute {
		_, pattern, _ := strings.Cut(key, " ")
		if !served[pattern] {
			t.Errorf("the policy names %q, which the router does not serve", key)
		}
	}
}
