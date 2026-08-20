package oauth

// Twitch's device code grant flow.
//
// The fixtures below are copied from the bodies on
// https://dev.twitch.tv/docs/authentication/getting-tokens-oauth/ rather than
// written from the structs that decode them. kick_test.go records what the
// other habit costs: a fake shaped like the code keeps a broken decode green
// for as long as it ships. That matters more here than usual, because two of
// Twitch's device-flow spellings differ from RFC 8628 and a self-agreeing
// fixture would confirm whichever spelling the code happened to send.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// twitchDeviceStub aims a provider at a handler and records the form of the
// last request, so a test can assert what was SENT as well as what was parsed.
// Both bases move together -- see WithBaseURL -- so nothing can reach the real
// id.twitch.tv.
func twitchDeviceStub(t *testing.T, h func(w http.ResponseWriter, r *http.Request, form url.Values)) (*Twitch, *deviceCalls) {
	t.Helper()
	calls := &deviceCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("stub could not parse the request form: %v", err)
		}
		calls.n++
		calls.path = r.URL.Path
		calls.form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		h(w, r, r.PostForm)
	}))
	t.Cleanup(srv.Close)
	return NewTwitch(WithBaseURL(srv.URL)), calls
}

type deviceCalls struct {
	n    int
	path string
	form url.Values
}

// The documented start response, verbatim from the vendor page.
const twitchDeviceStartBody = `{
   "device_code": "ike3GM8QIdYZs43KdrWPIO36LofILoCyFEzjlQ91",
   "expires_in": 1800,
   "interval": 5,
   "user_code": "ABCDEFGH",
   "verification_uri": "https://www.twitch.tv/activate?public=true&device-code=ABCDEFGH"
}`

func TestTwitchStartDeviceAuthAsksTheDeviceEndpointForTheScopesWeAlreadyRequest(t *testing.T) {
	tw, calls := twitchDeviceStub(t, func(w http.ResponseWriter, _ *http.Request, _ url.Values) {
		io.WriteString(w, twitchDeviceStartBody)
	})

	auth, err := tw.StartDeviceAuth(context.Background(), "cid")
	if err != nil {
		t.Fatalf("StartDeviceAuth failed against the documented response: %v", err)
	}

	if calls.path != "/oauth2/device" {
		t.Errorf("StartDeviceAuth posted to %q, want /oauth2/device", calls.path)
	}
	// PLURAL. The token call in the same vendor example uses the singular, and
	// getting these the wrong way round asks for no scopes at all -- which
	// succeeds here and fails later, as a 401 on a Helix write mid-broadcast.
	if got := calls.form.Get("scopes"); got != strings.Join(tw.Scopes(), " ") {
		t.Errorf("StartDeviceAuth sent scopes=%q, want the provider's own %q",
			got, strings.Join(tw.Scopes(), " "))
	}
	if calls.form.Has("scope") {
		t.Errorf("StartDeviceAuth sent a singular scope=%q; the device endpoint documents `scopes`",
			calls.form.Get("scope"))
	}
	if got := calls.form.Get("client_id"); got != "cid" {
		t.Errorf("StartDeviceAuth sent client_id=%q, want cid", got)
	}
	// The whole point of this flow is that it works for an operator whose box
	// has no registrable address, so a redirect URI must not creep into it.
	if calls.form.Has("redirect_uri") {
		t.Errorf("StartDeviceAuth sent redirect_uri=%q; device flow exists precisely because "+
			"there is no callback address to send", calls.form.Get("redirect_uri"))
	}

	if auth.DeviceCode != "ike3GM8QIdYZs43KdrWPIO36LofILoCyFEzjlQ91" {
		t.Errorf("device code is %q", auth.DeviceCode)
	}
	if auth.UserCode != "ABCDEFGH" {
		t.Errorf("user code is %q, want the one the operator types", auth.UserCode)
	}
	// Passed through untouched: it carries a query string, and any tidying here
	// would send the operator to a page that does not know which device asked.
	const wantURI = "https://www.twitch.tv/activate?public=true&device-code=ABCDEFGH"
	if auth.VerificationURI != wantURI {
		t.Errorf("verification URI is %q, want the platform's own %q", auth.VerificationURI, wantURI)
	}
	if auth.Interval != 5*time.Second {
		t.Errorf("poll interval is %v, want the 5s Twitch asked for", auth.Interval)
	}
	if auth.ExpiresAt.IsZero() {
		t.Error("expires_in was 1800 and ExpiresAt is zero, so a caller cannot stop polling a dead code")
	}
}

// A missing interval must not become a hot loop against the token endpoint --
// that is how an operator's whole app gets rate-limited for a mistake nobody
// made.
func TestTwitchDeviceAuthNeverPollsFasterThanTheFloor(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"interval absent", `{"device_code":"d","user_code":"U","verification_uri":"https://t.test/a"}`},
		{"interval zero", `{"device_code":"d","user_code":"U","verification_uri":"https://t.test/a","interval":0}`},
		{"interval negative", `{"device_code":"d","user_code":"U","verification_uri":"https://t.test/a","interval":-30}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tw, _ := twitchDeviceStub(t, func(w http.ResponseWriter, _ *http.Request, _ url.Values) {
				io.WriteString(w, tc.body)
			})
			auth, err := tw.StartDeviceAuth(context.Background(), "cid")
			if err != nil {
				t.Fatalf("StartDeviceAuth: %v", err)
			}
			if auth.Interval < deviceMinPollInterval {
				t.Errorf("poll interval is %v, below the %v floor", auth.Interval, deviceMinPollInterval)
			}
		})
	}
}

// The kick.go rule, one platform over: never invent the address.
func TestTwitchStartDeviceAuthNeverInventsAVerificationAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"no verification uri", `{"device_code":"d","user_code":"U","interval":5}`},
		{"no user code", `{"device_code":"d","verification_uri":"https://t.test/a","interval":5}`},
		{"no device code", `{"user_code":"U","verification_uri":"https://t.test/a","interval":5}`},
		{"an empty object", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tw, _ := twitchDeviceStub(t, func(w http.ResponseWriter, _ *http.Request, _ url.Values) {
				io.WriteString(w, tc.body)
			})
			auth, err := tw.StartDeviceAuth(context.Background(), "cid")
			if err == nil {
				t.Fatalf("StartDeviceAuth succeeded on %s and returned %#v; a caller would show "+
					"the operator a blank code", tc.name, auth)
			}
			if auth != nil {
				t.Errorf("StartDeviceAuth returned %#v alongside an error", auth)
			}
			// The only recovery from a missing verification_uri would be to
			// write twitch.tv/activate from memory. An operator who is sent to
			// a URL polyemesis made up cannot connect and cannot tell why.
			if strings.Contains(err.Error(), "://") {
				t.Errorf("the error reads as a fabricated verification address: %v", err)
			}
		})
	}
}

// The authorization server can also just say no -- a client id that was
// revoked, an app whose device grant is not enabled. That has to reach the
// operator with the status attached: it is the difference between "fix your
// credentials" and "try again later", and the support ticket is written from
// this string.
func TestTwitchStartDeviceAuthPassesTheAuthorizationServersRefusalOn(t *testing.T) {
	tw, _ := twitchDeviceStub(t, func(w http.ResponseWriter, _ *http.Request, _ url.Values) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"status":401,"message":"invalid client"}`)
	})

	auth, err := tw.StartDeviceAuth(context.Background(), "cid")
	if err == nil {
		t.Fatalf("StartDeviceAuth succeeded on a 401 and returned %#v", auth)
	}
	if auth != nil {
		t.Errorf("StartDeviceAuth returned %#v alongside an error", auth)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the error dropped the status the authorization server answered with: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid client") {
		t.Errorf("the error dropped what the platform said, so nobody can act on it: %v", err)
	}
}

// ------------------------------------------------------------------ polling

func TestTwitchPollDeviceAuthSendsTheDocumentedGrant(t *testing.T) {
	tw, calls := twitchDeviceStub(t, func(w http.ResponseWriter, _ *http.Request, _ url.Values) {
		// Verbatim from the vendor's device-flow example, scope array and all.
		io.WriteString(w, `{"access_token":"at","expires_in":14820,"refresh_token":"rt",`+
			`"scope":["channel:manage:broadcast"],"token_type":"bearer"}`)
	})

	tok, err := tw.PollDeviceAuth(context.Background(), "cid", "dev-code")
	if err != nil {
		t.Fatalf("PollDeviceAuth failed on the documented success body: %v", err)
	}

	if calls.path != "/oauth2/token" {
		t.Errorf("PollDeviceAuth posted to %q, want /oauth2/token", calls.path)
	}
	if got := calls.form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Errorf("grant_type is %q; Twitch documents the RFC 8628 URN", got)
	}
	if got := calls.form.Get("device_code"); got != "dev-code" {
		t.Errorf("device_code is %q, want the one the caller polled with", got)
	}
	// SINGULAR here, plural on the device endpoint. Both spellings come from
	// the same vendor example.
	if got := calls.form.Get("scope"); got != strings.Join(tw.Scopes(), " ") {
		t.Errorf("PollDeviceAuth sent scope=%q, want %q", got, strings.Join(tw.Scopes(), " "))
	}
	if calls.form.Has("scopes") {
		t.Errorf("PollDeviceAuth sent a plural scopes=%q; the token endpoint documents `scope`",
			calls.form.Get("scopes"))
	}
	// The documented request carries no secret. The app stays confidential --
	// it has to, or the code flow every existing operator uses stops working --
	// but sending Twitch a parameter its own example omits is the gamble
	// twitch.go's PKCE comment refuses to take.
	if calls.form.Has("client_secret") {
		t.Error("PollDeviceAuth sent client_secret, which the documented device-flow request does not")
	}

	if tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Errorf("token is %+v, want the access and refresh pair from the body", tok)
	}
	// SINGLE USE. A caller that stores this and keeps the old one orphans the
	// account on the next refresh; the assertion is that we hand back what
	// Twitch sent so the caller has something to store.
	if tok.RefreshToken == "" {
		t.Error("no refresh token survived the poll, so the account dies at the first expiry")
	}
	if tok.ExpiresAt.IsZero() {
		t.Error("expires_in was 14820 and ExpiresAt is zero")
	}
	if tok.Scopes != "channel:manage:broadcast" {
		t.Errorf("scopes decoded to %q from Twitch's array spelling", tok.Scopes)
	}
}

func TestTwitchPollDeviceAuthTellsWaitingApartFromFailing(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
		// wantOther means: a real error, and specifically NOT one of the
		// sentinels a polling caller treats as "keep going" or "start over".
		wantOther bool
	}{
		{
			// Twitch's own envelope. Not the RFC's.
			name:   "the documented pending body keeps the caller polling",
			status: http.StatusBadRequest,
			body:   `{"status":400,"message":"authorization_pending"}`,
			want:   ErrDeviceAuthPending,
		},
		{
			// The classifier matches the token, not the envelope, so the day
			// Twitch aligns with RFC 8628 nothing here has to change. Written
			// down as a test because a matcher keyed on `"message":` would pass
			// every other case in this file.
			name:   "the RFC spelling of pending is read the same way",
			status: http.StatusBadRequest,
			body:   `{"error":"authorization_pending","error_description":"waiting"}`,
			want:   ErrDeviceAuthPending,
		},
		{
			name:   "a spent device code stops the loop",
			status: http.StatusBadRequest,
			body:   `{"status":400,"message":"invalid device code"}`,
			want:   ErrDeviceCodeSpent,
		},
		{
			// Twitch answers "invalid device code" for a code that expired as
			// well as one already redeemed, and does not say which. One
			// sentinel, because inventing the distinction would mean showing
			// an operator a reason nobody has.
			name:   "an expired device code is the same answer and the same sentinel",
			status: http.StatusBadRequest,
			body:   `{"status":400,"message":"Invalid device code"}`,
			want:   ErrDeviceCodeSpent,
		},
		{
			// The reason the status is checked as well as the body. An error
			// page that happens to contain the phrase must not be read as an
			// invitation to keep polling through an outage.
			name:      "a 500 that merely contains the phrase is an outage, not a pending grant",
			status:    http.StatusInternalServerError,
			body:      `<html>authorization_pending</html>`,
			wantOther: true,
		},
		{
			// Rate limiting is a real failure: a caller that read it as
			// "pending" would poll harder into the thing that is throttling it.
			name:      "rate limiting stops the loop",
			status:    http.StatusTooManyRequests,
			body:      `{"status":429,"message":"too many requests"}`,
			wantOther: true,
		},
		{
			name:      "an unrecognised refusal is not silently treated as pending",
			status:    http.StatusBadRequest,
			body:      `{"status":400,"message":"invalid client"}`,
			wantOther: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tw, _ := twitchDeviceStub(t, func(w http.ResponseWriter, _ *http.Request, _ url.Values) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			tok, err := tw.PollDeviceAuth(context.Background(), "cid", "dev-code")
			if err == nil {
				t.Fatalf("PollDeviceAuth succeeded on a %d and returned %#v", tc.status, tok)
			}
			if tok != nil {
				t.Errorf("PollDeviceAuth returned %#v alongside an error", tok)
			}
			if tc.wantOther {
				if errors.Is(err, ErrDeviceAuthPending) {
					t.Errorf("a caller would poll through this forever: %v", err)
				}
				if errors.Is(err, ErrDeviceCodeSpent) {
					t.Errorf("a caller would abandon a live device code over this: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("PollDeviceAuth returned %v, want %v", err, tc.want)
			}
			// The sentinel must not swallow what the platform said; an
			// operator's support ticket is written from this string.
			if !strings.Contains(err.Error(), "400") {
				t.Errorf("the error dropped the status Twitch answered with: %v", err)
			}
		})
	}
}

// The guard on the polling loop itself. An empty device code can never succeed,
// so sending it every interval would burn requests against an endpoint that
// rate-limits the operator's whole app.
func TestTwitchPollDeviceAuthRefusesAnEmptyCodeWithoutAskingTwitch(t *testing.T) {
	tw, calls := twitchDeviceStub(t, func(w http.ResponseWriter, _ *http.Request, _ url.Values) {
		io.WriteString(w, `{"access_token":"at","expires_in":3600}`)
	})
	if _, err := tw.PollDeviceAuth(context.Background(), "cid", ""); err == nil {
		t.Fatal("PollDeviceAuth accepted an empty device code")
	}
	if calls.n != 0 {
		t.Errorf("PollDeviceAuth made %d request(s) with no device code to redeem", calls.n)
	}
}

// TestAnUnreachableAuthorizationServerIsAFailureAndNotOneOfTheSentinels is the
// third answer a poll can get, after "keep waiting" and "start over": nothing
// at all.
//
// classifyTwitchDeviceError reads the status and the body, and a connection
// that never opened has neither. Both sentinels would be wrong: pending would
// poll into a dead network for the code's whole 1800 seconds, and spent would
// throw away an authorization the operator is very likely still completing. The
// caller's own branch for a real failure -- which keeps the flow and retries --
// is the only correct one, and it is reached by returning the error untouched.
//
// StartDeviceAuth is asserted alongside it because the two share postFormJSON
// and the same base URL: if a dead host produced a nil error anywhere in that
// path, the start would hand the dialog a blank code rather than a message, and
// the poll's classification would never be reached to be judged.
func TestAnUnreachableAuthorizationServerIsAFailureAndNotOneOfTheSentinels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	// Closed before a single call, so both endpoints refuse the connection.
	srv.Close()
	tw := NewTwitch(WithBaseURL(base))

	if auth, err := tw.StartDeviceAuth(context.Background(), "cid"); err == nil {
		t.Errorf("StartDeviceAuth succeeded against a host that is not listening: %#v", auth)
	}

	tok, err := tw.PollDeviceAuth(context.Background(), "cid", "dev-code")
	if err == nil {
		t.Fatalf("PollDeviceAuth succeeded against a host that is not listening: %#v", tok)
	}
	if errors.Is(err, ErrDeviceAuthPending) {
		t.Errorf("a dead network was read as a pending authorization, so a caller would poll "+
			"it until the code expired: %v", err)
	}
	if errors.Is(err, ErrDeviceCodeSpent) {
		t.Errorf("a dead network was read as a spent code, so a caller would abandon a live "+
			"authorization over one refused connection: %v", err)
	}
}

// --------------------------------------------------------- who has this flow

// TestDeviceFlowIsTwitchOnlyAndSaysWhyForEachPlatformThatLacksIt is the
// negative half of #442, and it is the half worth pinning.
//
// Three platforms are absent for three DIFFERENT reasons, and each one is a
// decision somebody could reasonably reverse by adding twenty lines:
//
//	YouTube  the limited-input-device flow is capped at the `youtube` and
//	         `youtube.readonly` scopes, so youtube.upload -- thumbnails --
//	         becomes unreachable, and it needs a second "TVs and Limited Input
//	         devices" client. A loopback Desktop-app client runs the ordinary
//	         code flow with no ceiling and needs no code at all.
//	Facebook Device Login is current, but which permissions it can grant is
//	         UNVERIFIED. Building on it means finding out during a broadcast.
//	Kick     has no device endpoint. There is nothing to call.
//
// A test that only asserted "Twitch implements DeviceFlower" would stay green
// through somebody adding YouTube's, which is the change this is really here to
// make somebody argue for.
func TestDeviceFlowIsTwitchOnlyAndSaysWhyForEachPlatformThatLacksIt(t *testing.T) {
	withFlow := map[db.Platform]bool{db.PlatformTwitch: true}
	why := map[db.Platform]string{
		db.PlatformYouTube: "the device flow is capped at the youtube/youtube.readonly scopes, " +
			"so youtube.upload is unreachable; loopback gets YouTube the ordinary code flow instead",
		db.PlatformFacebook: "which permissions Device Login can grant is UNVERIFIED",
		db.PlatformKick:     "Kick documents no device authorization endpoint at all",
	}

	for p := range Providers() {
		df, ok := DeviceFor(p)
		if withFlow[p] {
			if !ok {
				t.Errorf("DeviceFor(%s) did not resolve; #442 built device flow for exactly this platform", p)
				continue
			}
			if df == nil {
				t.Errorf("DeviceFor(%s) answered true with a nil provider, which panics on use", p)
			}
			continue
		}
		if ok {
			t.Errorf("DeviceFor(%s) resolved, and #442 concluded it should not: %s", p, why[p])
		}
	}

	// Neither half may quietly cover nothing: if Providers() ever stopped
	// registering Twitch, the positive branch above would assert on an empty
	// set and pass for the wrong reason.
	if _, ok := DeviceFor(db.PlatformTwitch); !ok {
		t.Fatal("Twitch is the only device flow there is, and it did not resolve")
	}

	// A platform string nobody registered is refused the same way as one that is
	// registered but has no device flow: (nil, false). It reaches here from a URL
	// parameter --
	// internal/api reads the platform out of the path -- so it is a value an
	// operator can type, and a nil interface returned alongside true would panic
	// on the first method call rather than being refused by name.
	if df, ok := DeviceFor(db.Platform("peertube")); ok || df != nil {
		t.Errorf("DeviceFor(peertube) answered (%#v, %v) for a platform that has no provider "+
			"at all", df, ok)
	}
}

// The Set twin, for the reason endpoints.go states in prose: a caller holding a
// stubbed Set that resolved this through the package function would POST an
// operator's real client id to id.twitch.tv and then poll it every five
// seconds, from a test that believed everything was pointed at a stub.
func TestSetDeviceForResolvesAgainstTheSetAndNotTheRealTwitch(t *testing.T) {
	base, guard := stubbedWorld(t)
	set := NewSet(WithBaseURL(base))

	df, ok := set.DeviceFor(db.PlatformTwitch)
	if !ok {
		t.Fatal("Set.DeviceFor(twitch) did not resolve; the twin is not wired")
	}
	// Driven, so a provider that resolves but is aimed at production still
	// trips the escape guard. Errors are ignored: the generic stub body does
	// not satisfy the parsing, and the claim here is about destination.
	_, _ = df.StartDeviceAuth(context.Background(), "cid")
	_, _ = df.PollDeviceAuth(context.Background(), "cid", "dev-code")

	if got := guard.escapes(); len(got) > 0 {
		t.Fatalf("device flow resolved through a stubbed Set still reached real hosts:\n  %s",
			strings.Join(got, "\n  "))
	}

	// A platform without the capability must answer false rather than a nil
	// interface that panics on use.
	if _, ok := set.DeviceFor(db.PlatformKick); ok {
		t.Error("Set.DeviceFor(kick) resolved; Kick has no device authorization endpoint")
	}
	// And a platform the set does not contain at all, which is the other way in:
	// the platform arrives as a URL parameter, so "there is no such provider" is
	// as ordinary an answer as "that provider has no device flow", and both have
	// to leave the caller with a nil it was told about rather than one it wasn't.
	if df, ok := set.DeviceFor(db.Platform("peertube")); ok || df != nil {
		t.Errorf("Set.DeviceFor(peertube) answered (%#v, %v) for a platform the set does not "+
			"hold", df, ok)
	}
}
