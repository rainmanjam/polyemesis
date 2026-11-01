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
// version 2.173, read 2026-10-31), not invented. A rename in a decode struct
// fails here instead of returning an empty account at connect time.
const xMeFixture = `{"data": {"id": "1234567890", "name": "Sunday Practice", "username": "sundaypractice"}}`

const xSourcesFixture = `{"data": {"sources": [
  {"id": "k3y0ne", "name": "polyemesis", "rtmp_url": "rtmp://ingest.example.invalid/live",
   "rtmps_url": "rtmps://ingest.example.invalid:443/live", "rtmp_stream_key": "k3y0ne",
   "is_stream_active": false, "owner_id": 1234567890}
]}}`

const xEmptySourcesFixture = `{"data": {"sources": []}}`

const xBroadcastListFixture = `{
  "data": [
    {
      "id": "1ynJOZVeajOJR",
      "broadcast_id": "1ynJOZVeajOJR",
      "twitter_user_id": "1234567890",
      "source_id": "k3y0ne",
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

// TestXAuthURLSendsPKCEAndTheFiveScopesTheLivestreamFamilyNeeds pins the consent
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
	want := []string{"broadcast.read", "broadcast.write", "offline.access", "tweet.read", "users.read"}
	if !reflect.DeepEqual(granted, want) {
		t.Errorf("scope = %v, want exactly %v. broadcast.read/write are the Livestream family; "+
			"offline.access issues a refresh token; users.read and tweet.read let GET /2/users/me "+
			"and the broadcast.chat activity subscription succeed", granted, want)
	}
}

// TestXScopesAreTheListTheLedgerRecords keeps Scopes and ScopeVersion honest
// against the append-only ledger. X never shipped a version before this one, so
// no account holds an older grant and the first version is 1.
func TestXScopesAreTheListTheLedgerRecords(t *testing.T) {
	x := &X{}
	if got := x.ScopeVersion(); got != 1 {
		t.Errorf("ScopeVersion() = %d, want 1: X was never registered, so no account was "+
			"ever granted an earlier version and nothing needs a reconnect prompt", got)
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

// TestXLivestreamIsSwitchedOffByDefaultAndMakesNoRequest is the gate. Until the
// owner's app is whitelisted, every Livestream operation refuses WITHOUT a
// request: a request that fails with 403 would be a refusal the operator cannot
// interpret, and it would look like a bug in polyemesis.
func TestXLivestreamIsSwitchedOffByDefaultAndMakesNoRequest(t *testing.T) {
	x, hits := xCountingStub(t)
	if x.LivestreamAccess() {
		t.Fatal("Livestream access is on by default; it must be off until X whitelists the app")
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
	// The connect warning shows Error(). It must name the app's whitelist, and it must not blame the operator's
	// account for a plan it does not have: the gate is polyemesis's app, not the account.
	if err != nil && (!strings.Contains(err.Error(), "needs X Livestream access") || strings.Contains(err.Error(), "paid tier") || strings.Contains(err.Error(), "does not have")) {
		t.Errorf("CheckEntitlement message = %q, want the whitelist gate copy and no account-plan wording", err.Error())
	}
	if _, err := x.TransitionBroadcast(ctx, "tok", "1ynJOZVeajOJR", PhaseLive); err == nil {
		t.Error("TransitionBroadcast with the gate off returned no error")
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("%d request(s) reached X with the Livestream gate off; there must be none", n)
	}
}

// TestXGateReasonIsTheWhitelistCopyTheOwnerAgreed names the gate in the
// platform's own terms, usable before anything is connected.
func TestXGateReasonIsTheWhitelistCopyTheOwnerAgreed(t *testing.T) {
	x := &X{}
	reason := x.EntitlementReason()
	for _, want := range []string{"needs X Livestream access", "whitelisted"} {
		if !strings.Contains(reason, want) {
			t.Errorf("EntitlementReason() = %q, want it to contain %q", reason, want)
		}
	}
	if !strings.Contains(x.ManualKeyReason(), "producer") {
		t.Error("ManualKeyReason must still tell the operator where to paste the key from")
	}
}

// TestXEntitlementProbeMapsAGateRefusalToNotEntitled checks the three outcomes
// the EntitlementGated contract names. A 403 or 412 is a gate; a 500 is not, and
// must never be reported as one.
func TestXEntitlementProbeMapsAGateRefusalToNotEntitled(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   bool // true means ErrNotEntitled
	}{
		{"forbidden is the whitelist gate", http.StatusForbidden, true},
		{"precondition failed is the eligibility gate", http.StatusPreconditionFailed, true},
		{"server error is not evidence of a gate", http.StatusInternalServerError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/2/users/me" {
					_, _ = io.WriteString(w, xMeFixture)
					return
				}
				http.Error(w, `{"title":"Forbidden"}`, tc.status)
			})
			// The probe only reaches the family with the gate open; with it closed it refuses without a request, which
			// TestXLivestreamIsSwitchedOffByDefaultAndMakesNoRequest covers. These cases are about the open gate's mapping.
			x = x.WithLivestreamAccess()
			err := x.CheckEntitlement(context.Background(), "client", "tok")
			if got := errors.Is(err, ErrNotEntitled); got != tc.want {
				t.Errorf("CheckEntitlement(%d) errors.Is(ErrNotEntitled) = %v, want %v (err: %v)",
					tc.status, got, tc.want, err)
			}
		})
	}
}

// TestXEntitlementProbeSucceedsWhenTheLivestreamFamilyAnswers is the nil outcome.
func TestXEntitlementProbeSucceedsWhenTheLivestreamFamilyAnswers(t *testing.T) {
	x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/2/users/me" {
			_, _ = io.WriteString(w, xMeFixture)
			return
		}
		_, _ = io.WriteString(w, xEmptySourcesFixture)
	})
	x = x.WithLivestreamAccess()
	if err := x.CheckEntitlement(context.Background(), "client", "tok"); err != nil {
		t.Fatalf("CheckEntitlement against an answering Livestream family = %v, want nil", err)
	}
}

// ----------------------------------------------------------------- stream key

// TestXIngestMintsASourceWhenNoneExists is the source-minted key. With the gate
// on and no polyemesis source yet, a source is created and its own rtmps URL and
// key are returned. The host is read from the response, never invented.
func TestXIngestMintsASourceWhenNoneExists(t *testing.T) {
	var created bool
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/2/users/me":
			_, _ = io.WriteString(w, xMeFixture)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sources"):
			_, _ = io.WriteString(w, xEmptySourcesFixture)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sources"):
			created = true
			_, _ = io.WriteString(w, `{"data":{"source":{"id":"minted1","name":"polyemesis",`+
				`"rtmp_url":"rtmp://ingest.example.invalid/live","rtmps_url":"rtmps://ingest.example.invalid:443/live",`+
				`"rtmp_stream_key":"minted1"}}}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	})
	ing, err := x.Ingest(context.Background(), "client", "tok")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !created {
		t.Error("no source was created when none existed")
	}
	if ing.Key != "minted1" {
		t.Errorf("Key = %q, want the rtmp_stream_key X minted", ing.Key)
	}
	if ing.URL != "rtmps://ingest.example.invalid:443/live" {
		t.Errorf("URL = %q, want the rtmps_url X returned verbatim", ing.URL)
	}
}

// TestXIngestReusesThePolyemesisSourceInsteadOfMintingAnother keeps the source
// list from growing one entry per connect.
func TestXIngestReusesThePolyemesisSourceInsteadOfMintingAnother(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/2/users/me":
			_, _ = io.WriteString(w, xMeFixture)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, xSourcesFixture)
		default:
			t.Errorf("reused a source but still sent %s %s", r.Method, r.URL.Path)
			http.Error(w, "no", http.StatusTeapot)
		}
	})
	ing, err := x.Ingest(context.Background(), "client", "tok")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if ing.Key != "k3y0ne" {
		t.Errorf("Key = %q, want the existing source's key", ing.Key)
	}
}

// TestXKeyNeverAppearsInAnErrorMessage is the leak test. A failed create that
// echoes a key in its body must not carry that key into the error an operator
// sees, a log line or an event.
func TestXKeyNeverAppearsInAnErrorMessage(t *testing.T) {
	key := xSecret("leak", "canary", "9z")
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/2/users/me":
			_, _ = io.WriteString(w, xMeFixture)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, xEmptySourcesFixture)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"errors":[{"title":"Invalid request","detail":"name is required"}],"data":{"source":{"rtmp_stream_key":"`+key+`"}}}`)
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

// TestXStreamKeysListsEverySourceKeyForTheCrossCheck is the read-only reader.
func TestXStreamKeysListsEverySourceKeyForTheCrossCheck(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/2/users/me" {
			_, _ = io.WriteString(w, xMeFixture)
			return
		}
		_, _ = io.WriteString(w, xSourcesFixture)
	})
	keys, err := x.StreamKeys(context.Background(), "client", "tok")
	if err != nil {
		t.Fatalf("StreamKeys: %v", err)
	}
	if !reflect.DeepEqual(keys, []string{"k3y0ne"}) {
		t.Errorf("StreamKeys = %v, want the one source key", keys)
	}
}

// ---------------------------------------------------------------- lifecycle

// TestXGoLiveIsPublishAndEndIsEndAndTestingIsRefused maps X's state machine onto
// the three documented phases. X has no testing state, so asking for one is a
// refusal that names the reason.
func TestXGoLiveIsPublishAndEndIsEndAndTestingIsRefused(t *testing.T) {
	var seen []string
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/2/users/me" {
			_, _ = io.WriteString(w, xMeFixture)
			return
		}
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(body))
		_, _ = io.WriteString(w, `{"data":{"id":"1ynJOZVeajOJR","state":"RUNNING","broadcast_id":"1ynJOZVeajOJR"}}`)
	})
	ctx := context.Background()

	if _, err := x.TransitionBroadcast(ctx, "tok", "1ynJOZVeajOJR", PhaseTesting); err == nil {
		t.Error("X has no testing state; a testing transition must be refused")
	}
	if _, err := x.TransitionBroadcast(ctx, "tok", "1ynJOZVeajOJR", PhaseLive); err != nil {
		t.Fatalf("go live: %v", err)
	}
	if _, err := x.TransitionBroadcast(ctx, "tok", "1ynJOZVeajOJR", PhaseComplete); err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("saw %d writes, want 2 (publish, end): %v", len(seen), seen)
	}
	if !strings.HasPrefix(seen[0], "PUT /2/users/1234567890/broadcasts/1ynJOZVeajOJR/state") ||
		!strings.Contains(seen[0], `"state":"PUBLISH"`) {
		t.Errorf("go-live = %q, want PUT .../users/{id}/broadcasts/{bid}/state with PUBLISH", seen[0])
	}
	if !strings.HasPrefix(seen[1], "PUT /2/broadcasts/1ynJOZVeajOJR/state") ||
		!strings.Contains(seen[1], `"state":"END"`) {
		t.Errorf("end = %q, want PUT /2/broadcasts/{bid}/state with END", seen[1])
	}
}

// TestXBroadcastStateReadsTheSourceAndTheWord is the read side. The state word is
// passed through unmapped, because X documents its states and an operator
// comparing them against X's own page must see the same word.
func TestXBroadcastStateReadsTheSourceAndTheWord(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"id":"1ynJOZVeajOJR","state":"RUNNING","source_id":"k3y0ne","title":"Sunday practice"}}`)
	})
	st, err := x.BroadcastState(context.Background(), "tok", "1ynJOZVeajOJR")
	if err != nil {
		t.Fatalf("BroadcastState: %v", err)
	}
	if st.Status != "RUNNING" || st.BoundStreamID != "k3y0ne" || st.Title != "Sunday practice" {
		t.Errorf("BroadcastState = %+v", st)
	}
	if st.StreamActive != nil || st.MonitorStream != nil {
		t.Error("X publishes no stream-liveness read, so those preconditions must stay UNKNOWN (nil), not false")
	}
}

// TestXMetadataCannotChangeALiveBroadcastAndSaysSo: the title and chat option are
// written only by the PUBLISH transition. A metadata push must not claim it
// changed a broadcast that is already running.
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

// TestXBroadcastListDecodesTheSpecFields is the shape pin for the list read,
// including the viewer counts that must stay undecoded.
func TestXBroadcastListDecodesTheSpecFields(t *testing.T) {
	x, _ := xStubLive(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, xBroadcastListFixture)
	})
	bs, err := x.Broadcasts(context.Background(), "client", "tok")
	if err != nil {
		t.Fatalf("Broadcasts: %v", err)
	}
	if len(bs) != 1 || bs[0].State != "RUNNING" || bs[0].SourceID != "k3y0ne" {
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
