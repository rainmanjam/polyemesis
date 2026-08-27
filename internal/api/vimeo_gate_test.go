package api

// The connect handler's half of the Enterprise gate, driven end to end against
// a stub standing in for api.vimeo.com.
//
// WHAT IS ACTUALLY BEING ASSERTED, and why it is worth a whole file. Vimeo's
// live API is available only to Vimeo Enterprise customers, and it says so
// nowhere in its responses -- so an operator with correct credentials, granted
// scopes and a connected account has no route from anything polyemesis shows
// them to the reason nothing works. The mechanism under test turns that into a
// sentence at connect time. The two ways to get it wrong are symmetrical and
// both are covered here:
//
//	SILENCE     the probe fails or is skipped, the connection reports a plain
//	            success, and the operator learns about the gate from a refusal
//	            mid-broadcast. That is the status quo this replaces.
//	OVERREACH   the probe cannot complete and the operator is told their plan is
//	            too small anyway, or -- worse -- the connection is FAILED over
//	            a billing question, turning "this feature needs Enterprise" into
//	            "signing in is broken".

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// vimeoStubServer answers the three calls a Vimeo connect makes: the token
// exchange, the identity read, and the live-events probe. liveEvents is what a
// test varies.
type vimeoStubServer struct {
	URL string
	// liveEvents is the answer to GET /me/live_events -- the gated read. Its
	// status is the entire input to the mechanism under test.
	liveEvents stubAnswer
	probes     int
}

func newVimeoStub(t *testing.T, liveEvents stubAnswer) *vimeoStubServer {
	t.Helper()
	s := &vimeoStubServer{liveEvents: liveEvents}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/access_token":
			write(w, stubAnswer{http.StatusOK,
				`{"access_token":"vimeo-at-1","token_type":"bearer","scope":"public private"}`})
		case "/me":
			write(w, stubAnswer{http.StatusOK, `{"uri":"/users/152184","name":"Vimeo Staff"}`})
		case "/me/live_events":
			s.probes++
			// A zero status means "hang up": the request never gets an answer
			// at all, which is a TRANSPORT failure rather than a refusal. That
			// distinction is the whole subject of
			// TestAProbeThatCouldNotRunIsNeitherAGateNorSilence, and a 502
			// cannot stand in for it -- a 502 is a non-2xx answer from the
			// gated endpoint, which Vimeo's provider counts as a refusal on
			// purpose (Vimeo publishes no error table for that method).
			if s.liveEvents.status == 0 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return
				}
				_ = conn.Close()
				return
			}
			write(w, s.liveEvents)

		// Twitch's connect, so the control case below has an UNGATED platform
		// to connect through the very same handler. platformStub cannot serve
		// it: that one answers the data APIs and deliberately has no token
		// endpoint at all.
		case "/oauth2/token":
			write(w, stubAnswer{http.StatusOK,
				`{"access_token":"tw-at","refresh_token":"tw-rt","expires_in":3600,` +
					`"scope":["channel:manage:broadcast"],"token_type":"bearer"}`})
		// Helix's /users. The path has no /helix prefix under WithBaseURL:
		// twitchHelixBase already carries it in production.
		case "/users":
			write(w, stubAnswer{http.StatusOK,
				`{"data":[{"id":"44322889","login":"dallas","display_name":"Dallas"}]}`})

		default:
			write(w, stubAnswer{http.StatusNotFound, `{"error":"no stub for ` + r.URL.Path + `"}`})
		}
	}))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// connectVimeo runs a whole authorization-code callback and returns the
// Location the browser is sent back to, parsed.
func connectVimeo(t *testing.T, stub *vimeoStubServer) url.Values {
	t.Helper()
	s, h, store := testServerWith(t, Options{
		Config:    config.Config{},
		Providers: oauth.NewSet(oauth.WithBaseURL(stub.URL)),
	})
	if err := store.PutPlatformCreds(s.box, db.PlatformVimeo, "client-id-abc", "client-secret-xyz"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	// The state is what the start handler would have stored. Vimeo does not do
	// PKCE, so the verifier is empty -- exactly as handleOAuthStart leaves it.
	if err := store.PutOAuthState("state-vimeo-1", db.PlatformVimeo, ""); err != nil {
		t.Fatalf("state: %v", err)
	}

	sign := login(t, h)
	r := jsonRequest(t, http.MethodGet,
		"/api/v1/oauth/vimeo/callback?code=the-code&state=state-vimeo-1", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusFound {
		t.Fatalf("callback: status %d, want 302, body %s", w.Code, w.Body.String())
	}

	// The account must exist whatever the gate said. A connection unwound over
	// a billing question is the overreach failure.
	accts, err := store.ListPlatformAccounts()
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	var found bool
	for _, a := range accts {
		if a.Platform == db.PlatformVimeo {
			found = true
		}
	}
	if !found {
		t.Fatal("the Vimeo account was not stored. The gate is a fact about what the " +
			"account can DO; it must never turn a successful sign-in into a failed one")
	}

	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location %q: %v", w.Header().Get("Location"), err)
	}
	return loc.Query()
}

// Mutations run against this, both observed failing:
//   - pass "" to oauthWarn instead of s.entitlementWarning(...) in the callback.
//   - drop `";", "%3B"` from urlEscape. That one is not a decoration: Go's
//     url.Values.Get DROPS a pair whose value contains a semicolon, and the
//     gate sentence has one, so the warning vanished entirely with the code
//     otherwise unchanged. It is why urlEscape gained that entry.
func TestConnectingAGatedAccountSaysSoInThePlatformsOwnWords(t *testing.T) {
	stub := newVimeoStub(t, stubAnswer{http.StatusForbidden,
		`{"error":"You do not have permission to do that."}`})
	q := connectVimeo(t, stub)

	if stub.probes != 1 {
		t.Fatalf("the live API was probed %d times, want exactly 1 -- at connect, once, "+
			"which is the whole point of doing it here rather than at go-live", stub.probes)
	}
	if q.Get("oauth_ok") == "" {
		t.Error("the connection did not report success. The account connected; the gate is " +
			"about what it can do afterwards")
	}
	warn := q.Get("oauth_warn")
	if warn == "" {
		t.Fatal("connecting a gated account reported a plain success with no warning at all. " +
			"That is the status quo this mechanism replaces: the operator's next evidence " +
			"is a refusal mid-broadcast from an API that never uses the word Enterprise")
	}
	if !strings.Contains(warn, "Enterprise") {
		t.Errorf("the warning does not name Vimeo's own gate, so the operator still has "+
			"nowhere to go with it: %q", warn)
	}
	if q.Get("oauth_error") != "" {
		t.Errorf("the gate was reported as an ERROR (%q). Nothing needs retrying and "+
			"nothing failed -- red sends somebody back round a flow that succeeded",
			q.Get("oauth_error"))
	}
}

// Mutation run against this: make entitlementWarning return the reason
// unconditionally rather than only when CheckEntitlement errs.
// Observed FAIL ("an entitled account was warned about a gate it passed").
func TestConnectingAnEntitledAccountIsNotWarnedAboutAGateItPassed(t *testing.T) {
	stub := newVimeoStub(t, stubAnswer{http.StatusOK, `{"total":0,"data":[]}`})
	q := connectVimeo(t, stub)

	if q.Get("oauth_ok") == "" {
		t.Error("the connection did not report success")
	}
	if warn := q.Get("oauth_warn"); warn != "" {
		t.Errorf("an entitled account was warned about a gate it passed: %q.\n"+
			"An empty live-events list is a pass -- the question is whether the endpoint "+
			"answers, not whether the operator has scheduled anything", warn)
	}
}

// The third outcome, which is the one that usually gets dropped. A probe that
// could not run is not evidence of a gate AND is not nothing: reporting it as a
// gate is a claim about somebody's contract made on the strength of a bad
// minute, and reporting nothing hands them a clean bill of health polyemesis
// has no evidence for.
//
// Mutations run against this, both observed failing:
//   - collapse entitlementWarning's non-ErrNotEntitled branch to `return ""`.
//   - widen its ErrNotEntitled branch to `if err != nil`, so an unfinished
//     probe is reported as the gate itself.
//
// The stub hangs the connection up rather than answering 502, and that detail
// is load-bearing: an earlier version of this test used a 502, which Vimeo's
// provider counts as a REFUSAL on purpose -- so the test passed under the first
// mutation and was asserting nothing about the branch it names.
func TestAProbeThatCouldNotRunIsNeitherAGateNorSilence(t *testing.T) {
	// status 0 hangs the connection up: the probe gets no answer at all.
	stub := newVimeoStub(t, stubAnswer{})
	q := connectVimeo(t, stub)

	if q.Get("oauth_ok") == "" {
		t.Error("the connection did not report success")
	}
	warn := q.Get("oauth_warn")
	if warn == "" {
		t.Fatal("a probe that never ran reported a clean bill of health. The operator has " +
			"to know the check did not happen, or silence reads as a pass and they learn " +
			"about the gate mid-broadcast after all")
	}
	// It must say the CHECK did not happen, not that the account is gated.
	// Telling somebody their plan is too small on the strength of a dropped
	// connection is the overreach half of this file's header.
	if !strings.Contains(warn, "could not check") {
		t.Errorf("a probe that never completed was reported as a verdict about the "+
			"account: %q", warn)
	}
	// And it must still name the gate, so the operator knows what the
	// unfinished check was about and can go and read it themselves.
	if !strings.Contains(warn, "Enterprise") {
		t.Errorf("the warning does not say what the check was about: %q", warn)
	}
	if q.Get("oauth_error") != "" {
		t.Errorf("an unreachable platform failed the connection: %q", q.Get("oauth_error"))
	}
}

// The other half, and the reason the case above needed a hijack rather than a
// 502: a non-2xx ANSWER from the gated endpoint is a refusal, deliberately,
// because Vimeo publishes no error table for that method and matching a
// specific status would let a differently-shaped refusal through as "entitled".
func TestANonSuccessAnswerFromTheGatedEndpointIsReportedAsTheGate(t *testing.T) {
	stub := newVimeoStub(t, stubAnswer{http.StatusBadGateway, `<html>bad gateway</html>`})
	q := connectVimeo(t, stub)

	warn := q.Get("oauth_warn")
	if !strings.Contains(warn, "Enterprise") {
		t.Errorf("a non-2xx from the gated endpoint did not produce the gate warning: %q", warn)
	}
	if q.Get("oauth_error") != "" {
		t.Errorf("an unhealthy platform failed the connection: %q", q.Get("oauth_error"))
	}
}

// Every other platform must redirect exactly as it did before oauth_warn
// existed. A warning parameter that turns up on a Twitch connect is noise on
// the screen an operator sees least often and trusts most.
//
// The probe counter is what makes this bite: a lookup that started resolving
// for an ungated platform would send Twitch's token at a live-events endpoint.
func TestAPlatformWithNoGateRedirectsExactlyAsBefore(t *testing.T) {
	// The live-events answer is a 403, so this would warn loudly if the lookup
	// ever started resolving for a platform that has no gate.
	stub := newVimeoStub(t, stubAnswer{http.StatusForbidden, `{"error":"nope"}`})
	s, h, store := testServerWith(t, Options{
		Config:    config.Config{},
		Providers: oauth.NewSet(oauth.WithBaseURL(stub.URL)),
	})
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "cid", "sec"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	if err := store.PutOAuthState("state-twitch-1", db.PlatformTwitch, ""); err != nil {
		t.Fatalf("state: %v", err)
	}

	sign := login(t, h)
	r := jsonRequest(t, http.MethodGet,
		"/api/v1/oauth/twitch/callback?code=the-code&state=state-twitch-1", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusFound {
		t.Fatalf("callback: status %d, want 302, body %s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if loc.Query().Has("oauth_warn") {
		t.Errorf("a platform with no entitlement gate carried a warning: %q",
			w.Header().Get("Location"))
	}
	if loc.Query().Get("oauth_ok") == "" {
		t.Errorf("Twitch no longer reports a plain success: %q", w.Header().Get("Location"))
	}
	if stub.probes != 0 {
		t.Errorf("connecting Twitch probed the live-events endpoint %d times. The lookup "+
			"must answer false for a platform with no gate, not go asking", stub.probes)
	}
}
