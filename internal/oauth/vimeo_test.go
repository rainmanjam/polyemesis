package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// vimeoStub returns a provider pointed at a test server, plus the server.
// Same shape as kickStub and xStub: consent, tokens and data all resolve
// through the per-instance base, so nothing here can reach api.vimeo.com.
func vimeoStub(t *testing.T, h http.HandlerFunc) (*Vimeo, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewVimeo(WithBaseURL(srv.URL)), srv
}

// vimeoMeFixture is Vimeo's own example response for GET /me, cut down to the
// two fields this provider reads. The shape is transcribed from the rendered
// reference (developer.vimeo.com/api/reference/users, "Get the user", Example
// tab, read 2026-08-26) rather than invented -- which is the value of a
// fixture: a rename in the decode struct fails here instead of producing a
// nameless account at connect time.
const vimeoMeFixture = `{
  "uri": "/users/152184",
  "name": "Vimeo Staff",
  "link": "https://vimeo.com/staff",
  "resource_key": "bac1033deba2310ebba2caec33c23e4beea67aba"
}`

// ------------------------------------------------------------------- oauth

func TestVimeoAuthURLMatchesTheDocumentedQueryAndLeaksNoPKCE(t *testing.T) {
	v := &Vimeo{}
	if v.PKCE() {
		t.Fatal("PKCE() is true. Vimeo's authentication guide documents four grant types " +
			"and no RFC 7636 parameter in any of them; see the comment on PKCE for what " +
			"evidence would justify turning it on")
	}

	// A challenge is passed anyway: the framework hands one to every provider,
	// and a provider reporting PKCE false must DISCARD it. Vimeo's own words
	// for a malformed authorize request are "the request fails, and the
	// standard Vimeo 404 page loads" -- lock-everyone-out with no diagnosis.
	raw := v.AuthURL("client-id", "https://polyemesis.test/api/v1/oauth/vimeo/callback",
		"state-123", "a-challenge")

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthURL produced an unparseable URL %q: %v", raw, err)
	}
	if got, want := u.Scheme+"://"+u.Host+u.Path, "https://api.vimeo.com/oauth/authorize"; got != want {
		t.Errorf("AuthURL points at %q, want %q -- the address in Vimeo's authorization-code workflow", got, want)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type": "code",
		"client_id":     "client-id",
		"redirect_uri":  "https://polyemesis.test/api/v1/oauth/vimeo/callback",
		"state":         "state-123",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// "The space-separated list of scopes that you want to be able to access."
	// Table 6, verbatim. A comma or a plus here is a silently narrower grant:
	// Vimeo reads the whole thing as one unknown scope name.
	if got, want := q.Get("scope"), "public private"; got != want {
		t.Errorf("scope = %q, want %q", got, want)
	}
	if q.Has("code_challenge") || q.Has("code_challenge_method") {
		t.Fatalf("Vimeo does not opt into PKCE and AuthURL still sent %v", u.RawQuery)
	}
}

// THE TRAP THIS PINS: every other provider here posts a form with client_id and
// client_secret as fields, which is what postForm does. Vimeo's Table 8 says
// Authorization is `basic base64_encode(x:y)`, Content-Type is
// application/json, and Accept pins version 3.4. A form post is refused, and
// the refusal reads exactly like a bad credential.
func TestVimeoExchangePostsJSONWithBasicAuthAndTheVersionHeader(t *testing.T) {
	var (
		gotPath   string
		gotAuth   string
		gotType   string
		gotAccept string
		gotBody   map[string]any
	)
	v, _ := vimeoStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"access_token":"tok-1","token_type":"bearer","scope":"public private"}`))
	})

	tok, err := v.Exchange(context.Background(), "cid", "sec",
		"https://polyemesis.test/cb", "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	if gotPath != "/oauth/access_token" {
		t.Errorf("posted to %q, want /oauth/access_token", gotPath)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("cid:sec"))
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want HTTP Basic of the client pair", gotAuth)
	}
	if !strings.HasPrefix(gotType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json -- Vimeo's token endpoints do not take a form", gotType)
	}
	if gotAccept != "application/vnd.vimeo.*+json;version=3.4" {
		t.Errorf("Accept = %q, want the version pin. Without it Vimeo answers in whatever "+
			"version it currently defaults to, which can change under a running install", gotAccept)
	}
	if gotBody["grant_type"] != "authorization_code" || gotBody["code"] != "the-code" ||
		gotBody["redirect_uri"] != "https://polyemesis.test/cb" {
		t.Errorf("body = %v, want the three fields Vimeo's step 4 names", gotBody)
	}
	if _, leaked := gotBody["code_verifier"]; leaked {
		t.Errorf("Exchange sent code_verifier to a provider that has not documented PKCE: %v", gotBody)
	}
	if tok.AccessToken != "tok-1" || tok.Scopes != "public private" {
		t.Errorf("token = %v, want the access_token and scope Vimeo returned", tok)
	}
	// Vimeo's Table 10 lists no expires_in and no refresh_token for this grant.
	// A non-zero ExpiresAt here would make db.PlatformAccount.Expired() true and
	// send internal/api down the refresh path, which for Vimeo cannot work.
	if !tok.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want zero: Vimeo sends no expires_in for the "+
			"authorization-code grant, and inventing one makes the account expire "+
			"into a refresh that has no endpoint to call", tok.ExpiresAt)
	}
	if tok.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty: Vimeo issues none", tok.RefreshToken)
	}
}

func TestVimeoNeverPutsTheClientSecretWhereItCanBeReadBack(t *testing.T) {
	// An http.Client failure arrives as a *url.Error carrying the FULL request
	// URL, so a credential in a query string ends up in the operator's error
	// text and in the logs on any DNS outage. credcheck.go records that
	// happening to Facebook. Vimeo's secret rides in a header, and this is what
	// keeps it there.
	const secret = "s3cr3t-do-not-print-me"
	var gotURL, gotBody string
	v, _ := vimeoStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"We don't recognize your API app."}`))
	})

	err := v.CheckCredentials(context.Background(), "cid", secret)
	if err == nil {
		t.Fatal("a 401 was accepted as a valid credential pair")
	}
	if strings.Contains(gotURL, secret) {
		t.Errorf("the request URL carries the client secret: %q", gotURL)
	}
	if strings.Contains(gotBody, secret) {
		t.Errorf("the request body carries the client secret: %q", gotBody)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the error text carries the client secret: %q", err.Error())
	}
}

// Refresh must make NO request. Vimeo documents no refresh grant for the
// authorization-code flow, so any request at all is a guessed endpoint whose
// 400 reads like a broken credential -- and the guess is exactly the "finish
// the stub" edit somebody will be tempted into.
func TestVimeoRefreshMakesNoRequest(t *testing.T) {
	var calls int
	v, _ := vimeoStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"access_token":"nope"}`))
	})

	tok, err := v.Refresh(context.Background(), "cid", "sec", "a-refresh-token")
	if err == nil {
		t.Fatal("Refresh reported success. Vimeo issues no refresh token for this grant, " +
			"so a success here means something was invented")
	}
	if tok != nil {
		t.Fatalf("Refresh returned a token: %v", tok)
	}
	if calls != 0 {
		t.Fatalf("Refresh made %d HTTP request(s). Vimeo documents no refresh endpoint; "+
			"posting at a guessed one produces a 400 that reads like a bad credential", calls)
	}
}

func TestVimeoAccountReadsTheMemberFromSlashMe(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	v, _ := vimeoStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotAccept = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept")
		_, _ = w.Write([]byte(vimeoMeFixture))
	})

	acct, err := v.Account(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if gotPath != "/me" {
		t.Errorf("read %q, want /me", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
	if gotAccept != "application/vnd.vimeo.*+json;version=3.4" {
		t.Errorf("Accept = %q, want the version pin on the data call too", gotAccept)
	}
	if acct.Name != "Vimeo Staff" {
		t.Errorf("Name = %q, want %q", acct.Name, "Vimeo Staff")
	}
	// Vimeo publishes no bare numeric id on this resource, only the uri. The
	// ref is what gets stored and handed back to the platform, so a leading
	// "/users/" left on it is a ref that matches nothing.
	if acct.Ref != "152184" {
		t.Errorf("Ref = %q, want %q derived from the uri", acct.Ref, "152184")
	}
}

// ------------------------------------------------------- the Enterprise gate

// THE THREE OUTCOMES entitlement.go names, and the third is the one that
// usually gets dropped: a probe that did not complete is NOT evidence of a
// gate, and reporting it as one tells somebody their contract is too small on
// the strength of a timeout.
func TestVimeoCheckEntitlementDistinguishesRefusalFromFailureToAsk(t *testing.T) {
	t.Run("the live API answers", func(t *testing.T) {
		var gotPath string
		v, _ := vimeoStub(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			// An EMPTY list is a pass. The question is whether the endpoint
			// answers, not whether the operator has scheduled anything.
			_, _ = w.Write([]byte(`{"total":0,"data":[]}`))
		})
		if err := v.CheckEntitlement(context.Background(), "cid", "tok"); err != nil {
			t.Fatalf("a 200 from the live API was reported as a gate: %v", err)
		}
		if gotPath != "/me/live_events" {
			t.Errorf("probed %q, want /me/live_events -- the cheapest READ on the gated "+
				"surface, and one that creates nothing", gotPath)
		}
	})

	// Vimeo's reference for this method publishes exactly one response row,
	// 200, and no error table at all -- so there is no documented status to
	// match on and every non-2xx has to count. A test that only fed 403 would
	// pass over an implementation that hardcoded 403 and let a 401-shaped
	// refusal through as "entitled".
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			v, _ := vimeoStub(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"You do not have permission to do that."}`))
			})
			err := v.CheckEntitlement(context.Background(), "cid", "tok")
			if !errors.Is(err, ErrNotEntitled) {
				t.Fatalf("a %d from the gated API did not wrap ErrNotEntitled, so no caller "+
					"can tell this refusal from a broken one: %v", status, err)
			}
			// The whole point is that the operator gets the platform's own
			// words. A bare "403" is the mystery this mechanism exists to end.
			if !strings.Contains(err.Error(), "Enterprise") {
				t.Errorf("the message does not name Vimeo's own gate: %q", err.Error())
			}
			// And what Vimeo actually replied, because Vimeo documents no error
			// code here and a bug report needs the platform's answer rather
			// than polyemesis's interpretation of it.
			if !strings.Contains(err.Error(), "permission to do that") {
				t.Errorf("the message drops what Vimeo actually said: %q", err.Error())
			}
		})
	}

	t.Run("the probe never completed", func(t *testing.T) {
		// A server that is not there: this is a transport failure, not an
		// answer. Closing the stub is the cheapest honest way to produce one.
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		base := srv.URL
		srv.Close()
		v := NewVimeo(WithBaseURL(base))

		err := v.CheckEntitlement(context.Background(), "cid", "tok")
		if err == nil {
			t.Fatal("an unreachable platform was reported as entitled, which is a clean bill " +
				"of health polyemesis has no evidence for")
		}
		if errors.Is(err, ErrNotEntitled) {
			t.Fatalf("a transport failure was reported as a commercial gate: %v.\n"+
				"That tells an operator their plan is too small on the strength of a "+
				"timeout -- the defect credcheck.go describes for CheckUnreachable, "+
				"one layer along", err)
		}
	})
}

// Ingest is where the gate reaches an operator who never reads a settings page:
// they press Fetch key. All three answers are ErrNoStreamKeyAPI, because
// internal/api uses that sentinel to send them to the paste field with a reason
// rather than offering a retry that cannot work -- and they say three different
// true things.
func TestVimeoIngestSaysWhichReasonAppliesToThisAccount(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
		notWant string
	}{
		{
			name: "gated",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			want: "Enterprise",
		},
		{
			name: "entitled, and polyemesis still cannot fetch one",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"total":0,"data":[]}`))
			},
			want: "does not create Vimeo live events yet",
			// Telling an Enterprise customer their plan is the problem is the
			// mirror image of the mystery 403, and just as wrong.
			notWant: "Enterprise-only",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, _ := vimeoStub(t, tc.handler)
			_, err := v.Ingest(context.Background(), "cid", "tok")
			if !errors.Is(err, ErrNoStreamKeyAPI) {
				t.Fatalf("Ingest did not wrap ErrNoStreamKeyAPI, so internal/api answers 502 "+
					"and invites a retry that can never succeed: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the message does not say %q: %v", tc.want, err)
			}
			if tc.notWant != "" && strings.Contains(err.Error(), tc.notWant) {
				t.Errorf("the message claims %q for an account that is not gated: %v", tc.notWant, err)
			}
		})
	}
}

func TestVimeoEntitlementReasonIsUsableBeforeAnybodyHasConnectedAnything(t *testing.T) {
	// It is what the setup guide, the capability matrix and the destination
	// preset say to somebody still deciding. A reason that only exists after a
	// probe arrives too late to be worth having.
	v := &Vimeo{}
	for name, got := range map[string]string{
		"EntitlementReason": v.EntitlementReason(),
		"ManualKeyReason":   v.ManualKeyReason(),
	} {
		if !strings.Contains(got, "Enterprise") {
			t.Errorf("%s does not name Vimeo's gate: %q", name, got)
		}
	}
	// The stream-key advice has one more job than the gate sentence: telling
	// the operator what still works. capabilities.go's whole argument is that a
	// pasted key is a fully supported destination, not a degraded one.
	if !strings.Contains(v.ManualKeyReason(), "works exactly as well") {
		t.Errorf("ManualKeyReason does not tell the operator what still works: %q", v.ManualKeyReason())
	}
}

// ------------------------------------------------------ credential checking

func TestVimeoCheckCredentials(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantErr     bool
		unreachable bool
	}{
		{name: "accepted", status: 200, body: `{"access_token":"a","token_type":"bearer","scope":"public"}`},
		// Vimeo's Table 4: 401 with error code 8001, "We don't recognize your
		// API app." A considered answer about the pair, and it stands.
		{name: "app not recognised", status: 401, body: `{"error":"8001"}`, wantErr: true},
		{name: "bad grant_type", status: 400, body: `{"error":"2204"}`, wantErr: true},
		// A 5xx says nothing about whether the credentials are right.
		{name: "platform broken", status: 502, body: `<html>bad gateway</html>`, wantErr: true, unreachable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotBody map[string]any
			v, _ := vimeoStub(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &gotBody)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			err := v.CheckCredentials(context.Background(), "cid", "sec")
			if tc.wantErr && err == nil {
				t.Fatal("accepted, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("rejected a valid pair: %v", err)
			}
			if got := errors.Is(err, ErrCheckUnreachable); got != tc.unreachable {
				t.Fatalf("errors.Is(err, ErrCheckUnreachable) = %v, want %v (err = %v)",
					got, tc.unreachable, err)
			}
			if gotPath != "/oauth/authorize/client" {
				t.Errorf("checked against %q, want /oauth/authorize/client", gotPath)
			}
			if gotBody["grant_type"] != "client_credentials" {
				t.Errorf("grant_type = %v, want client_credentials", gotBody["grant_type"])
			}
			// Vimeo states a client-credentials token cannot reach anything but
			// public data "even if you specify additional scopes". Asking for
			// more on a yes/no credential check is asking for what the grant
			// cannot issue.
			if gotBody["scope"] != "public" {
				t.Errorf("scope = %v, want public", gotBody["scope"])
			}
		})
	}
}

// ------------------------------------------------------------- registration

// The Set twin, which endpoints.go asks for in prose. Without it a caller
// holding a stubbed Set resolves the entitlement probe through the PACKAGE
// lookup, which reads the production providers -- and the first thing it does
// is send an operator's live access token to api.vimeo.com from a test that
// believed the whole world was pointed at a stub.
func TestEntitlementForResolvesTheSamePlatformsAsItsSetTwin(t *testing.T) {
	base, guard := stubbedWorld(t)
	set := NewSet(WithBaseURL(base))

	var gated, plain int
	for _, row := range PlatformCapabilities() {
		if row.Platform == "" {
			continue
		}
		pkgOK := func() bool { _, ok := EntitlementFor(row.Platform); return ok }()
		setEG, setOK := set.EntitlementFor(row.Platform)
		if pkgOK != setOK {
			t.Fatalf("EntitlementFor(%s) = %v but Set.EntitlementFor = %v. A caller holding a "+
				"stubbed Set would fall through to the production provider for this one call",
				row.Platform, pkgOK, setOK)
		}
		if !setOK {
			// False must be a usable answer rather than a nil interface that
			// panics on use: internal/api branches on this bool.
			plain++
			continue
		}
		gated++
		_ = setEG.CheckEntitlement(context.Background(), "cid", "tok")
	}
	// Neither half may quietly cover nothing.
	if gated == 0 || plain == 0 {
		t.Fatalf("this test needs both cases to mean anything: %d gated platforms, %d ungated", gated, plain)
	}
	if got := guard.escapes(); len(got) > 0 {
		t.Fatalf("the entitlement probe resolved through a stubbed Set still reached real hosts:\n  %s",
			strings.Join(got, "\n  "))
	}
}

// Vimeo is the only gate today, and pinning that is what makes the test above
// non-vacuous in the direction that matters: if the registration were dropped,
// the loop would simply stop finding a gated platform.
func TestVimeoIsTheRegisteredEntitlementGate(t *testing.T) {
	if _, ok := EntitlementFor(db.PlatformVimeo); !ok {
		t.Fatal("Vimeo does not resolve as an EntitlementGated provider, so nothing asks " +
			"Vimeo whether this account reaches the live API and the refusal arrives " +
			"mid-broadcast instead")
	}
	for _, p := range []db.Platform{db.PlatformYouTube, db.PlatformTwitch, db.PlatformFacebook, db.PlatformKick} {
		if _, ok := EntitlementFor(p); ok {
			t.Errorf("%s reports a commercial gate. If that is now true, say so in "+
				"capabilities.go and docs/PLATFORMS.md too; if it is not, the assertion "+
				"is matching something it should not", p)
		}
	}
}
