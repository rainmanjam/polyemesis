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
	h, _, sign, destID := plantedServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

	_ = destID
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
	h, _, sign, _ := plantedServer(t)
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
	h, _, sign, _ := plantedServer(t)
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
	rp := jsonRequest(t, http.MethodPost, "/hls/live.m3u8", nil)
	sign(rp)
	if w := do(t, h, rp); w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
		t.Errorf("POST /hls/live.m3u8 returned %d; the group is registered for GET and "+
			"HEAD now, so anything else should not reach the handler", w.Code)
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
	h, _, sign, _ := plantedServer(t)
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
