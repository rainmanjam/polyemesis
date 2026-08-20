package api

// The API half of #442, driven end to end against a stub standing in for
// id.twitch.tv.
//
// A SEPARATE STUB FROM platformStub, deliberately. That one answers Helix and
// Graph -- the DATA APIs -- and has no /oauth2 anything, because until now no
// test in this package needed a token minted. Teaching it the device endpoints
// would give every existing test a token endpoint it never asked for, and this
// stub needs something platformStub is built not to do: answer the SAME request
// differently on successive calls, since "still pending, then a token" is the
// whole behaviour under test.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// twitchDeviceStub is id.twitch.tv plus Helix's /users, with a scripted answer
// per poll.
type twitchDeviceStub struct {
	URL string

	mu sync.Mutex
	// tokenAnswers is consumed one per POST /oauth2/token. Each entry is
	// written verbatim, with its status, so a test spells Twitch's own envelope
	// rather than a paraphrase of it -- which is the distinction
	// classifyTwitchDeviceError exists for.
	tokenAnswers []stubAnswer
	// deviceAnswer overrides the device endpoint's reply.
	deviceAnswer *stubAnswer
	// usersAnswer overrides Helix's /users, which is how the identity read that
	// happens AFTER a token has been minted is made to fail.
	usersAnswer *stubAnswer
	tokenCalls  int
	deviceCalls int
	seenScopes  []string
	seenCodes   []string
}

type stubAnswer struct {
	status int
	body   string
}

func newTwitchDeviceStub(t *testing.T) *twitchDeviceStub {
	t.Helper()
	s := &twitchDeviceStub{}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *twitchDeviceStub) set() oauth.Set {
	return oauth.NewSet(oauth.WithBaseURL(s.URL))
}

func (s *twitchDeviceStub) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case r.URL.Path == "/oauth2/device":
		s.deviceCalls++
		// `scopes`, PLURAL, is what Twitch documents for this endpoint, and
		// recording what arrived is how this test would notice the provider
		// starting to send the RFC's singular spelling -- which asks for no
		// scopes at all and fails much later, during a broadcast.
		s.seenScopes = append(s.seenScopes, r.Form.Get("scopes"))
		if s.deviceAnswer != nil {
			write(w, *s.deviceAnswer)
			return
		}
		write(w, stubAnswer{http.StatusOK, `{
			"device_code": "DEV-CODE-SECRET",
			"user_code": "ABCD-1234",
			"verification_uri": "https://www.twitch.tv/activate?public=true",
			"expires_in": 1800,
			"interval": 5
		}`})

	case r.URL.Path == "/oauth2/token":
		s.tokenCalls++
		s.seenCodes = append(s.seenCodes, r.Form.Get("device_code"))
		if len(s.tokenAnswers) == 0 {
			write(w, stubAnswer{http.StatusInternalServerError, `{"message":"stub ran out of scripted answers"}`})
			return
		}
		a := s.tokenAnswers[0]
		s.tokenAnswers = s.tokenAnswers[1:]
		write(w, a)

	case r.URL.Path == "/users":
		if s.usersAnswer != nil {
			write(w, *s.usersAnswer)
			return
		}
		write(w, stubAnswer{http.StatusOK,
			`{"data":[{"id":"44322889","login":"dallas","display_name":"Dallas"}]}`})

	default:
		write(w, stubAnswer{http.StatusNotFound, `{"message":"no stub for ` + r.URL.Path + `"}`})
	}
}

func write(w http.ResponseWriter, a stubAnswer) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.status)
	_, _ = w.Write([]byte(a.body))
}

// twitchPendingBody is Twitch's OWN envelope for a pending authorization,
// spelled out here rather than the RFC's `{"error":"authorization_pending"}`.
// A test written against the RFC's spelling would pass with a classifier that
// cannot read Twitch, which is the bug twitch_device.go's header names.
const twitchPendingBody = `{"status":400,"message":"authorization_pending"}`

// twitchSpentBody is the answer to a code already redeemed AND to one that
// timed out. Twitch does not distinguish them.
const twitchSpentBody = `{"status":400,"message":"invalid device code"}`

const twitchTokenBody = `{
	"access_token": "at-device-1",
	"refresh_token": "rt-device-1",
	"expires_in": 14124,
	"scope": ["channel:manage:broadcast"],
	"token_type": "bearer"
}`

// deviceFlowServer is a signed-in server with Twitch credentials stored and
// every provider aimed at the stub.
func deviceFlowServer(t *testing.T) (*Server, http.Handler, *twitchDeviceStub, func(*http.Request)) {
	t.Helper()
	stub := newTwitchDeviceStub(t)
	s, h, store := testServerWith(t, Options{Config: config.Config{}, Providers: stub.set()})
	if err := store.PutPlatformCreds(s.box, db.PlatformTwitch, "client-id-abc", "client-secret-xyz"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	return s, h, stub, login(t, h)
}

func startDeviceFlow(t *testing.T, h http.Handler, sign func(*http.Request)) deviceAuthView {
	t.Helper()
	raw := send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/twitch/device", nil, http.StatusOK)
	var out deviceAuthView
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode start: %v (%s)", err, raw)
	}
	return out
}

func pollDeviceFlow(t *testing.T, h http.Handler, sign func(*http.Request), handle string) devicePollView {
	t.Helper()
	raw := send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/twitch/device/poll",
		map[string]any{"handle": handle}, http.StatusOK)
	var out devicePollView
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode poll: %v (%s)", err, raw)
	}
	return out
}

// TestStartDeviceAuthGivesTheOperatorACodeAndKeepsTheSecretOnTheServer is the
// property oauth.DeviceAuth's `json:"-"` tag exists to guarantee, asserted at
// the wire rather than at the struct.
//
// The tag is only worth anything if no handler copies the value back out by
// hand, and a struct tag cannot enforce that. This reads the actual response
// bytes and requires the device code to be absent from them -- so a future
// handler that adds `"deviceCode": flow.deviceCode` to the payload, for the
// entirely reasonable-looking purpose of letting the client poll without a
// handle, fails here.
//
// Mutation to run against it: give deviceAuthView its own DeviceCode field,
// tagged deviceCode, and fill it from authz.DeviceCode.
// Observed FAIL ("the start response carries the device code").
func TestStartDeviceAuthGivesTheOperatorACodeAndKeepsTheSecretOnTheServer(t *testing.T) {
	_, h, stub, sign := deviceFlowServer(t)

	raw := send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/twitch/device", nil, http.StatusOK)
	if strings.Contains(string(raw), "DEV-CODE-SECRET") {
		t.Errorf("the start response carries the device code, which is the bearer-equivalent "+
			"secret that redeems the token: %s", raw)
	}

	var out deviceAuthView
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if out.UserCode != "ABCD-1234" {
		t.Errorf("userCode = %q, want the code the platform issued", out.UserCode)
	}
	// Passed through untouched. A verification URI assembled here instead of
	// returned by the platform is the fabricated-URL failure kick.go's Ingest
	// refuses, and twitch.tv/activate written from memory would drop the query
	// string Twitch actually sends.
	if out.VerificationURI != "https://www.twitch.tv/activate?public=true" {
		t.Errorf("verificationUri = %q, want the platform's own address verbatim, query "+
			"string included", out.VerificationURI)
	}
	if out.Handle == "" {
		t.Error("no handle, so nothing can be polled")
	}
	if out.IntervalSeconds != 5 {
		t.Errorf("intervalSeconds = %d, want 5", out.IntervalSeconds)
	}
	if out.ExpiresAt.IsZero() {
		t.Error("expiresAt is zero, so the UI cannot stop polling a dead code")
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.seenScopes) != 1 || stub.seenScopes[0] != strings.Join((&oauth.Twitch{}).Scopes(), " ") {
		t.Errorf("the device endpoint received scopes %q, want exactly the provider's "+
			"Scopes(); an account connected by device flow must carry the same "+
			"ScopeVersion as a code-flow one", stub.seenScopes)
	}
}

// TestPollingThroughToAConnectedAccountStoresItLikeTheCodeFlowDoes drives the
// whole flow: pending, then a token, then the stored account.
//
// The account is what makes this more than a protocol test. A device-flow
// connection has to be INDISTINGUISHABLE from a code-flow one afterwards --
// same refresh path, same ScopeVersion -- because everything downstream
// (tokenFor, RefreshLoop, AccountNeedsReconnect) reads the row and not the flow
// that produced it.
func TestPollingThroughToAConnectedAccountStoresItLikeTheCodeFlowDoes(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	stub.tokenAnswers = []stubAnswer{
		{http.StatusBadRequest, twitchPendingBody},
		{http.StatusOK, twitchTokenBody},
	}

	start := startDeviceFlow(t, h, sign)

	// The first poll is refused by the pacing guard without a request leaving
	// the process, so the clock is wound back to let it through. Winding the
	// registry rather than sleeping five seconds is the point: the guard is
	// asserted on its own below.
	openThePollGate(t, s, start.Handle)
	if got := pollDeviceFlow(t, h, sign, start.Handle); got.State != devicePending {
		t.Fatalf("first poll state = %q, want %q -- Twitch's own envelope carries the RFC "+
			"token inside a `message` field, and a classifier keyed on `error` reads it "+
			"as a hard failure", got.State, devicePending)
	}

	openThePollGate(t, s, start.Handle)
	done := pollDeviceFlow(t, h, sign, start.Handle)
	if done.State != deviceConnected {
		t.Fatalf("second poll state = %q, want %q (reason %q)", done.State, deviceConnected, done.Reason)
	}
	if done.Account == nil {
		t.Fatal("connected with no account in the response; the UI has nothing to render")
	}
	if done.Account.AccountName != "Dallas" || done.Account.AccountRef != "44322889" {
		t.Errorf("account = %q/%q, want the identity Helix reported",
			done.Account.AccountName, done.Account.AccountRef)
	}

	stored, err := s.store.ListPlatformAccounts()
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored %d accounts, want exactly one", len(stored))
	}
	got, err := s.store.GetPlatformAccount(s.box, stored[0].ID)
	if err != nil {
		t.Fatalf("read account: %v", err)
	}
	if got.AccessToken != "at-device-1" || got.RefreshToken != "rt-device-1" {
		t.Errorf("stored tokens = %q/%q, want the pair the device grant returned. The "+
			"refresh token is ONE TIME USE, so losing it here orphans the account",
			got.AccessToken, got.RefreshToken)
	}
	// Stamped at connect time from the provider, exactly as handleOAuthCallback
	// does. A zero here means AccountNeedsReconnect would nag an account that
	// was just connected with the current scopes.
	if got.ScopeVer != (&oauth.Twitch{}).ScopeVersion() {
		t.Errorf("scopeVer = %d, want the provider's current %d",
			got.ScopeVer, (&oauth.Twitch{}).ScopeVersion())
	}

	// The handle is spent -- the server is no longer holding it at all, which is
	// why the poll gate cannot be opened for it. A client that keeps polling
	// after success must not be told to keep waiting for something that already
	// happened.
	if _, held := s.devices.take(start.Handle); held {
		t.Error("the server is still holding a device code it has already redeemed")
	}
	if again := pollDeviceFlow(t, h, sign, start.Handle); again.State != deviceExpired {
		t.Errorf("polling a completed flow = %q, want %q", again.State, deviceExpired)
	}
}

// TestAnEarlyPollNeverReachesThePlatform is the guarantee device_flow.go's
// header claims, and it is the one property the UI cannot provide.
//
// A duplicated tab, a reload loop or a future refactor that forgets the
// interval spends the OPERATOR'S rate limit on the operator's whole app,
// mid-connect. The count of requests that reached the stub is the assertion,
// because a server that answered "pending" politely while still calling Twitch
// would look identical from the response alone.
//
// Mutation to run against it: delete the `now.Before(flow.nextPollAt)` branch
// in handlePollDeviceAuth. Observed FAIL ("2 token requests reached the
// platform, want 0").
func TestAnEarlyPollNeverReachesThePlatform(t *testing.T) {
	_, h, stub, sign := deviceFlowServer(t)
	stub.tokenAnswers = []stubAnswer{
		{http.StatusBadRequest, twitchPendingBody},
		{http.StatusBadRequest, twitchPendingBody},
	}

	start := startDeviceFlow(t, h, sign)
	for i := 0; i < 2; i++ {
		got := pollDeviceFlow(t, h, sign, start.Handle)
		if got.State != devicePending {
			t.Fatalf("poll %d state = %q, want %q", i, got.State, devicePending)
		}
		// The client is told how long is left rather than being left to guess,
		// so a reloaded tab that lost the start response still paces itself.
		if got.RetryInSeconds <= 0 || got.RetryInSeconds > 5 {
			t.Errorf("poll %d retryInSeconds = %d, want 1..5", i, got.RetryInSeconds)
		}
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.tokenCalls != 0 {
		t.Errorf("%d token requests reached the platform, want 0: the interval is enforced "+
			"on the server, not just honoured by the client", stub.tokenCalls)
	}
}

// TestASpentCodeStopsTheFlowRatherThanBeingRetriedForever pins the branch that
// decides whether an operator watches a spinner for half an hour.
//
// ErrDeviceCodeSpent covers a code already redeemed AND one that expired,
// because Twitch answers "invalid device code" for both and inventing a
// distinction it does not draw would mean showing a reason we made up.
func TestASpentCodeStopsTheFlowRatherThanBeingRetriedForever(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	stub.tokenAnswers = []stubAnswer{{http.StatusBadRequest, twitchSpentBody}}

	start := startDeviceFlow(t, h, sign)
	openThePollGate(t, s, start.Handle)

	got := pollDeviceFlow(t, h, sign, start.Handle)
	if got.State != deviceExpired {
		t.Fatalf("state = %q, want %q", got.State, deviceExpired)
	}
	if strings.TrimSpace(got.Reason) == "" {
		t.Error("expired with no reason; the operator is told to start again without being " +
			"told what happened")
	}
	// And the handle is gone, so a client that ignores the word still cannot
	// spend another request on a code that can only ever answer the same thing.
	if _, held := s.devices.take(start.Handle); held {
		t.Error("the server is still holding a device code Twitch will never honour")
	}
}

// TestAPlatformWithNoDeviceFlowIsRefusedByName is the negative half, at the API
// layer.
//
// #442 built device flow for exactly one platform and the other three are
// absent for three DIFFERENT reasons. A handler that answered every platform
// the same way -- or, worse, that resolved a nil DeviceFlower and panicked --
// would make that decision invisible from outside.
func TestAPlatformWithNoDeviceFlowIsRefusedByName(t *testing.T) {
	_, h, _, sign := deviceFlowServer(t)

	for _, platform := range []string{"youtube", "facebook", "kick"} {
		raw := send(t, h, sign, http.MethodPost,
			"/api/v1/platforms/credentials/"+platform+"/device", nil, http.StatusBadRequest)
		if !strings.Contains(string(raw), platform) {
			t.Errorf("%s was refused without being named: %s. Three platforms lack a device "+
				"flow for three different reasons, and a generic sentence reads as a fault",
				platform, raw)
		}
	}
}

// TestStartingADeviceFlowWithoutCredentialsSaysWhichScreenToGoTo mirrors
// handleOAuthStart's 412: an operator who has not pasted a client id yet needs
// the settings page, not a 500.
func TestStartingADeviceFlowWithoutCredentialsSaysWhichScreenToGoTo(t *testing.T) {
	stub := newTwitchDeviceStub(t)
	_, h, _ := testServerWith(t, Options{Config: config.Config{}, Providers: stub.set()})
	sign := login(t, h)

	send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/twitch/device", nil, http.StatusPreconditionFailed)

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.deviceCalls != 0 {
		t.Errorf("%d device requests were made with no client id configured", stub.deviceCalls)
	}
}

// TestAnUnknownHandleIsExpiredRatherThanAnError covers the restart case, which
// is the one an operator actually hits: the box is upgraded while a code is on
// screen, the registry is in memory, and the next poll names a handle nobody
// holds. That is the same recovery as an expired code and must use the same
// word, or the UI grows a second dead end.
func TestAnUnknownHandleIsExpiredRatherThanAnError(t *testing.T) {
	_, h, _, sign := deviceFlowServer(t)

	got := pollDeviceFlow(t, h, sign, "a-handle-nobody-issued")
	if got.State != deviceExpired {
		t.Errorf("state = %q, want %q", got.State, deviceExpired)
	}
}

// TestAFlowPastItsExpiryIsNeverPolled stops a request being spent on a code the
// server can already prove is dead.
func TestAFlowPastItsExpiryIsNeverPolled(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	stub.tokenAnswers = []stubAnswer{{http.StatusOK, twitchTokenBody}}

	start := startDeviceFlow(t, h, sign)
	s.devices.mu.Lock()
	s.devices.byHandle[start.Handle].expiresAt = time.Now().Add(-time.Second)
	s.devices.byHandle[start.Handle].nextPollAt = time.Now().Add(-time.Second)
	s.devices.mu.Unlock()

	if got := pollDeviceFlow(t, h, sign, start.Handle); got.State != deviceExpired {
		t.Fatalf("state = %q, want %q", got.State, deviceExpired)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.tokenCalls != 0 {
		t.Errorf("%d token requests were made against a code that had already expired",
			stub.tokenCalls)
	}
}

// TestATransportFailureKeepsTheFlowAlive is the difference between "Twitch is
// down" and "this code is dead", which oauth.classifyTwitchDeviceError draws by
// returning 429s and 5xx untouched. Losing the flow on a transient fault would
// make an operator restart a perfectly good authorization because the platform
// hiccuped once.
func TestATransportFailureKeepsTheFlowAlive(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	stub.tokenAnswers = []stubAnswer{
		{http.StatusTooManyRequests, `{"status":429,"message":"too many requests"}`},
		{http.StatusOK, twitchTokenBody},
	}

	start := startDeviceFlow(t, h, sign)
	openThePollGate(t, s, start.Handle)
	send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/twitch/device/poll",
		map[string]any{"handle": start.Handle}, http.StatusBadGateway)

	if _, held := s.devices.take(start.Handle); !held {
		t.Fatal("a 429 threw the flow away; the code is still good and the operator may " +
			"still be typing")
	}
	openThePollGate(t, s, start.Handle)
	if got := pollDeviceFlow(t, h, sign, start.Handle); got.State != deviceConnected {
		t.Errorf("state after the platform recovered = %q, want %q", got.State, deviceConnected)
	}
}

// TestAHandleCannotBeRedeemedAgainstAnotherPlatform. Not a security boundary --
// the handle is unguessable and both halves are behind the same session -- but
// redeeming a Twitch code against whatever provider the URL named would store
// the wrong thing under the wrong platform.
func TestAHandleCannotBeRedeemedAgainstAnotherPlatform(t *testing.T) {
	s, h, _, sign := deviceFlowServer(t)
	start := startDeviceFlow(t, h, sign)
	openThePollGate(t, s, start.Handle)

	send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/youtube/device/poll",
		map[string]any{"handle": start.Handle}, http.StatusBadRequest)
}

// TestAStartThePlatformRefusesIsA502AndLeavesNoFlowBehind. The status is the
// assertion: 502 says the far end misbehaved, 500 would blame this process for
// something it did not do, and 400 would send the operator to the Settings page
// to re-check a client id that is very probably fine.
func TestAStartThePlatformRefusesIsA502AndLeavesNoFlowBehind(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	stub.deviceAnswer = &stubAnswer{http.StatusInternalServerError,
		`{"status":500,"message":"internal server error"}`}

	send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/twitch/device", nil, http.StatusBadGateway)

	// Nothing was issued, so nothing may be held. A handle recorded here would be
	// one the UI could poll forever against a device code that does not exist.
	s.devices.mu.Lock()
	defer s.devices.mu.Unlock()
	if n := len(s.devices.byHandle); n != 0 {
		t.Errorf("%d flow(s) recorded from a start that never received a device code", n)
	}
}

// TestAnAbandonedFlowIsSweptWhenTheNextOneStarts is the only thing that ever
// removes a flow nobody came back for.
//
// The operator closed the dialog, or the tab went away. No poll will arrive, so
// no poll can notice the expiry, and the entry holds a bearer-equivalent secret
// in memory. The sweep on the next start is the whole of the cleanup -- see
// deviceFlows.put -- so a change that dropped it would leak an entry per
// abandoned attempt for the life of the process.
func TestAnAbandonedFlowIsSweptWhenTheNextOneStarts(t *testing.T) {
	s, h, _, sign := deviceFlowServer(t)

	// Two operators, or two tabs. Only one of them walks away.
	stillTyping := startDeviceFlow(t, h, sign)
	abandoned := startDeviceFlow(t, h, sign)
	s.devices.mu.Lock()
	s.devices.byHandle[abandoned.Handle].expiresAt = time.Now().Add(-time.Second)
	s.devices.mu.Unlock()

	fresh := startDeviceFlow(t, h, sign)

	if _, held := s.devices.take(abandoned.Handle); held {
		t.Error("a device code that died of old age is still being held; nothing polls it and " +
			"nothing else removes it, so it stays until the process ends")
	}
	// The other half of the claim: the sweep takes the dead one and ONLY the dead
	// one. This is the assertion a put that simply emptied the map would fail --
	// stillTyping was already there when the sweep ran, so clearing the map takes
	// it, and the operator staring at that code polls a handle the server has
	// forgotten. The just-started one cannot test this: it is written in after
	// the sweep, so it survives an emptied map too.
	if _, held := s.devices.take(stillTyping.Handle); !held {
		t.Error("the sweep took a live flow with a code an operator may still be entering")
	}
	if _, held := s.devices.take(fresh.Handle); !held {
		t.Error("the sweep took the flow that had just been started")
	}
}

// TestAPollBodyTheServerCannotReadIsRefusedBeforeThePlatformIsCalled.
//
// The handle is the ONLY thing a poll may name -- that is the entire point of
// handing out an opaque one -- so a client that tries to supply the device code
// itself is refused rather than humoured. The flow survives, because a
// malformed request is the client's mistake and not a reason to make the
// operator start over.
func TestAPollBodyTheServerCannotReadIsRefusedBeforeThePlatformIsCalled(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	stub.tokenAnswers = []stubAnswer{{http.StatusOK, twitchTokenBody}}

	start := startDeviceFlow(t, h, sign)
	openThePollGate(t, s, start.Handle)

	send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/twitch/device/poll",
		map[string]any{"handle": start.Handle, "deviceCode": "DEV-CODE-SECRET"},
		http.StatusBadRequest)

	stub.mu.Lock()
	calls := stub.tokenCalls
	stub.mu.Unlock()
	if calls != 0 {
		t.Errorf("%d token request(s) went out for a body the server had already rejected", calls)
	}
	if _, held := s.devices.take(start.Handle); !held {
		t.Error("a malformed poll threw the flow away; the operator would have to start over " +
			"because their browser sent a field the server does not accept")
	}
}

// TestAFlowWhosePlatformLostItsDeviceSupportIsRefusedRatherThanPanicking.
//
// Only reachable if the build changed under a live flow -- which is a restart,
// which drops the registry -- so the branch is unreachable in production and
// worth keeping anyway: the alternative to answering is resolving a nil
// DeviceFlower and calling a method on it, which takes the request handler down
// rather than the request. The held flow's platform is rewritten by hand --
// s.devices.byHandle, not the oauth provider registry -- because a start
// against Kick is refused up front, so that is the only way to end up holding a
// live flow whose platform has no device provider.
func TestAFlowWhosePlatformLostItsDeviceSupportIsRefusedRatherThanPanicking(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	start := startDeviceFlow(t, h, sign)

	s.devices.mu.Lock()
	s.devices.byHandle[start.Handle].platform = db.PlatformKick
	s.devices.byHandle[start.Handle].nextPollAt = time.Now().Add(-time.Second)
	s.devices.mu.Unlock()

	raw := send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/kick/device/poll",
		map[string]any{"handle": start.Handle}, http.StatusBadRequest)
	if !strings.Contains(string(raw), string(db.PlatformKick)) {
		t.Errorf("the refusal does not name the platform it is about: %s", raw)
	}
	// And the handle goes, because no future poll of it can end differently.
	if _, held := s.devices.take(start.Handle); held {
		t.Error("the server is still holding a flow it has no way to redeem")
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.tokenCalls != 0 {
		t.Errorf("%d token request(s) went out for a platform with no device flow", stub.tokenCalls)
	}
}

// TestATokenWhoseAccountCannotBeReadEndsTheFlow is the window between the two
// calls a successful poll makes.
//
// Twitch has minted the token, so the device code is spent whatever happens
// next; it will not mint a second one. Keeping the handle would leave the UI
// polling a code that can only answer "invalid device code" from here on, and
// the operator watching a spinner. So the handle goes and the answer is a 502 --
// the platform's fault, not the operator's -- and no half-built account is
// stored.
func TestATokenWhoseAccountCannotBeReadEndsTheFlow(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	stub.tokenAnswers = []stubAnswer{{http.StatusOK, twitchTokenBody}}
	stub.usersAnswer = &stubAnswer{http.StatusInternalServerError,
		`{"error":"Internal Server Error","status":500}`}

	start := startDeviceFlow(t, h, sign)
	openThePollGate(t, s, start.Handle)

	send(t, h, sign, http.MethodPost,
		"/api/v1/platforms/credentials/twitch/device/poll",
		map[string]any{"handle": start.Handle}, http.StatusBadGateway)

	if _, held := s.devices.take(start.Handle); held {
		t.Error("the server is still holding a device code Twitch has already redeemed; every " +
			"further poll of it can only answer that it is invalid")
	}
	accounts, err := s.store.ListPlatformAccounts()
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("%d account(s) stored from a token whose identity was never read", len(accounts))
	}
}

// TestAStoreThatRefusesTheAccountEndsTheFlowRatherThanLeavingItPollable is the
// same window one step later: the token is real, the identity is read, and the
// write fails.
//
// The device code is just as spent as in the case above, so the handle has to
// go for the same reason. What must NOT happen is a "connected" answer with no
// row behind it -- the UI would close the dialog and list an account that is not
// there.
func TestAStoreThatRefusesTheAccountEndsTheFlowRatherThanLeavingItPollable(t *testing.T) {
	s, h, stub, sign := deviceFlowServer(t)
	stub.tokenAnswers = []stubAnswer{{http.StatusOK, twitchTokenBody}}

	start := startDeviceFlow(t, h, sign)
	openThePollGate(t, s, start.Handle)

	// Taking the store away is the one failure a test can stage at exactly this
	// point: the platform calls have already happened and the write has not.
	if err := s.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	w := pollWithoutTheRouter(t, s, string(db.PlatformTwitch), start.Handle)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("poll status = %d, want %d; a store that cannot answer is this process's "+
			"failure and not the platform's", w.Code, http.StatusInternalServerError)
	}
	if strings.Contains(w.Body.String(), string(deviceConnected)) {
		t.Errorf("the poll reported a connection that was never written: %s", w.Body.String())
	}
	if _, held := s.devices.take(start.Handle); held {
		t.Error("the server is still holding a device code that has already been redeemed")
	}
}

// pollWithoutTheRouter drives handlePollDeviceAuth directly, for the one test
// that needs the store to be gone.
//
// The session middleware reads the same store, so a request through the handler
// chain would be turned away at the door and never reach the branch under test.
// Everything the handler itself reads -- the platform URL parameter and the JSON
// body -- is supplied here exactly as chi and the SPA supply it.
func pollWithoutTheRouter(t *testing.T, s *Server, platform, handle string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"handle": handle})
	if err != nil {
		t.Fatalf("encode poll body: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost,
		"/api/v1/platforms/credentials/"+platform+"/device/poll", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("platform", platform)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	s.handlePollDeviceAuth(w, r)
	return w
}

// openThePollGate winds the pacing clock back so a test can take the next poll
// without sleeping through the platform's interval.
//
// It reaches into the registry rather than shortening the interval, because the
// interval is the thing under test in TestAnEarlyPollNeverReachesThePlatform
// and a fixture that could shrink it would let that test pass against a server
// that had stopped enforcing anything.
func openThePollGate(t *testing.T, s *Server, handle string) {
	t.Helper()
	s.devices.mu.Lock()
	defer s.devices.mu.Unlock()
	f, ok := s.devices.byHandle[handle]
	if !ok {
		t.Fatalf("no flow is being held for %q", handle)
	}
	f.nextPollAt = time.Now().Add(-time.Second)
}

// TestSecondsUntilRoundsUpSoAClientIsNeverToldToWaitNoTime is the arithmetic on
// its own, because the rounding DIRECTION is the whole value of the function: a
// client told to wait 0 polls immediately, which is the hot loop the interval
// exists to prevent.
func TestSecondsUntilRoundsUpSoAClientIsNeverToldToWaitNoTime(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		at   time.Time
		want int
	}{
		{"already due", now, 0},
		{"in the past", now.Add(-time.Hour), 0},
		{"a sliver", now.Add(time.Millisecond), 1},
		{"just under a second", now.Add(900 * time.Millisecond), 1},
		{"exactly a second", now.Add(time.Second), 1},
		{"four and a half", now.Add(4500 * time.Millisecond), 5},
		{"five", now.Add(5 * time.Second), 5},
	} {
		if got := secondsUntil(tc.at, now); got != tc.want {
			t.Errorf("%s: secondsUntil = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestTheGuideAdvertisesDeviceFlowForExactlyThePlatformsThatHaveIt is the wire
// between the capability and the button.
//
// The UI decides whether to offer "connect without a browser" from this field,
// and reading it off DeviceFor rather than a hard-coded platform name is what
// makes the answer follow the code: a build that added YouTube's device flow
// would start offering the button with no UI change, and one that dropped
// Twitch's would stop.
func TestTheGuideAdvertisesDeviceFlowForExactlyThePlatformsThatHaveIt(t *testing.T) {
	_, h, _, sign := deviceFlowServer(t)
	raw := send(t, h, sign, http.MethodGet, "/api/v1/platforms/guides", nil, http.StatusOK)

	var guides []oauth.SetupGuide
	if err := json.Unmarshal(raw, &guides); err != nil {
		t.Fatalf("decode guides: %v (%s)", err, raw)
	}
	if len(guides) == 0 {
		t.Fatal("no guides, so the loop below asserts nothing")
	}
	for _, g := range guides {
		_, want := oauth.DeviceFor(g.Platform)
		if g.DeviceFlow != want {
			t.Errorf("the %s guide reports deviceFlow=%v and DeviceFor says %v. The UI offers "+
				"the device-code button off this field, so a disagreement is a button that "+
				"cannot work or a capability nobody can reach", g.Platform, g.DeviceFlow, want)
		}
	}
}
