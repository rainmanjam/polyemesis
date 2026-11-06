package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// xStub returns a provider pointed at a test server, plus the server.
//
// Every X call -- consent, token, Livestream and users/me -- resolves through
// the per-instance base, so a stray real request would make this suite depend
// on the network and on an X developer app nobody running these tests has.
// Livestream access is OFF unless a test turns it on, which is the production
// default and the first thing most tests check.
func xStub(t *testing.T, h http.HandlerFunc) (*X, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewX(WithBaseURL(srv.URL)), srv
}

// xStubLive is xStub with the Livestream gate switched on, for the tests that
// exercise the Livestream family itself.
func xStubLive(t *testing.T, h http.HandlerFunc) (*X, *httptest.Server) {
	t.Helper()
	x, srv := xStub(t, h)
	return x.WithLivestreamAccess(), srv
}

// xCountingStub counts every request that reaches the server. The gate tests
// use it to prove that a switched-off Livestream makes NO request at all, not
// merely a failing one.
func xCountingStub(t *testing.T) (*X, *int32) {
	t.Helper()
	var hits int32
	x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "should not be reached", http.StatusTeapot)
	})
	return x, &hits
}

// Fixtures transcribed from X's served OpenAPI spec (api.x.com/2/openapi.json,
// version 2.173, read 2026-10-31) and the Livestream docs migration guide (read 2026-11-06), not invented. A rename in a decode struct
// fails here instead of returning an empty account at connect time.
const xMeFixture = `{"data": {"id": "1234567890", "name": "Sunday Practice", "username": "sundaypractice"}}`

// The source list is GET /2/sources: data is an array, and no source carries its stream key (the key is read from
// GET /2/sources/{id}/stream_key). Source ids are 19 digits and are not secret.
const xSourcesFixture = `{"data": [
  {"id": "9123456789012345678", "name": "polyemesis", "region": "us-east-1",
   "rtmp_url": "rtmp://ingest.example.invalid/live", "rtmps_url": "rtmps://ingest.example.invalid:443/live",
   "is_stream_active": false, "created_at_ms": "1772029802844"}
], "meta": {"result_count": 1}}`

const xEmptySourcesFixture = `{"data": [], "meta": {"result_count": 0}}`

const xBroadcastListFixture = `{
  "data": [
    {
      "id": "1ynJOZVeajOJR",
      "broadcast_id": "1ynJOZVeajOJR",
      "twitter_user_id": "1234567890",
      "source_id": "9123456789012345678",
      "title": "Sunday practice",
      "share_url": "https://x.com/i/broadcasts/1ynJOZVeajOJR",
      "media_key": "13_1729384756",
      "state": "RUNNING",
      "start_ms": "1755302400000",
      "end_ms": "",
      "total_watching": "17",
      "total_watched": "402",
      "chat_option": 1,
      "width": 1920,
      "height": 1080
    }
  ],
  "meta": {"result_count": 1}
}`

// xSecret builds a credential-shaped value at runtime. gitleaks forbids
// literal key shapes in test fixtures, so the key is assembled here.
func xSecret(parts ...string) string { return strings.Join(parts, "") }

// ---------------------------------------------------------------- the surface

// TestXAuthURLSendsPKCEAndTheScopesLivestreamAndClipPublishingNeed pins the consent
// request. PKCE is ON: X's authorization-code flow documents PKCE as the
// recommended flow for the Livestream API, so the challenge reaches the wire
// and the verifier is sent at exchange.
func TestXAuthURLSendsPKCEAndTheFiveScopesTheLivestreamFamilyNeeds(t *testing.T) {
	x := &X{}
	if !x.PKCE() {
		t.Fatal("PKCE() is false. X's Livestream authentication page documents " +
			"OAuth 2.0 Authorization Code Flow with PKCE as the recommended flow")
	}
	raw := x.AuthURL("client-id", "https://polyemesis.test/api/v1/oauth/x/callback", "state-123", "a-challenge")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthURL produced an unparseable URL %q: %v", raw, err)
	}
	if got, want := u.Scheme+"://"+u.Host+u.Path, "https://api.x.com/2/oauth2/authorize"; got != want {
		t.Errorf("AuthURL points at %q, want %q", got, want)
	}
	q := u.Query()
	if got := q.Get("code_challenge"); got != "a-challenge" {
		t.Errorf("code_challenge = %q, want the challenge handed to AuthURL", got)
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	granted := strings.Fields(q.Get("scope"))
	sort.Strings(granted)
	want := []string{"broadcast.read", "broadcast.write", "media.write", "offline.access", "tweet.read", "tweet.write", "users.read"}
	if !reflect.DeepEqual(granted, want) {
		t.Errorf("scope = %v, want exactly %v. broadcast.read/write are the Livestream family; "+
			"offline.access issues a refresh token; users.read and tweet.read let GET /2/users/me "+
			"and the broadcast.chat activity subscription succeed; tweet.write and media.write post clips", granted, want)
	}
}

// TestXScopesAreTheListTheLedgerRecords keeps Scopes and ScopeVersion honest
// against the append-only ledger. Version 2 added tweet.write and media.write so
// clips can be posted; an account connected at version 1 reconnects once.
func TestXScopesAreTheListTheLedgerRecords(t *testing.T) {
	x := &X{}
	if got := x.ScopeVersion(); got != 2 {
		t.Errorf("ScopeVersion() = %d, want 2: version 2 asks for the two publish scopes, and "+
			"an account on version 1 has to be told to reconnect", got)
	}
	if got := x.Platform(); got != db.PlatformX {
		t.Errorf("Platform() = %q, want db.PlatformX", got)
	}
}

// TestXReachesNoRealHost is the escape guard: with a base URL set, no call may
// leave for api.x.com.
func TestXReachesNoRealHost(t *testing.T) {
	x, srv := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Host, "x.com") {
			t.Errorf("request reached a real X host: %s", r.Host)
		}
		_, _ = io.WriteString(w, xMeFixture)
	})
	if _, err := x.Account(context.Background(), "client", "tok"); err != nil {
		t.Fatalf("Account: %v", err)
	}
	if !strings.HasPrefix(srv.URL, "http://127.0.0.1") {
		t.Fatalf("test server is not local: %s", srv.URL)
	}
}

// -------------------------------------------------------------- token exchange

// TestXExchangeSendsTheVerifierBecausePKCEIsOn is the other half of PKCE: the
// verifier that matched the challenge must reach the token endpoint, or X
// refuses the exchange with a message that does not mention PKCE.
func TestXExchangeSendsTheVerifierBecausePKCEIsOn(t *testing.T) {
	var got url.Values
	x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":7200,"scope":"broadcast.read"}`)
	})
	tok, err := x.Exchange(context.Background(), "client", "secret", "https://cb", "code-1", "the-verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got.Get("code_verifier") != "the-verifier" {
		t.Errorf("code_verifier = %q, want the verifier that matched the challenge", got.Get("code_verifier"))
	}
	if tok.RefreshToken != "r" {
		t.Errorf("refresh token = %q; offline.access must yield one", tok.RefreshToken)
	}
}

// --------------------------------------------------------------------- account

// TestXAccountReadsTheHandleFromUsersMe replaces the earlier identity-from-a-
// broadcast approach. With users.read granted, /2/users/me returns a handle.
func TestXAccountReadsTheHandleFromUsersMe(t *testing.T) {
	x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2/users/me" {
			t.Errorf("Account read %s, want /2/users/me", r.URL.Path)
		}
		_, _ = io.WriteString(w, xMeFixture)
	})
	acct, err := x.Account(context.Background(), "client", "tok")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acct.Ref != "1234567890" {
		t.Errorf("Ref = %q, want the numeric user id", acct.Ref)
	}
	if acct.Name != "sundaypractice" {
		t.Errorf("Name = %q, want the handle without a leading @", acct.Name)
	}
}

// TestXAccountDoesNotNeedABroadcastToConnect is the defect the old code had: an
// account that had never broadcast could not connect, because the id was read
// off a broadcast. /2/users/me needs no broadcast.
func TestXAccountDoesNotNeedABroadcastToConnect(t *testing.T) {
	x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/2/broadcasts") {
			t.Errorf("Account asked for broadcasts (%s); it must not need one", r.URL.Path)
		}
		_, _ = io.WriteString(w, xMeFixture)
	})
	if _, err := x.Account(context.Background(), "client", "tok"); err != nil {
		t.Fatalf("an account with no broadcasts could not connect: %v", err)
	}
}

// ------------------------------------------------------------------ the gate

// TestXLivestreamIsSwitchedOffByDefaultAndMakesNoRequest is the gate. While the operator has not switched
// POLYEMESIS_X_LIVESTREAM on, every Livestream operation refuses WITHOUT a request: a request that fails with 403 would
// be a refusal the operator cannot interpret, and it would look like a bug in polyemesis.
func TestXLivestreamIsSwitchedOffByDefaultAndMakesNoRequest(t *testing.T) {
	x, hits := xCountingStub(t)
	if x.LivestreamAccess() {
		t.Fatal("Livestream access is on by default; it must stay opt-in")
	}
	ctx := context.Background()
	if _, err := x.Ingest(ctx, "client", "tok"); !errors.Is(err, ErrNoStreamKeyAPI) {
		t.Errorf("Ingest with the gate off = %v, want ErrNoStreamKeyAPI so the operator is sent to the paste field", err)
	}
	if _, err := x.StreamKeys(ctx, "client", "tok"); err == nil {
		t.Error("StreamKeys with the gate off returned no error")
	}
	if _, err := x.BroadcastState(ctx, "tok", "1ynJOZVeajOJR"); err == nil {
		t.Error("BroadcastState with the gate off returned no error")
	}
	// The connect-time probe is a Livestream-family read too. With the gate off it must refuse without a request, and
	// its refusal must name the gate, so the connect warning shows the honest copy and not a probe failure.
	err := x.CheckEntitlement(ctx, "client", "tok")
	if !errors.Is(err, ErrNotEntitled) || !errors.Is(err, ErrXLivestreamOff) {
		t.Errorf("CheckEntitlement with the gate off = %v, want it to match ErrNotEntitled and ErrXLivestreamOff", err)
	}
	// The connect warning shows Error(). It must say how to switch Livestream on, and it must not blame the operator's
	// account for a plan it does not have, or send them to an X access form that no longer exists.
	if err != nil && (!strings.Contains(err.Error(), "needs X Livestream switched on (POLYEMESIS_X_LIVESTREAM=1)") ||
		strings.Contains(err.Error(), "paid tier") || strings.Contains(err.Error(), "does not have") ||
		strings.Contains(strings.ToLower(err.Error()), "whitelist")) {
		t.Errorf("CheckEntitlement message = %q, want the switch-on gate copy and no whitelist or account-plan wording", err.Error())
	}
	if _, err := x.TransitionBroadcast(ctx, "tok", "1ynJOZVeajOJR", PhaseLive); err == nil {
		t.Error("TransitionBroadcast with the gate off returned no error")
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("%d request(s) reached X with the Livestream gate off; there must be none", n)
	}
}

// TestXGateReasonNamesTheSwitchNotAWhitelist names the gate in the platform's own terms, usable before anything is
// connected. X's Livestream docs list no access form any more, so the copy must not send anyone to one.
func TestXGateReasonNamesTheSwitchNotAWhitelist(t *testing.T) {
	x := &X{}
	reason := x.EntitlementReason()
	if xGateCopy != "needs X Livestream switched on (POLYEMESIS_X_LIVESTREAM=1)" {
		t.Errorf("xGateCopy = %q, want the neutral switch-on wording", xGateCopy)
	}
	for _, want := range []string{"needs X Livestream switched on", "POLYEMESIS_X_LIVESTREAM=1"} {
		if !strings.Contains(reason, want) {
			t.Errorf("EntitlementReason() = %q, want it to contain %q", reason, want)
		}
	}
	for _, banned := range []string{"whitelist", "access form", "Request Livestream"} {
		if strings.Contains(strings.ToLower(reason), strings.ToLower(banned)) {
			t.Errorf("EntitlementReason() = %q still carries %q", reason, banned)
		}
	}
	if !strings.Contains(x.ManualKeyReason(), "producer") {
		t.Error("ManualKeyReason must still tell the operator where to paste the key from")
	}
	if strings.Contains(strings.ToLower(x.ManualKeyReason()), "whitelist") {
		t.Errorf("ManualKeyReason() = %q still names a whitelist", x.ManualKeyReason())
	}
}

// TestXEntitlementProbeMapsAGateRefusalToNotEntitled checks the three outcomes the EntitlementGated contract names. A
// 403 or 412 is a refusal of the Livestream family; a 500 is not, and must never be reported as one. The probe reads
// GET /2/sources, which takes the user from the token.
func TestXEntitlementProbeMapsAGateRefusalToNotEntitled(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   bool // true means ErrNotEntitled
	}{
		{"forbidden is a refusal", http.StatusForbidden, true},
		{"precondition failed is the eligibility refusal", http.StatusPreconditionFailed, true},
		{"server error is not evidence of a gate", http.StatusInternalServerError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/2/sources" {
					t.Errorf("the probe sent %s %s, want GET /2/sources", r.Method, r.URL.Path)
				}
				http.Error(w, `{"title":"Forbidden"}`, tc.status)
			})
			x = x.WithLivestreamAccess()
			err := x.CheckEntitlement(context.Background(), "client", "tok")
			if got := errors.Is(err, ErrNotEntitled); got != tc.want {
				t.Errorf("CheckEntitlement(%d) errors.Is(ErrNotEntitled) = %v, want %v (err: %v)",
					tc.status, got, tc.want, err)
			}
		})
	}
}

// TestXEntitlementProbeSucceedsWhenTheLivestreamFamilyAnswers is the nil outcome, and it needs no /2/users/me.
func TestXEntitlementProbeSucceedsWhenTheLivestreamFamilyAnswers(t *testing.T) {
	f := newXFake(t)
	f.sources = xEmptySourcesFixture
	if err := f.x.CheckEntitlement(context.Background(), "client", "tok"); err != nil {
		t.Fatalf("CheckEntitlement against an answering Livestream family = %v, want nil", err)
	}
	if got := f.requests(); !reflect.DeepEqual(got, []string{"GET /2/sources"}) {
		t.Errorf("the probe made %v, want exactly GET /2/sources", got)
	}
}

// ----------------------------------------------------------------- stream key

// TestXIngestMintsASourceWhenNoneExists is the source-minted key. With the gate on and no polyemesis source yet, the
// recommended region is read, a source is created with exactly {name, region}, and its own rtmps URL and stream_key are
// returned. The host is read from the response, never invented.
func TestXIngestMintsASourceWhenNoneExists(t *testing.T) {
	f := newXFake(t)
	f.sources = xEmptySourcesFixture
	ing, err := f.x.Ingest(context.Background(), "client", "tok")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	want := []string{"GET /2/sources", "GET /2/region", `POST /2/sources {"name":"polyemesis","region":"us-east-1"}`}
	if got := f.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("Ingest made %v, want %v", got, want)
	}
	if ing.Key != "minted-key" {
		t.Errorf("Key = %q, want the stream_key X minted", ing.Key)
	}
	if ing.URL != "rtmps://ingest.example.invalid:443/live" {
		t.Errorf("URL = %q, want the rtmps_url X returned verbatim", ing.URL)
	}
}

// TestXIngestReusesThePolyemesisSourceInsteadOfMintingAnother keeps the source list from growing one entry per connect.
// The list no longer carries keys, so the key is read from the source's stream_key route.
func TestXIngestReusesThePolyemesisSourceInsteadOfMintingAnother(t *testing.T) {
	f := newXFake(t)
	ing, err := f.x.Ingest(context.Background(), "client", "tok")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if ing.Key != xKey {
		t.Errorf("Key = %q, want the existing source's stream key", ing.Key)
	}
	want := []string{"GET /2/sources", "GET /2/sources/" + xSourceID + "/stream_key"}
	if got := f.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("Ingest made %v, want %v (and no POST)", got, want)
	}
}

// TestXKeyNeverAppearsInAnErrorMessage is the leak test. A failed create that echoes a key in its body must not carry
// that key into the error an operator sees, a log line or an event.
func TestXKeyNeverAppearsInAnErrorMessage(t *testing.T) {
	key := xSecret("leak", "canary", "9z")
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/2/sources":
			_, _ = io.WriteString(w, xEmptySourcesFixture)
		case r.URL.Path == "/2/region":
			_, _ = io.WriteString(w, `{"region":"us-east-1"}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errors":[{"title":"Invalid request","detail":"name is required"}],"data":{"stream_key":"`+key+`"}}`)
		}
	})
	_, err := x.Ingest(context.Background(), "client", "tok")
	if err == nil {
		t.Fatal("Ingest returned no error for a refused create")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("the stream key appeared in the error: %v", err)
	}
}

// TestXStreamKeysListsEverySourceKeyForTheCrossCheck is the read-only reader: the list for the sources, then each
// source's own stream_key route, because the list no longer carries keys.
func TestXStreamKeysListsEverySourceKeyForTheCrossCheck(t *testing.T) {
	f := newXFake(t)
	keys, err := f.x.StreamKeys(context.Background(), "client", "tok")
	if err != nil {
		t.Fatalf("StreamKeys: %v", err)
	}
	if !reflect.DeepEqual(keys, []string{xKey}) {
		t.Errorf("StreamKeys = %v, want the one source key", keys)
	}
}

// TestXNoRequestEverHitsAUserScopedSourceOrBroadcastRoute is the removal pin. X removes /2/users/{id}/sources and
// /2/users/{id}/broadcasts (and their sub-routes) on 2027-01-01, and they 404 from then on. The fake fails the test on
// any such path, so a call site that slips back to the old routes fails here. /2/users/me stays: it is the account read.
func TestXNoRequestEverHitsAUserScopedSourceOrBroadcastRoute(t *testing.T) {
	f := newXFake(t)
	ctx := context.Background()
	two := 2
	steps := map[string]func() error{
		"CheckEntitlement": func() error { return f.x.CheckEntitlement(ctx, "c", "tok") },
		"Ingest":           func() error { _, err := f.x.Ingest(ctx, "c", "tok"); return err },
		"StreamKeys":       func() error { _, err := f.x.StreamKeys(ctx, "c", "tok"); return err },
		"Broadcasts":       func() error { _, err := f.x.Broadcasts(ctx, "c", "tok"); return err },
		"BroadcastByID":    func() error { _, err := f.x.BroadcastByID(ctx, "c", "tok", xBroadcastID); return err },
		"BroadcastState":   func() error { _, err := f.x.BroadcastState(ctx, "tok", xBroadcastID); return err },
		"OpenBroadcast":    func() error { _, err := f.x.OpenBroadcast(ctx, "tok", xKey); return err },
		"Publish": func() error {
			_, err := f.x.PublishBroadcast(ctx, "tok", xPendingHandle(xSourceID), XPublish{Title: "T", ChatOption: &two})
			return err
		},
		"Live": func() error {
			_, err := f.x.TransitionBroadcast(ctx, "tok", xPendingHandle(xSourceID), PhaseLive)
			return err
		},
		"End": func() error { _, err := f.x.TransitionBroadcast(ctx, "tok", xBroadcastID, PhaseComplete); return err },
	}
	for name, step := range steps {
		if err := step(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, req := range f.requests() {
		if xOldRoute.MatchString(strings.Fields(req)[1]) {
			t.Errorf("request %q hit a user-scoped route X removes on 2027-01-01", req)
		}
	}
	if len(f.requests()) == 0 {
		t.Fatal("no requests recorded; the pin proves nothing")
	}
}

// ---------------------------------------------------------------- lifecycle

// TestXGoLiveIsOneCallAndEndIsEndAndTestingIsRefused maps X's state machine onto the three documented phases. X has no
// testing state, so asking for one is a refusal that names the reason. Going live is ONE call, POST /2/broadcasts, and
// its result carries the real broadcast id, which is not the pending handle the coordinator held.
func TestXGoLiveIsOneCallAndEndIsEndAndTestingIsRefused(t *testing.T) {
	f := newXFake(t)
	ctx := context.Background()
	handle := xPendingHandle(xSourceID)

	if _, err := f.x.TransitionBroadcast(ctx, "tok", handle, PhaseTesting); err == nil {
		t.Error("X has no testing state; a testing transition must be refused")
	}
	live, err := f.x.TransitionBroadcast(ctx, "tok", handle, PhaseLive)
	if err != nil {
		t.Fatalf("go live: %v", err)
	}
	if live.BroadcastID != xBroadcastID || live.Status != XStateRunning {
		t.Errorf("go live = %+v, want the real broadcast id %s RUNNING", live, xBroadcastID)
	}
	end, err := f.x.TransitionBroadcast(ctx, "tok", xBroadcastID, PhaseComplete)
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if end.Status != XStateEnded {
		t.Errorf("end status = %q, want ENDED", end.Status)
	}
	want := []string{
		`POST /2/broadcasts {"source_id":"` + xSourceID + `"}`,
		`PUT /2/broadcasts/` + xBroadcastID + `/state {"state":"END"}`,
	}
	if got := f.requests(); !reflect.DeepEqual(got, want) {
		t.Errorf("writes = %v, want %v", got, want)
	}
}

// TestXBroadcastStateReadsTheSourceAndTheWord is the read side. The state word is passed through unmapped, because X
// documents its states and an operator comparing them against X's own page must see the same word.
func TestXBroadcastStateReadsTheSourceAndTheWord(t *testing.T) {
	f := newXFake(t)
	st, err := f.x.BroadcastState(context.Background(), "tok", xBroadcastID)
	if err != nil {
		t.Fatalf("BroadcastState: %v", err)
	}
	if st.Status != "RUNNING" || st.BoundStreamID != xSourceID || st.Title != "Sunday practice" {
		t.Errorf("BroadcastState = %+v", st)
	}
	if st.MonitorStream != nil {
		t.Error("X publishes no monitor-stream read, so that precondition must stay UNKNOWN (nil), not false")
	}
}

// TestXMetadataCannotChangeALiveBroadcastAndSaysSo: the title and chat option are written only when the broadcast goes
// live. A metadata push must not claim it changed a broadcast that is already running.
func TestXMetadataCannotChangeALiveBroadcastAndSaysSo(t *testing.T) {
	x := &X{}
	caps := x.MetadataCaps()
	if len(caps.Fields) != 0 {
		t.Errorf("MetadataCaps().Fields = %v, want none: X sets the title only at go-live", caps.Fields)
	}
	res, err := x.PushMetadata(context.Background(), "client", "tok", "1234567890",
		Metadata{Title: "New title"})
	if err == nil {
		t.Fatal("PushMetadata reported success for a title X cannot change on a live broadcast")
	}
	if !strings.Contains(err.Error(), "go live") && !strings.Contains(err.Error(), "going live") {
		t.Errorf("PushMetadata error = %q, want it to say the title is set when the broadcast goes live", err)
	}
	if res != nil && len(res.Applied) != 0 {
		t.Errorf("PushMetadata claimed to apply %v", res.Applied)
	}
}

// ------------------------------------------------------------ helpers / shape

// TestXBroadcastListDecodesTheSpecFields is the shape pin for the list read, including the viewer counts that must stay
// undecoded.
func TestXBroadcastListDecodesTheSpecFields(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, xBroadcastListFixture)
	})
	bs, err := x.Broadcasts(context.Background(), "client", "tok")
	if err != nil {
		t.Fatalf("Broadcasts: %v", err)
	}
	if len(bs) != 1 || bs[0].State != "RUNNING" || bs[0].SourceID != xSourceID {
		t.Fatalf("decoded = %+v", bs)
	}
	raw, _ := json.Marshal(bs[0])
	if strings.Contains(string(raw), "total_watching") {
		t.Error("the viewer counts are undescribed by X and must not be decoded into a count")
	}
}

// TestXAsksForEveryFieldItDecodes pins the broadcast.fields query to the struct.
func TestXAsksForEveryFieldItDecodes(t *testing.T) {
	for _, f := range []string{"id", "broadcast_id", "twitter_user_id", "source_id", "title", "share_url", "media_key", "state", "start_ms", "end_ms"} {
		if !strings.Contains(xBroadcastFields, f) {
			t.Errorf("broadcast.fields does not request %q", f)
		}
	}
}

// TestXRejectsAnEmptyBroadcastID stops a lookup against "whichever broadcast".
func TestXRejectsAnEmptyBroadcastID(t *testing.T) {
	x, hits := xCountingStub(t)
	if _, err := x.BroadcastByID(context.Background(), "client", "tok", "  "); err == nil {
		t.Error("an empty broadcast id was accepted")
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Error("an empty id still reached X")
	}
}

// ------------------------------------------------ opening and going live

// TestXOpenBroadcastCreatesNothingOnXAndReturnsAPendingHandle: Go Live is public the moment it returns and ends the
// broadcast as TIMED_OUT after five minutes without video, so opening must NOT call it. Opening resolves the stored
// stream key to the source id and returns a handle; nothing exists on X yet.
func TestXOpenBroadcastCreatesNothingOnXAndReturnsAPendingHandle(t *testing.T) {
	f := newXFake(t)
	id, err := f.x.OpenBroadcast(context.Background(), "tok", xKey)
	if err != nil {
		t.Fatalf("OpenBroadcast: %v", err)
	}
	if id != xPendingHandle(xSourceID) {
		t.Errorf("OpenBroadcast = %q, want the pending handle for source %s", id, xSourceID)
	}
	for _, req := range f.requests() {
		if strings.HasPrefix(req, "POST") || strings.HasPrefix(req, "PUT") {
			t.Errorf("OpenBroadcast wrote to X (%q); a broadcast must not exist before go-live", req)
		}
	}
}

// TestXOpenBroadcastRefusesAMalformedOrUnknownKey: a key outside the stream key shape is refused before any byte leaves
// the process, and a key no source holds is refused in words that never echo it.
func TestXOpenBroadcastRefusesAMalformedOrUnknownKey(t *testing.T) {
	var hits int32
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "should not be reached", http.StatusTeapot)
	})
	for _, bad := range []string{"", "has space", "way-too-long-" + strings.Repeat("a", 40), "k3y/../x"} {
		if _, err := x.OpenBroadcast(context.Background(), "tok", bad); err == nil {
			t.Errorf("OpenBroadcast(%q) succeeded, want refusal", bad)
		}
	}
	if hits != 0 {
		t.Errorf("malformed keys caused %d requests, want none", hits)
	}
	f := newXFake(t)
	unknown := xSecret("unk", "nown", "K3y")
	_, err := f.x.OpenBroadcast(context.Background(), "tok", unknown)
	if err == nil {
		t.Fatal("a key no source holds was accepted")
	}
	if strings.Contains(err.Error(), unknown) {
		t.Errorf("the unknown key appeared in the error: %v", err)
	}
}

// TestXCreateAndPublishMakeNoRequestWhileTheGateIsClosed: the switched-off Livestream family refuses opening and going
// live without a request, like every other Livestream operation.
func TestXCreateAndPublishMakeNoRequestWhileTheGateIsClosed(t *testing.T) {
	x, hits := xCountingStub(t)
	ctx := context.Background()
	if _, err := x.OpenBroadcast(ctx, "tok", "k3y0ne"); !errors.Is(err, ErrXLivestreamOff) {
		t.Errorf("OpenBroadcast with the gate closed = %v, want ErrXLivestreamOff", err)
	}
	if _, err := x.PublishBroadcast(ctx, "tok", "1ynJOZVeajOJR", XPublish{Title: "Sunday"}); !errors.Is(err, ErrXLivestreamOff) {
		t.Errorf("PublishBroadcast with the gate closed = %v, want ErrXLivestreamOff", err)
	}
	if *hits != 0 {
		t.Errorf("closed gate made %d requests, want none", *hits)
	}
}

// TestXPublishSendsTheTitleAndChatOptionInOneGoLiveCall: the title and chat option travel in the POST /2/broadcasts
// body with the SOURCE ID (not the stream key), as a JSON integer, and with no state field: the route rejects unknown
// fields.
func TestXPublishSendsTheTitleAndChatOptionInOneGoLiveCall(t *testing.T) {
	f := newXFake(t)
	two := 2
	res, err := f.x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID),
		XPublish{Title: "  Sunday practice  ", ChatOption: &two})
	if err != nil {
		t.Fatalf("PublishBroadcast: %v", err)
	}
	want := `POST /2/broadcasts {"chat_option":2,"source_id":"` + xSourceID + `","title":"Sunday practice"}`
	if got := f.requests(); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("go-live = %v, want [%s]", got, want)
	}
	if res.State != "RUNNING" || res.ID != xBroadcastID {
		t.Errorf("PublishBroadcast = %+v, want id %s RUNNING", res, xBroadcastID)
	}
}

// TestXPublishOmitsTitleAndChatOptionWhenUnset: an unset chat option is left out of the body so X keeps its own
// default, and an empty title is not sent as a blank title.
func TestXPublishOmitsTitleAndChatOptionWhenUnset(t *testing.T) {
	f := newXFake(t)
	if _, err := f.x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID), XPublish{}); err != nil {
		t.Fatalf("PublishBroadcast: %v", err)
	}
	if got, want := f.requests(), []string{`POST /2/broadcasts {"source_id":"` + xSourceID + `"}`}; !reflect.DeepEqual(got, want) {
		t.Errorf("go-live body = %v, want %v", got, want)
	}
}

// TestXPublishSuppressesTheAnnouncementPostOnlyWhenAsked: should_not_tweet travels as a JSON boolean true when the
// announcement is to be suppressed, and is left out otherwise, so X's default of posting is what an unset field means.
//
// MUTATION: the should_not_tweet line in PublishBroadcast removed. Expected: FAIL, the body had no should_not_tweet.
func TestXPublishSuppressesTheAnnouncementPostOnlyWhenAsked(t *testing.T) {
	f := newXFake(t)
	if _, err := f.x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID),
		XPublish{Title: "Sunday practice", ShouldNotTweet: true}); err != nil {
		t.Fatalf("PublishBroadcast: %v", err)
	}
	if got := f.requests(); len(got) != 1 || !strings.Contains(got[0], `"should_not_tweet":true`) {
		t.Fatalf("go-live body = %v, want should_not_tweet:true", got)
	}
	f.reset()
	if _, err := f.x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID),
		XPublish{Title: "Sunday practice"}); err != nil {
		t.Fatalf("PublishBroadcast: %v", err)
	}
	if got := f.requests(); len(got) != 1 || strings.Contains(got[0], "should_not_tweet") {
		t.Fatalf("go-live body = %v, want no should_not_tweet when the post is wanted", got)
	}
}

// TestXPublishRefusesAChatOptionOutsideZeroToSixWithoutARequest: X answers 400 to anything outside 0 to 6, so the
// refusal is made locally and costs nothing.
func TestXPublishRefusesAChatOptionOutsideZeroToSixWithoutARequest(t *testing.T) {
	var hits int32
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "should not be reached", http.StatusTeapot)
	})
	for _, bad := range []int{-1, 7, 99} {
		v := bad
		if _, err := x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID), XPublish{ChatOption: &v}); err == nil {
			t.Errorf("chat option %d accepted, want refusal", bad)
		}
	}
	if hits != 0 {
		t.Errorf("out-of-range chat options caused %d requests", hits)
	}
}

// TestXPublishOfARecordedBroadcastReadsItsSourceFirst: a destination recorded before this change holds a REAL
// NOT_STARTED broadcast id made on the old create route. The new routes cannot publish it, but they can take its source
// live, so the source id is read from the broadcast and Go Live is called with it.
func TestXPublishOfARecordedBroadcastReadsItsSourceFirst(t *testing.T) {
	f := newXFake(t)
	f.broadcastState = "NOT_STARTED"
	res, err := f.x.PublishBroadcast(context.Background(), "tok", "legacyId1234", XPublish{})
	if err != nil {
		t.Fatalf("PublishBroadcast: %v", err)
	}
	got := f.requests()
	if len(got) != 2 || !strings.HasPrefix(got[0], "GET /2/broadcasts/legacyId1234") ||
		got[1] != `POST /2/broadcasts {"source_id":"`+xSourceID+`"}` {
		t.Errorf("requests = %v, want a read of the broadcast then Go Live on its source", got)
	}
	if res.ID != xBroadcastID {
		t.Errorf("ID = %q, want the new real broadcast id", res.ID)
	}
}

// TestXGoLiveExplainsTheConflictsAndTheIneligibleAccount: X answers Go Live 409 for two different reasons and 403 for an
// account that cannot go live. Each must reach the operator in words that name the cause, with X's own detail kept.
func TestXGoLiveExplainsTheConflictsAndTheIneligibleAccount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"conflict", http.StatusConflict, `{"type":"https://api.x.com/2/problems/conflict","title":"Conflict","detail":"Another broadcast 1abc is live on this source.","status":409}`, "Another broadcast 1abc is live"},
		{"ineligible", http.StatusForbidden, `{"type":"about:blank","title":"Forbidden","detail":"This account is not eligible to go live.","status":403}`, "not eligible to go live"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID), XPublish{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PublishBroadcast = %v, want X's detail %q", err, tc.want)
			}
			var se *xStatusError
			if !errors.As(err, &se) || se.Status != tc.status {
				t.Errorf("error %v no longer unwraps to the X status error %d", err, tc.status)
			}
		})
	}
}

// TestXSixBroadcastStatesAreKnownAndAnythingElseIsNot: X documents six states. An unrecognised word must not be treated
// as live, because a wrong "live" would stop the coordinator from ever publishing.
func TestXSixBroadcastStatesAreKnownAndAnythingElseIsNot(t *testing.T) {
	for _, s := range []string{XStateNotStarted, XStatePrePublished, XStateRunning, XStateEnded, XStateTimedOut, XStateCanceled} {
		if !XStateKnown(s) {
			t.Errorf("XStateKnown(%q) = false, want true: X documents it", s)
		}
	}
	for _, s := range []string{"", "live", "RUNNING ", "running", "STARTED", "PUBLISHED"} {
		if XStateKnown(s) {
			t.Errorf("XStateKnown(%q) = true, want false: only X's exact six words are known", s)
		}
	}
}

// TestXBroadcastStateOfAPendingHandleIsNotStartedAndReadsTheSource: nothing exists on X for a pending handle, so its
// state is a synthetic NOT_STARTED, and whether it may go live is the source's own is_stream_active. Publishing before
// bytes arrive would announce a blank broadcast.
func TestXBroadcastStateOfAPendingHandleIsNotStartedAndReadsTheSource(t *testing.T) {
	for _, active := range []bool{true, false} {
		f := newXFake(t)
		f.active = active
		st, err := f.x.BroadcastState(context.Background(), "tok", xPendingHandle(xSourceID))
		if err != nil {
			t.Fatalf("BroadcastState: %v", err)
		}
		if st.Status != XStateNotStarted || st.BoundStreamID != xSourceID {
			t.Errorf("BroadcastState = %+v, want NOT_STARTED bound to source %s", st, xSourceID)
		}
		if st.StreamActive == nil || *st.StreamActive != active {
			t.Errorf("StreamActive = %v, want %v from the source's is_stream_active", st.StreamActive, active)
		}
		for _, req := range f.requests() {
			if strings.Contains(req, "/2/broadcasts") {
				t.Errorf("a pending handle read asked X about a broadcast (%q) that does not exist", req)
			}
		}
	}
}

// TestXBroadcastStateReportsWhetherTheSourceIsReceivingBytes: a real broadcast's source carries is_stream_active, matched
// by SOURCE ID (the stream key is no longer an id).
func TestXBroadcastStateReportsWhetherTheSourceIsReceivingBytes(t *testing.T) {
	f := newXFake(t)
	f.active = true
	st, err := f.x.BroadcastState(context.Background(), "tok", xBroadcastID)
	if err != nil {
		t.Fatalf("BroadcastState: %v", err)
	}
	if st.StreamActive == nil || !*st.StreamActive {
		t.Errorf("StreamActive = %v, want true from the source's is_stream_active", st.StreamActive)
	}
}

// TestXPendingHandleRoundTripsAndNeverMistakesARealID: the handle must be recognised only by its own prefix with a
// source id after it, so a real broadcast id is never read as one.
func TestXPendingHandleRoundTripsAndNeverMistakesARealID(t *testing.T) {
	h := xPendingHandle(xSourceID)
	if id, ok := xPendingSource(h); !ok || id != xSourceID {
		t.Errorf("xPendingSource(%q) = %q, %v", h, id, ok)
	}
	for _, real := range []string{"1ynJOZVeajOJR", "", "pending:", "pending:abc", "xpending:" + xSourceID} {
		if _, ok := xPendingSource(real); ok {
			t.Errorf("xPendingSource(%q) read a real id as a pending handle", real)
		}
	}
}

// TestXEndingAPendingHandleSendsNothing: no broadcast exists on X for it, so there is nothing to end.
func TestXEndingAPendingHandleSendsNothing(t *testing.T) {
	f := newXFake(t)
	res, err := f.x.TransitionBroadcast(context.Background(), "tok", xPendingHandle(xSourceID), PhaseComplete)
	if err != nil {
		t.Fatalf("end of a pending handle: %v", err)
	}
	if !res.Redundant {
		t.Errorf("end of a pending handle = %+v, want Redundant: nothing was on air", res)
	}
	if got := f.requests(); len(got) != 0 {
		t.Errorf("ending a pending handle made requests %v", got)
	}
}

// TestXGoLiveReturnsXsErrorsOnASuccessfulStatusWithNoBroadcast: X answers some failures 200 with an errors array and no
// broadcast. Reading only the state would report a go-live that did nothing as done.
func TestXGoLiveReturnsXsErrorsOnASuccessfulStatusWithNoBroadcast(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"title":"Forbidden","detail":"source cannot go live"}]}`)
	})
	if _, err := x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID), XPublish{}); err == nil ||
		!strings.Contains(err.Error(), "source cannot go live") {
		t.Errorf("PublishBroadcast on a 200 with errors and no broadcast = %v, want X's error", err)
	}
	if _, err := x.TransitionBroadcast(context.Background(), "tok", "1ynJOZVeajOJR", PhaseComplete); err == nil ||
		!strings.Contains(err.Error(), "source cannot go live") {
		t.Errorf("end on a 200 with errors and no state = %v, want X's error", err)
	}
}
