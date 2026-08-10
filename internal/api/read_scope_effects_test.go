package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The recurrence guard for #150's second finding: GETs THAT ARE NOT READS.
//
// The disclosure half of the fix (read_scope_leak_test.go) asks what a response
// contains. This half asks what making the request DOES. They are two different
// failures of the same root cause -- a rule shaped by the HTTP verb cannot see
// either one -- and they need separate guards because a route can pass one
// while failing the other.
//
// Every assertion here drives the real router. The denials are asserted as the
// 403 a client actually receives, and each is paired with the same route
// reaching a principal that IS entitled to it, so a change that simply broke
// the route for everybody cannot pass this file.

// notReadsForReadTokens is the classification, restated as behaviour.
//
// Two reasons appear here and it is worth keeping them distinct, because they
// call for the same remedy for different causes:
//
//	THE RESPONSE IS A CREDENTIAL -- the expert routes return the resolved argv,
//	whose output target is rtmps://host/app/<streamKey>. Denial rather than
//	masking, because expert mode's contract is that the command shown is the
//	command that runs.
//
//	THE REQUEST DOES WORK -- keyframes spawns ffprobe; the account-stats and
//	broadcast-window routes call a third party and can refresh AND PERSIST an
//	OAuth token, so they are GETs that write the database and spend somebody
//	else's quota.
func TestReadTokenIsDeniedTheRoutesThatAreNotReads(t *testing.T) {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

	cases := []struct {
		method string
		path   string
		body   any
		why    string
	}{
		{http.MethodGet, "/api/v1/destinations/1/expert", nil,
			"the resolved argv carries the destination stream key"},
		{http.MethodPost, "/api/v1/destinations/1/expert/preview",
			map[string]any{"inputArgs": "", "outputArgs": ""},
			"the same argv, through a POST the read-scope allowlist used to permit"},
		{http.MethodGet, "/api/v1/clipper/recordings/1/keyframes?fromMs=0&toMs=1000", nil,
			"spawns one ffprobe per overlapping timeline part"},
		{http.MethodGet, "/api/v1/platforms/accounts/1/stats", nil,
			"calls a third party and can refresh-and-persist an OAuth token"},
		{http.MethodGet, "/api/v1/metadata/broadcast-window", nil,
			"one third-party call per connected account, each able to persist a refresh"},

		// #154: content, not metadata. Media bytes.
		{http.MethodGet, "/api/v1/recordings/1/download", nil,
			"the recording itself"},
		{http.MethodGet, "/api/v1/recordings/stems/stem.wav/download", nil,
			"a separated audio stem of the recording"},
		{http.MethodGet, "/api/v1/clips/clip.mp4/download", nil,
			"an exported clip"},
		{http.MethodGet, "/api/v1/clipper/jobs/1/download", nil,
			"the clipper's export output"},
		{http.MethodGet, "/api/v1/library/recordings/1/media/take.mkv", nil,
			"a media file inside a library recording"},

		// #154: content, not metadata. The words.
		{http.MethodGet, "/api/v1/clipper/recordings/1/transcript", nil,
			"the verbatim transcript"},
		{http.MethodGet, "/api/v1/library/recordings/1/transcript", nil,
			"the verbatim transcript, by the library's route to it"},
		{http.MethodGet, "/api/v1/library/search?q=the", nil,
			"db.TranscriptHit carries Text, Context and Speaker, so iterating common " +
				"words reconstructs the transcript without naming a /transcript route"},
	}

	// EVERY denied pattern must appear above. The deny list is subtractive, so
	// its failure mode is a route that quietly never gets asserted -- and this
	// table WAS hand-written and five entries stale when #154 added eight more.
	// Matching on the pattern's literal prefix before the first "{" is enough to
	// pair a concrete path with the pattern it exercises.
	covered := func(pattern string) bool {
		prefix := pattern
		if i := strings.Index(prefix, "{"); i >= 0 {
			prefix = prefix[:i]
		}
		for _, c := range cases {
			if strings.HasPrefix(c.path, prefix) {
				return true
			}
		}
		return false
	}
	for pattern := range readScopeDeniedPatterns {
		if !covered(pattern) {
			t.Errorf("readScopeDeniedPatterns has %q and no case above drives it. "+
				"A denial nothing asserts is a denial that can be deleted by accident.",
				pattern)
		}
	}

	for _, c := range cases {
		r := jsonRequest(t, c.method, c.path, c.body)
		bearer(read)(r)
		w := do(t, h, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s returned %d to a read token, want 403 (%s): %s",
				c.method, c.path, w.Code, c.why, strings.TrimSpace(w.Body.String()))
			continue
		}
		// The message has to say what to do about it. A bare "forbidden" on a
		// route that used to work sends an operator to the source.
		if !strings.Contains(w.Body.String(), "admin") {
			t.Errorf("%s %s: the 403 does not tell the caller which scope it needs: %s",
				c.method, c.path, w.Body.String())
		}

		// And the denial is about the SCOPE, not about the route being broken:
		// an admin token still reaches the handler. Anything but the read-only
		// 403 proves the request got past requireScope.
		ra := jsonRequest(t, c.method, c.path, c.body)
		bearer(admin)(ra)
		wa := do(t, h, ra)
		if wa.Code == http.StatusForbidden && strings.Contains(wa.Body.String(), "read-only") {
			t.Errorf("%s %s refused an ADMIN token as read-only; the denial list is "+
				"matching the wrong principal", c.method, c.path)
		}
	}
}

// TestExpertPreviewIsNoLongerOnTheReadScopeAllowlist.
//
// Asserted on the ROUTER rather than on the map, because the map is
// production source text and a test that read it would be the #107 mistake:
// it would pass just as happily if requireScope stopped consulting it.
//
// The two POSTs that remain on the allowlist are asserted alongside, so a
// change that emptied the list to make this pass would fail here instead.
func TestExpertPreviewIsNoLongerOnTheReadScopeAllowlist(t *testing.T) {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	r := jsonRequest(t, http.MethodPost, "/api/v1/routing/compile",
		map[string]any{"profile": map[string]any{}})
	bearer(read)(r)
	if w := do(t, h, r); w.Code == http.StatusForbidden {
		t.Errorf("POST /routing/compile was refused to a read token; it computes a "+
			"filter graph and returns no credential: %s", w.Body.String())
	}

	r2 := jsonRequest(t, http.MethodPost, "/api/v1/destinations/1/expert/preview",
		map[string]any{"inputArgs": "", "outputArgs": ""})
	bearer(read)(r2)
	if w := do(t, h, r2); w.Code != http.StatusForbidden {
		t.Errorf("POST /destinations/{id}/expert/preview reached a read token with %d; "+
			"its response is the FFmpeg argv, stream key and all", w.Code)
	}
}

// TestHLSPreviewIsSessionOnly.
//
// The preview group was the ONLY authenticated group in the router with no
// scope check at all, and it was registered with r.Handle, so it answered every
// method. Any path ending .m3u8 calls PreviewRequested before the file server
// runs, which starts the on-demand encoder and keeps it alive for as long as
// something keeps polling -- so a stolen bearer of either scope could pin an
// FFmpeg indefinitely.
//
// Session-only is asserted here as behaviour: the browser, which is the only
// thing that has ever fetched these, still gets through, and neither bearer
// does. A 404 from the session is the right pass -- the fixture has no preview
// segments on disk -- because it proves the request reached the file server
// rather than being turned away at the door.
func TestHLSPreviewIsSessionOnly(t *testing.T) {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

	r := jsonRequest(t, http.MethodGet, "/hls/live.m3u8", nil)
	sign(r)
	if w := do(t, h, r); w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
		t.Errorf("the operator's own session was refused the preview playlist with %d; "+
			"hls.js in the dashboard has nothing else to authenticate with", w.Code)
	}

	for _, principal := range []struct {
		name string
		tok  string
	}{{"read", read}, {"admin", admin}} {
		rb := jsonRequest(t, http.MethodGet, "/hls/live.m3u8", nil)
		bearer(principal.tok)(rb)
		if w := do(t, h, rb); w.Code != http.StatusForbidden {
			t.Errorf("a %s bearer reached the preview playlist with %d; that request "+
				"starts and sustains an FFmpeg", principal.name, w.Code)
		}
	}

	// And it no longer answers the methods a file server has no business
	// answering. POST used to reach PreviewRequested exactly as GET did.
	//
	// EXACTLY 405, and the loosening to `!= 405 && != 404` that stood here was
	// found by mutation to be vacuous: reverting the route registration to the
	// pre-fix `r.Handle("/hls/*", ...)`, which answers POST with the handler
	// itself, still produced a status this accepted. A 404 is what a route that
	// does not exist returns; the claim being made is that the route DOES exist
	// and refuses the method, and only 405 says that. It passes as written
	// today -- the narrowing costs nothing and buys the whole assertion.
	rp := jsonRequest(t, http.MethodPost, "/hls/live.m3u8", nil)
	sign(rp)
	w := do(t, h, rp)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /hls/live.m3u8 returned %d, want 405; the group is registered for "+
			"GET and HEAD, so anything else must be refused BY THE ROUTER rather than "+
			"reaching the handler", w.Code)
	}
	// Allow is asserted only to be PRESENT. It reads "HEAD" here rather than
	// "GET, HEAD", which was measured and is chi's own accounting of the
	// method-not-allowed set, not something this route table can spell
	// differently. Pinning the exact string would be pinning a dependency's
	// internals under the name of a claim about this product.
	if w.Header().Get("Allow") == "" {
		t.Error("POST /hls/live.m3u8 answered 405 with no Allow header")
	}
}

// TestEncoderRedetectNeedsAdmin.
//
// The plain listing is a genuine read and stays reachable -- withholding it
// would break the monitoring case the read scope exists for. `?redetect=`
// is not: it spawns a test encode per candidate encoder, enumerates GPU device
// nodes, and overwrites install-wide capability state under a global mutex on a
// context deliberately detached from the request, so the caller cannot even
// abort what it started.
//
// Gated in the handler rather than by denying the route, because both
// behaviours share one URL and only one of them is the problem. That is why
// this needs its own test: the route-level denial list would not have caught a
// regression here.
func TestEncoderRedetectNeedsAdmin(t *testing.T) {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	plain := jsonRequest(t, http.MethodGet, "/api/v1/encoders", nil)
	bearer(read)(plain)
	if w := do(t, h, plain); w.Code != http.StatusOK {
		t.Errorf("GET /encoders returned %d to a read token; listing what this FFmpeg "+
			"can encode is a read: %s", w.Code, w.Body.String())
	}

	redetect := jsonRequest(t, http.MethodGet, "/api/v1/encoders?redetect=1", nil)
	bearer(read)(redetect)
	w := do(t, h, redetect)
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET /encoders?redetect=1 returned %d to a read token; it runs test "+
			"encodes and rewrites this install's capability cache", w.Code)
	}
	if !strings.Contains(w.Body.String(), "admin") {
		t.Errorf("the 403 does not say which scope is needed: %s", w.Body.String())
	}
}
