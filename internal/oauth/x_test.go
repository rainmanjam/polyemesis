package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// xStub returns a provider pointed at a test server, plus the server.
//
// The same shape kickStub uses, and for the same reason: X's consent, token and
// data calls all resolve through the per-instance base, so a stray real request
// would make this suite depend on the network -- and on an X developer app
// nobody running these tests has.
func xStub(t *testing.T, h http.HandlerFunc) (*X, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewX(WithBaseURL(srv.URL)), srv
}

// A real-shaped ListBroadcastsResponse. The field names are transcribed from
// X's served spec (components.schemas.Broadcast, read 2026-08-16) rather than
// invented, which is the whole value of the fixture: a rename in the decode
// struct fails here instead of returning an empty account at connect time.
//
// It carries total_watching and total_watched deliberately -- see
// TestXNeverDecodesTheUndescribedViewerCounts.
const xBroadcastListFixture = `{
  "data": [
    {
      "id": "1ynJOZVeajOJR",
      "broadcast_id": "1ynJOZVeajOJR",
      "twitter_user_id": "1234567890",
      "source_id": "src_abc123",
      "title": "Sunday practice",
      "share_url": "https://x.com/i/broadcasts/1ynJOZVeajOJR",
      "media_key": "13_1729384756",
      "state": "Running",
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

// ------------------------------------------------------------------ oauth

func TestXAuthURLCarriesBothBroadcastScopesAndNoUndocumentedParameter(t *testing.T) {
	x := &X{}
	if x.PKCE() {
		t.Fatal("PKCE() is true. X's served spec declares an authorizationCode flow with " +
			"no RFC 7636 parameters of any kind; see the comment on PKCE for the argument " +
			"and for what evidence would justify turning it on")
	}

	// A challenge is passed anyway. The framework hands one to every provider,
	// and a provider that reports PKCE false must DISCARD it rather than send
	// it: an authorization server that validates its query string strictly
	// rejects an unknown parameter, which locks everyone out of sign-in.
	raw := x.AuthURL("client-id", "https://polyemesis.test/api/v1/oauth/x/callback", "state-123", "a-challenge")

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthURL produced an unparseable URL %q: %v", raw, err)
	}
	if got, want := u.Scheme+"://"+u.Host+u.Path, "https://api.x.com/2/oauth2/authorize"; got != want {
		t.Errorf("AuthURL points at %q, want %q -- the authorizationUrl X's spec declares", got, want)
	}
	q := u.Query()
	for _, forbidden := range []string{"code_challenge", "code_challenge_method"} {
		if q.Has(forbidden) {
			t.Errorf("AuthURL sends %q while PKCE() is false. Nothing in X's spec documents it, "+
				"and an unknown parameter can be refused outright", forbidden)
		}
	}
	if got := q.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want code", got)
	}
	if got := q.Get("state"); got != "state-123" {
		t.Errorf("state = %q, want it passed through unchanged", got)
	}
	if got := q.Get("client_id"); got != "client-id" {
		t.Errorf("client_id = %q", got)
	}

	// The scopes are the point: a consent screen missing broadcast.write yields
	// a token that can read broadcasts and do nothing else, and no amount of
	// reconnecting fixes it without a scope change.
	granted := strings.Fields(q.Get("scope"))
	sort.Strings(granted)
	want := []string{"broadcast.read", "broadcast.write"}
	if !reflect.DeepEqual(granted, want) {
		t.Errorf("scope = %v, want exactly %v -- the only two scopes X's spec attaches to "+
			"the Broadcasts family", granted, want)
	}
}

func TestXScopesAndVersionAreThePairTheMechanismNeeds(t *testing.T) {
	x := &X{}
	if got := x.ScopeVersion(); got < 1 {
		t.Errorf("ScopeVersion() = %d; a zero version makes every account it ever connects "+
			"look like a legacy row and silently disables the reconnect prompt", got)
	}
	if len(x.Scopes()) == 0 {
		t.Fatal("Scopes() is empty")
	}
	// offline.access would change what a connected token can do, so it must
	// arrive with a version bump rather than quietly. This is not a ban on
	// adding it -- Refresh's comment argues for it -- it is the pairing that
	// makes forgetting the bump visible in review.
	for _, s := range x.Scopes() {
		if s == "offline.access" && x.ScopeVersion() == 1 {
			t.Error("offline.access was added to Scopes() without bumping ScopeVersion past 1. " +
				"Every existing account holds a token that still has no refresh token, and " +
				"the account list is what has to say so")
		}
	}
	if got := x.Platform(); string(got) != "x" {
		t.Errorf("Platform() = %q, want \"x\" -- the id the destination preset and the "+
			"capability row already use", got)
	}
}

// TestXReachesNoRealHost is this file's local copy of the escape guard, because
// endpoints_test.go enumerates the four REGISTERED providers and X is not one of
// them yet. It uses that file's own stub-and-guard helper, so when X is
// registered the two agree by construction.
func TestXReachesNoRealHost(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(x *X){
		"Exchange":      func(x *X) { _, _ = x.Exchange(ctx, "cid", "secret", "https://r.test/cb", "code", "") },
		"Refresh":       func(x *X) { _, _ = x.Refresh(ctx, "cid", "secret", "refresh") },
		"Account":       func(x *X) { _, _ = x.Account(ctx, "cid", "tok") },
		"Broadcasts":    func(x *X) { _, _ = x.Broadcasts(ctx, "cid", "tok") },
		"BroadcastByID": func(x *X) { _, _ = x.BroadcastByID(ctx, "cid", "tok", "1ynJOZVeajOJR") },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			base, guard := stubbedWorld(t)
			call(NewX(WithBaseURL(base)))
			if got := guard.escapes(); len(got) > 0 {
				t.Fatalf("X.%s went to a real host despite WithBaseURL:\n  %s", name, strings.Join(got, "\n  "))
			}
		})
	}
}

// --------------------------------------------------------------- the missing key

func TestXIngestRefusesTheManualKeyWayWithoutInventingAnEndpoint(t *testing.T) {
	// Pointed at a stub that would answer anything, to prove the refusal is a
	// decision rather than a failed request: X publishes nothing to call here,
	// so Ingest must not spend a round trip finding that out.
	var called bool
	x, _ := xStub(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		io.WriteString(w, `{"data":{"source_id":"src_abc123"}}`)
	})

	ing, err := x.Ingest(context.Background(), "cid", "tok")
	if err == nil {
		t.Fatalf("Ingest returned %#v; X publishes no endpoint that mints or lists a stream key", ing)
	}
	if ing != nil {
		t.Fatalf("Ingest returned %#v alongside an error", ing)
	}
	if called {
		t.Error("Ingest made an HTTP request. There is no documented endpoint for it to call, " +
			"so a request here means one was invented")
	}
	if !errors.Is(err, ErrNoStreamKeyAPI) {
		t.Fatalf("the error does not wrap ErrNoStreamKeyAPI, so callers cannot tell "+
			"'paste it yourself' from 'retry': %v", err)
	}

	// The fabrication guard, copied from Kick's: the message may name X's
	// producer tooling in prose, but must contain nothing an operator could
	// mistake for a real ingest endpoint.
	msg := strings.ToLower(err.Error())
	for _, forbidden := range []string{"://", "rtmp", "/2/sources"} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("the refusal contains %q, which reads as a fabricated ingest endpoint: %s", forbidden, err)
		}
	}
}

func TestXAdvertisesTheManualKeyCapabilitySoTheUICanSayItUpFront(t *testing.T) {
	var p any = &X{}
	mk, ok := p.(ManualKey)
	if !ok {
		t.Fatal("X does not implement ManualKey, so the UI can only learn the key cannot be " +
			"fetched by watching Ingest fail at the moment somebody clicks Fetch key")
	}
	if strings.TrimSpace(mk.ManualKeyReason()) == "" {
		t.Fatal("ManualKeyReason is empty; the stream-key field would carry no explanation")
	}
	// The good news has to be in there too. A reason that only says no reads as
	// "this platform does not work", and X's sign-in and broadcast reads do.
	if !strings.Contains(strings.ToLower(mk.ManualKeyReason()), "works without them") {
		t.Errorf("the reason does not tell the operator what still works: %q", mk.ManualKeyReason())
	}
}

// ------------------------------------------------------------- broadcast reads

func TestXAccountParsesARealShapedBroadcastList(t *testing.T) {
	var gotPath, gotFields, gotAuth string
	x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFields = r.URL.Query().Get("broadcast.fields")
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, xBroadcastListFixture)
	})

	acct, err := x.Account(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatalf("Account failed against a real-shaped list: %v", err)
	}
	if gotPath != "/2/broadcasts" {
		t.Errorf("Account called %q, want /2/broadcasts -- the only endpoint a broadcast.read "+
			"token may call that returns an account id at all", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want the bearer token", gotAuth)
	}
	if !strings.Contains(gotFields, "twitter_user_id") {
		t.Errorf("broadcast.fields = %q and does not ask for twitter_user_id, so an empty id "+
			"would be polyemesis failing to ask rather than X declining to answer", gotFields)
	}
	if acct.Ref != "1234567890" {
		t.Errorf("Ref = %q, want the twitter_user_id from the broadcast object", acct.Ref)
	}
	if acct.Name != "1234567890" {
		t.Errorf("Name = %q. X hands a broadcast.read token no handle and no display name, so "+
			"the id is the only honest label; a synthesised @handle would be one X never said", acct.Name)
	}
}

// The case an operator will actually hit first: a fresh account with no
// broadcast on it. It must say why rather than store a connection with no ref.
func TestXAccountSaysWhyWhenTheTokenOwnsNoBroadcast(t *testing.T) {
	x, _ := xStub(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"data":[],"meta":{"result_count":0}}`)
	})
	acct, err := x.Account(context.Background(), "cid", "tok")
	if err == nil {
		t.Fatalf("Account returned %#v for an account with no broadcasts; a stored account with "+
			"no ref fails later, at a worse moment", acct)
	}
	if !strings.Contains(err.Error(), "no broadcasts") {
		t.Errorf("the error does not name the cause: %v", err)
	}
	if !strings.Contains(err.Error(), "connect again") {
		t.Errorf("the error does not tell the operator what to do about it: %v", err)
	}
}

// A 200 carrying an errors array and no data is a refusal wearing a success
// code, and X's problem objects are the only words available for it.
func TestXSurfacesAProblemArrivingInsideATwoHundred(t *testing.T) {
	x, _ := xStub(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"data":[],"errors":[{
			"type":"https://api.x.com/2/problems/not-authorized-for-resource",
			"title":"Authorization Error",
			"detail":"Sorry, you are not authorized to see this broadcast.",
			"resource_type":"broadcast"}]}`)
	})
	_, err := x.Broadcasts(context.Background(), "cid", "tok")
	if err == nil {
		t.Fatal("a 200 whose body is nothing but problems was read as an empty list, so the " +
			"caller sees 'no broadcasts' where X said 'not authorized'")
	}
	if !strings.Contains(err.Error(), "not authorized to see this broadcast") {
		t.Errorf("X's own detail string did not survive into the error: %v", err)
	}
}

// TestXNamesNoCauseItCannotSource is the taxonomy rule as a test. X documents no
// error codes for any Broadcasts operation, so the failure must carry X's words
// and add no diagnosis of its own.
func TestXNamesNoCauseItCannotSource(t *testing.T) {
	x, _ := xStub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"title":"Client Forbidden","detail":"Access to this resource is not allowed."}`)
	})
	_, err := x.Broadcasts(context.Background(), "cid", "tok")
	if err == nil {
		t.Fatal("a 403 was not reported as a failure")
	}
	if !strings.Contains(err.Error(), "Access to this resource is not allowed.") {
		t.Errorf("X's own body did not reach the operator: %v", err)
	}
	if !strings.Contains(err.Error(), "no error taxonomy") {
		t.Errorf("the error does not say that X published no cause for this, which is the "+
			"difference between an honest refusal and an invented one: %v", err)
	}
	// The unresolved tier question belongs in the same breath: no X pricing page
	// names the Broadcasts family, so a 403 may be about the app's access rather
	// than about the request, and an operator rewriting a correct request is the
	// cost of not saying so.
	if !strings.Contains(err.Error(), "access to them") {
		t.Errorf("the error does not mention that the app may simply lack access to the "+
			"broadcast endpoints: %v", err)
	}
}

// TestXNeverDecodesTheUndescribedViewerCounts is the rule from the evidence
// written as a guard: total_watching and total_watched are undescribed
// string-typed fields whose concurrent-versus-cumulative reading comes from
// their names alone. Decoding them is how they end up on a screen labelled
// "viewers", and stats.go's ViewerCount is a POINTER precisely because a count
// nobody can source must be absent rather than zero.
func TestXNeverDecodesTheUndescribedViewerCounts(t *testing.T) {
	rt := reflect.TypeOf(XBroadcast{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if strings.Contains(tag, "watch") {
			t.Errorf("XBroadcast decodes %q. X describes neither field, the word \"viewer\" "+
				"appears nowhere in its spec, and viewer stats are deliberately out of scope "+
				"for this commit -- a decoded count is one render away from being presented "+
				"as authoritative", tag)
		}
	}

	// And the behavioural half: the fixture carries both fields, so a struct
	// that grew one would show up here as a populated value rather than as a
	// silently ignored key.
	x, _ := xStub(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, xBroadcastListFixture)
	})
	bs, err := x.Broadcasts(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatalf("Broadcasts: %v", err)
	}
	if len(bs) != 1 {
		t.Fatalf("got %d broadcasts, want 1", len(bs))
	}
	if strings.Contains(strings.ToLower(reflect.TypeOf(bs[0]).String()), "watch") {
		t.Error("a viewer-count field reached XBroadcast")
	}
}

// TestXAsksForEveryFieldItDecodes pins the broadcast.fields parameter against
// the struct, in both directions.
//
// The parameter is optional and X does not document what a request without it
// returns, so asking for exactly what is decoded is what makes an empty field
// mean "X declined" rather than "polyemesis forgot to ask". A field added to the
// struct and not to the list would decode nothing forever, which is the silent
// half; a field asked for and not decoded is only wasteful, but it is the same
// mistake pointing the other way and both are cheap to catch here.
func TestXAsksForEveryFieldItDecodes(t *testing.T) {
	asked := map[string]bool{}
	for _, f := range strings.Split(xBroadcastFields, ",") {
		asked[strings.TrimSpace(f)] = true
	}

	rt := reflect.TypeOf(XBroadcast{})
	decoded := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		decoded[name] = true
		if !asked[name] {
			t.Errorf("XBroadcast decodes %q, which broadcast.fields does not ask for. X's spec "+
				"does not state what an unrequested field does, so this one may never arrive", name)
		}
	}
	for name := range asked {
		if !decoded[name] {
			t.Errorf("broadcast.fields asks for %q, which nothing decodes", name)
		}
	}
}

func TestXBroadcastByIDReadsTheOneBroadcast(t *testing.T) {
	var gotPath string
	x, _ := xStub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{"data":{"id":"1ynJOZVeajOJR","broadcast_id":"1ynJOZVeajOJR",
			"state":"Running","source_id":"src_abc123","start_ms":"1755302400000"}}`)
	})
	b, err := x.BroadcastByID(context.Background(), "cid", "tok", "1ynJOZVeajOJR")
	if err != nil {
		t.Fatalf("BroadcastByID: %v", err)
	}
	if gotPath != "/2/broadcasts/1ynJOZVeajOJR" {
		t.Errorf("called %q, want /2/broadcasts/{id}", gotPath)
	}
	if b.SourceID != "src_abc123" {
		t.Errorf("SourceID = %q, want the value X echoed back", b.SourceID)
	}
	// start_ms stays a string. The suffix implies milliseconds; the spec does
	// not say so for this field, and a parsed time would present that inference
	// as a fact.
	if b.StartMS != "1755302400000" {
		t.Errorf("StartMS = %q, want the string X sent, unconverted", b.StartMS)
	}
	if b.State != "Running" {
		t.Errorf("State = %q", b.State)
	}
}
