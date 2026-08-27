package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

/* THE FIXTURES IN THIS FILE ARE COPIED FROM TROVO'S OWN RESPONSE SAMPLES, BYTE
 * FOR BYTE, AND THAT IS THE POINT OF THE FILE.
 *
 * kick.go records what happens when a fake agrees with the code instead of with
 * the platform: "It passed its tests because the tests served a fixture shaped
 * like the struct rather than like the endpoint." Trovo has four ways to write
 * that same bug and the first one is invisible in Go source — expires_in is a
 * QUOTED STRING in every token response Trovo publishes, so a hand-rolled
 * fixture that writes `"expires_in": 14400` makes an `int` field pass here and
 * fail on the first real exchange.
 *
 * So the token samples below are §3.2's and §4.3's, the channel sample is
 * §5.6's, and the category sample is §5.2's, transcribed from
 * https://developer.trovo.live/docs/APIs.html read 2026-08-26 and recorded in
 * docs/evidence/vimeo-trovo-oauth-2026-08-26.md. Anyone editing one of these
 * strings is changing what this suite believes Trovo sends: re-read the
 * reference rather than adjusting the fixture to suit the code.
 */

// THE SHAPE IS VERBATIM; THE TOKEN VALUES ARE NOT, and the difference matters.
//
// What these fixtures exist to pin is Trovo's SHAPE -- above all `expires_in`
// as a quoted STRING, which an int field cannot decode and which a hand-written
// fixture gets wrong by writing 14400. That is preserved exactly.
//
// The token values are replaced with obviously-fake strings. Trovo's published
// samples use realistic 32-hex tokens, and copying those in tripped
// generic-api-key four times. The allowlist is not the answer: .gitleaks.toml
// says so in its own words -- "six permanent known-failures is how a scanner
// stops being read at all" -- and adding four more to accommodate a test would
// dilute the one check that finds a real key committed to a test file.

// §3.2, "Response Sample". Note expires_in.
const trovoExchangeSample = `{
    "access_token": "trovo-not-a-real-access-token",
    "token_type": "bearer",
    "expires_in": "14400",
    "refresh_token": "trovo-not-a-real-refresh-token"
}`

// §4.3, "Response Sample", verbatim.
const trovoRefreshSample = `{
    "access_token": "trovo-not-a-real-rotated-access-token",
    "token_type": "bearer",
    "expires_in": "14400",
    "refresh_token": "trovo-not-a-real-rotated-refresh-token"
}`

// §5.6, "Response Sample", verbatim. The mixed types are Trovo's: uid,
// channel_id and created_at are quoted, current_viewers and followers are not.
const trovoChannelSample = `{
    "uid": "100000021",
    "channel_id": "100000021",
    "is_live": false,
    "category_id": "10672",
    "category_name": "Genshin Impact",
    "live_title": "Genshin impact with leafinsummer",
    "stream_key": "live/xxxxxxxxxxxxxxxxxxx",
    "audi_type": "CHANNEL_AUDIENCE_TYPE_FAMILYFRIENDLY",
    "language_code": "EN",
    "thumbnail": "",
    "current_viewers": 0,
    "followers": 27,
    "streamer_info": "Hello",
    "profile_pic": "https://headicon.trovo.live/user/cxq7kbiaaaaabd5cniezh3x5cu.jpeg?t=2",
    "channel_url": "https://trovo.live/leafinsummer",
    "created_at": "1584341848",
    "subscriber_num": 1,
    "username": "leafinsummer",
    "social_links": [
        {
            "type": "Instagram",
            "url": "https://www.instagram.com/iwang88/"
        }
    ]
}`

// §5.2, "Sample response", trimmed to two of its three entries.
const trovoCategorySample = `{
    "category_info": [
        {
            "id": "2000027",
            "name": "Apex Legends",
            "short_name": "Apex",
            "icon_url": "https://static.trovo.live/imgupload/category/x.jpg",
            "desc": "Apex"
        },
        {
            "id": "2000017",
            "name": "PLAYERUNKNOWN'S BATTLEGROUNDS",
            "short_name": "PUBG",
            "icon_url": "https://static.trovo.live/imgUpload/y.png",
            "desc": "PLAYERUNKNOWN'S BATTLEGROUNDS"
        }
    ],
    "has_more": true
}`

// §5.7's response to a successful update. It says nothing about what was
// applied, which is why the shape of the REQUEST is what these tests assert on.
const trovoUpdateSample = `{"empty": ""}`

// trovoRecorder is a stub Trovo plus the log of what reached it.
type trovoRecorder struct {
	mu   sync.Mutex
	seen []trovoSeenRequest
}

type trovoSeenRequest struct {
	Method   string
	Path     string
	RawQuery string
	Auth     string
	ClientID string
	Body     map[string]any
	RawBody  string
}

func (rec *trovoRecorder) record(r *http.Request) {
	var raw []byte
	if r.Body != nil {
		raw, _ = readAllLimited(r)
	}
	seen := trovoSeenRequest{
		Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
		Auth: r.Header.Get("Authorization"), ClientID: r.Header.Get("Client-ID"),
		RawBody: string(raw),
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &seen.Body)
	}
	rec.mu.Lock()
	rec.seen = append(rec.seen, seen)
	rec.mu.Unlock()
}

func (rec *trovoRecorder) calls() []trovoSeenRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]trovoSeenRequest(nil), rec.seen...)
}

func readAllLimited(r *http.Request) ([]byte, error) {
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
		if len(buf) > 1<<20 {
			return buf, nil
		}
	}
}

// trovoFixture stands up a Trovo that answers each documented path with its
// documented sample, and hands back the provider aimed at it.
func trovoFixture(t *testing.T) (*Trovo, *trovoRecorder) {
	t.Helper()
	rec := &trovoRecorder{}
	srv := checkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case trovoExchangePath:
			_, _ = w.Write([]byte(trovoExchangeSample))
		case trovoRefreshPath:
			_, _ = w.Write([]byte(trovoRefreshSample))
		case trovoChannelPath:
			_, _ = w.Write([]byte(trovoChannelSample))
		case trovoSearchPath:
			_, _ = w.Write([]byte(trovoCategorySample))
		case trovoUpdatePath:
			_, _ = w.Write([]byte(trovoUpdateSample))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":1002,"message":"Invalid parameters."}`))
		}
	})
	return NewTrovo(WithBaseURL(srv.URL)), rec
}

// ---------------------------------------------------- trap 1: expires_in

// TestTrovoDecodesTheQuotedExpiresInTrovoActuallySends is the first trap.
//
// Trovo sends "expires_in": "14400" -- a quoted string -- in BOTH documented
// token responses. An `int` field cannot unmarshal that, and encoding/json
// fails the whole object when one field fails, so the access token is lost too:
// a perfectly good response is reported as "decode token response" and sign-in
// is broken outright.
//
// Mutation run against this: in trovo.go, change the ExpiresIn field's type
// from `trovoSeconds` to `int`. Observed FAIL, both subtests --
// "Exchange rejected Trovo's own documented response sample: decode token
// response: json: cannot unmarshal string into Go struct field .expires_in of
// type int".
func TestTrovoDecodesTheQuotedExpiresInTrovoActuallySends(t *testing.T) {
	// Four hours, which is what "14400" seconds is and what the evidence file
	// records as the access-token lifetime.
	const wantLifetime = 4 * time.Hour

	t.Run("exchange", func(t *testing.T) {
		tv, _ := trovoFixture(t)
		before := time.Now()
		tok, err := tv.Exchange(context.Background(), "cid", "secret", "https://r.test/cb", "the-code", "")
		if err != nil {
			t.Fatalf("Exchange rejected Trovo's own documented response sample: %v", err)
		}
		if tok.AccessToken != "trovo-not-a-real-access-token" {
			t.Errorf("access token = %q", tok.AccessToken)
		}
		if tok.RefreshToken != "trovo-not-a-real-refresh-token" {
			t.Errorf("refresh token = %q", tok.RefreshToken)
		}
		assertLifetime(t, tok.ExpiresAt, before, wantLifetime)
	})

	t.Run("refresh", func(t *testing.T) {
		tv, _ := trovoFixture(t)
		before := time.Now()
		tok, err := tv.Refresh(context.Background(), "cid", "secret", "old-refresh")
		if err != nil {
			t.Fatalf("Refresh rejected Trovo's own documented response sample: %v", err)
		}
		if tok.RefreshToken != "trovo-not-a-real-rotated-refresh-token" {
			t.Errorf("refresh token = %q; Trovo rotates it and the new one must be stored", tok.RefreshToken)
		}
		assertLifetime(t, tok.ExpiresAt, before, wantLifetime)
	})
}

// assertLifetime checks an expiry lands roughly `want` after the call started.
// A minute of slack, because the assertion is "the string was parsed as
// seconds" and not "the clock is exact".
func assertLifetime(t *testing.T, expiresAt, before time.Time, want time.Duration) {
	t.Helper()
	if expiresAt.IsZero() {
		t.Fatalf("ExpiresAt is the zero time, so expires_in was dropped. A token with no "+
			"expiry is never refreshed on expiry, which is exactly what Trovo's "+
			"fifty-tokens-per-refresh-token ceiling punishes. want about %s from now", want)
	}
	got := expiresAt.Sub(before)
	if got < want-time.Minute || got > want+time.Minute {
		t.Errorf("token expires in %s, want about %s -- expires_in was misread", got, want)
	}
}

// TestTrovoRefreshKeepsTheStoredRefreshTokenWhenTrovoOmitsOne: Trovo rotates
// refresh tokens and documents that the old one keeps working for its full 30
// days. A response without one must not silently disconnect the account.
func TestTrovoRefreshKeepsTheStoredRefreshTokenWhenTrovoOmitsOne(t *testing.T) {
	srv := checkFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new","token_type":"bearer","expires_in":"14400"}`))
	})
	tv := NewTrovo(WithBaseURL(srv.URL))

	tok, err := tv.Refresh(context.Background(), "cid", "secret", "the-stored-one")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.RefreshToken != "the-stored-one" {
		t.Errorf("refresh token = %q, want the stored one carried forward", tok.RefreshToken)
	}
}

// ------------------------------------------- traps 2 and 3: the headers

// TestEveryTrovoCallSendsTheOAuthSchemeAndTheClientIDHeader covers the two
// traps that produce a 401 naming the wrong cause.
//
// Trovo authenticates with `Authorization: OAuth <token>`, NOT Bearer, and
// wants the client id in a header rather than a query parameter. Both failures
// arrive as a refusal that looks like a bad or expired token, so the operator
// is sent to reconnect an account that was never the problem.
//
// Every entry point is enumerated rather than one being sampled, for the reason
// endpoints_test.go gives about stubbed hosts: whichever single call a sampled
// guard happened to make would be the one that worked.
//
// Mutation run against this: in trovoRequest, change the header to
// "Bearer "+accessToken. Observed FAIL on every authenticated subtest --
// `Authorization = "Bearer access-token", want the OAuth scheme`.
func TestEveryTrovoCallSendsTheOAuthSchemeAndTheClientIDHeader(t *testing.T) {
	ctx := context.Background()
	const tok = "access-token"

	calls := map[string]func(tv *Trovo){
		"Account": func(tv *Trovo) { _, _ = tv.Account(ctx, "cid", tok) },
		"Ingest":  func(tv *Trovo) { _, _ = tv.Ingest(ctx, "cid", tok) },
		"Stats":   func(tv *Trovo) { _, _ = tv.Stats(ctx, "cid", tok) },
		"UpdateChannel": func(tv *Trovo) {
			_ = tv.UpdateChannel(ctx, "cid", tok, "100000021", TrovoChannelUpdate{LiveTitle: "x"})
		},
		"SearchCategories": func(tv *Trovo) { _, _ = tv.SearchCategories(ctx, "cid", tok, "apex") },
		"PushMetadata": func(tv *Trovo) {
			_, _ = tv.PushMetadata(ctx, "cid", tok, "100000021", Metadata{Title: "x", Category: "Apex Legends"})
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			tv, rec := trovoFixture(t)
			call(tv)

			seen := rec.calls()
			if len(seen) == 0 {
				t.Fatal("the call made no request at all, so this subtest asserts nothing")
			}
			for _, c := range seen {
				if !strings.HasPrefix(c.Auth, "OAuth ") {
					t.Errorf("%s %s: Authorization = %q, want the OAuth scheme.\n"+
						"Trovo answers a Bearer with a 401, which reads as an expired token "+
						"rather than a wrong header -- so the operator reconnects an account "+
						"that was fine and nothing changes.", c.Method, c.Path, c.Auth)
				}
				if c.ClientID != "cid" {
					t.Errorf("%s %s: Client-ID header = %q, want the client id. Trovo takes it "+
						"in a header on every call, never as a query parameter.",
						c.Method, c.Path, c.ClientID)
				}
				if strings.Contains(c.RawQuery, "client_id") {
					t.Errorf("%s %s: the client id was put in the query string (%q)",
						c.Method, c.Path, c.RawQuery)
				}
			}
		})
	}
}

// TestTrovoTokenCallsCarryTheClientIDHeaderAndAJSONBody pins the other half of
// trap 3, on the two calls that do NOT carry an access token.
//
// Trovo's exchange and refresh take application/json with the client id in a
// header -- not the form-encoded body with client_id in it that postForm
// produces for the other four providers. A form body here is refused with
// Trovo's own invalidHeader error, which says nothing about the encoding.
func TestTrovoTokenCallsCarryTheClientIDHeaderAndAJSONBody(t *testing.T) {
	ctx := context.Background()

	t.Run("exchange", func(t *testing.T) {
		tv, rec := trovoFixture(t)
		if _, err := tv.Exchange(ctx, "cid", "the-secret", "https://r.test/cb", "the-code", "unused-verifier"); err != nil {
			t.Fatalf("Exchange: %v", err)
		}
		c := onlyCall(t, rec)
		if c.Path != trovoExchangePath {
			t.Errorf("path = %q, want %q", c.Path, trovoExchangePath)
		}
		if c.ClientID != "cid" {
			t.Errorf("client-id header = %q", c.ClientID)
		}
		if c.Body == nil {
			t.Fatalf("the exchange body is not JSON: %q", c.RawBody)
		}
		for k, want := range map[string]string{
			"client_secret": "the-secret",
			"grant_type":    "authorization_code",
			"code":          "the-code",
			"redirect_uri":  "https://r.test/cb",
		} {
			if got, _ := c.Body[k].(string); got != want {
				t.Errorf("body[%q] = %q, want %q", k, got, want)
			}
		}
		// PKCE is not documented for Trovo, so the verifier a caller passed in
		// must not reach the wire even though one was supplied.
		if _, ok := c.Body["code_verifier"]; ok {
			t.Error("the exchange sent a code_verifier to a provider that reports PKCE() false")
		}
	})

	t.Run("refresh", func(t *testing.T) {
		tv, rec := trovoFixture(t)
		if _, err := tv.Refresh(ctx, "cid", "the-secret", "the-refresh"); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		c := onlyCall(t, rec)
		if c.Path != trovoRefreshPath {
			t.Errorf("path = %q, want %q", c.Path, trovoRefreshPath)
		}
		if c.ClientID != "cid" {
			t.Errorf("client-id header = %q", c.ClientID)
		}
		if got, _ := c.Body["grant_type"].(string); got != "refresh_token" {
			t.Errorf("grant_type = %q", got)
		}
		if got, _ := c.Body["refresh_token"].(string); got != "the-refresh" {
			t.Errorf("refresh_token = %q", got)
		}
	})
}

func onlyCall(t *testing.T, rec *trovoRecorder) trovoSeenRequest {
	t.Helper()
	seen := rec.calls()
	if len(seen) != 1 {
		t.Fatalf("want exactly one request, got %d: %+v", len(seen), seen)
	}
	return seen[0]
}

// ------------------------------------------------------------- the auth URL

// TestTrovoAuthURLSeparatesScopesWithLiteralPlusSigns.
//
// Trovo documents "'+' separated list of scopes" and its own example sends them
// unencoded. url.Values.Encode percent-encodes a '+' inside a value as %2B, so
// building the whole query through it produces a scope parameter that differs
// from every documented example -- and a consent screen that grants the wrong
// set, or refuses, is a failure at the one step the operator cannot work
// around.
//
// Mutation run against this: build the URL with q.Set("scope",
// strings.Join(t.Scopes(), "+")) and return t.loginEndpoint()+"/page/login.html?"+
// q.Encode(). Observed FAIL -- `AuthURL percent-encoded the scope separator`.
func TestTrovoAuthURLSeparatesScopesWithLiteralPlusSigns(t *testing.T) {
	tv := &Trovo{}
	raw := tv.AuthURL("the-client", "https://box.test/api/v1/oauth/trovo/callback", "state-1", "")

	want := "scope=" + strings.Join(tv.Scopes(), "+")
	if !strings.Contains(raw, want) {
		t.Errorf("AuthURL = %q\nwant it to contain %q", raw, want)
	}
	if strings.Contains(raw, "%2B") || strings.Contains(raw, "%2b") {
		t.Errorf("AuthURL percent-encoded the scope separator: %q\n"+
			"Trovo's own example sends literal '+' between scopes.", raw)
	}
	// The rest of the query still needs escaping, and the redirect is the one
	// that proves it: an unescaped one would end the parameter at its own '?'.
	if !strings.Contains(raw, "redirect_uri=https%3A%2F%2Fbox.test%2Fapi%2Fv1%2Foauth%2Ftrovo%2Fcallback") {
		t.Errorf("AuthURL did not escape the redirect URI: %q", raw)
	}
	if !strings.HasPrefix(raw, "https://open.trovo.live/page/login.html?") {
		t.Errorf("AuthURL = %q, want Trovo's consent page", raw)
	}
	if !strings.Contains(raw, "response_type=code") {
		t.Errorf("AuthURL asks for something other than a code: %q", raw)
	}
}

// ---------------------------------------------------------------- the ingest

// TestTrovoIngestReturnsTheKeyAndDeliberatelyNoURL.
//
// Trovo publishes the stream key on its channel resource and publishes the
// ingest hostname NOWHERE: it is regional and appears only in the creator
// dashboard. A URL invented here would publish to somebody else's region and
// look like a polyemesis bug -- the failure kick.go's Ingest spends a paragraph
// warning about -- so an empty URL is the honest answer and the caller keeps
// whatever the operator pasted.
func TestTrovoIngestReturnsTheKeyAndDeliberatelyNoURL(t *testing.T) {
	tv, _ := trovoFixture(t)

	ing, err := tv.Ingest(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if ing.Key != "live/xxxxxxxxxxxxxxxxxxx" {
		t.Errorf("key = %q, want the stream_key from Trovo's documented sample", ing.Key)
	}
	if ing.URL != "" {
		t.Errorf("Ingest returned an ingest URL %q. Trovo publishes none, so anything "+
			"here was invented and points at one particular region.", ing.URL)
	}
}

// TestTrovoIngestSaysWhatToDoWhenTheKeyIsWithheld. The key is only present when
// the token carries channel_details_self, and granting a scope never upgrades a
// token already issued -- so the fix is a reconnect and the error has to say so,
// or the symptom reads as Trovo being broken.
func TestTrovoIngestSaysWhatToDoWhenTheKeyIsWithheld(t *testing.T) {
	srv := checkFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"channel_id":"100000021","username":"leafinsummer"}`))
	})
	tv := NewTrovo(WithBaseURL(srv.URL))

	_, err := tv.Ingest(context.Background(), "cid", "tok")
	if err == nil {
		t.Fatal("a channel with no stream key was reported as a successful ingest")
	}
	for _, want := range []string{"channel_details_self", "connect it again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q, so it does not name the fix: %v", want, err)
		}
	}
}

func TestTrovoAccountNamesTheChannelAndKeepsItsID(t *testing.T) {
	tv, _ := trovoFixture(t)

	acct, err := tv.Account(context.Background(), "cid", "tok")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if acct.Name != "leafinsummer" {
		t.Errorf("name = %q, want the username", acct.Name)
	}
	// The ref is what PushMetadata addresses the update to, so a wrong one is a
	// title written to somebody else's channel or to nobody's.
	if acct.Ref != "100000021" {
		t.Errorf("ref = %q, want the channel id", acct.Ref)
	}
}

// -------------------------------------------------------------- the metadata

// TestTrovoUpdateSendsANumericChannelIDAndSpellsTheCategoryFieldCategoryID is
// the trap Trovo's own parameter table sets, and it is silent: §5.7 answers
// every accepted request with {"empty": ""}, so a write that changes nothing
// is indistinguishable from one that works.
//
// TWO SHAPES, both taken from §5.7's request SAMPLE rather than its parameter
// table:
//
//	channel_id is a JSON NUMBER. §5.6 returns the same id as the STRING
//	"100000021", so carrying the read value straight into the write sends a
//	string where an int is documented.
//
//	the category field is spelled category_id. The table calls the parameter
//	`category` and then gives its own example as (e.g."category_id":"10023");
//	the request sample sends "category_id", and §5.6 reads it back as
//	category_id. Sending `category` sets no category and reports success.
//
// Mutation run against this: in UpdateChannel, use body["category"] instead of
// body["category_id"]. Observed FAIL -- "the update body has no category_id".
// Second mutation: send body["channel_id"] = channelID (the string). Observed
// FAIL -- "channel_id arrived as a string".
func TestTrovoUpdateSendsANumericChannelIDAndSpellsTheCategoryFieldCategoryID(t *testing.T) {
	tv, rec := trovoFixture(t)

	res, err := tv.PushMetadata(context.Background(), "cid", "tok", "100000021",
		Metadata{Title: "Tonight's show", Category: "Apex Legends"})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}

	var update *trovoSeenRequest
	for i, c := range rec.calls() {
		if c.Path == trovoUpdatePath {
			update = &rec.calls()[i]
		}
	}
	if update == nil {
		t.Fatalf("no channel update reached Trovo; the calls were %+v", rec.calls())
	}

	// json.Unmarshal into `any` decodes every JSON number as float64 and every
	// JSON string as string, so this distinguishes the two spellings exactly.
	switch v := update.Body["channel_id"].(type) {
	case float64:
		if v != 100000021 {
			t.Errorf("channel_id = %v, want 100000021", v)
		}
	default:
		t.Errorf("channel_id arrived as a %T (%v); §5.7 types it int and its request "+
			"sample sends it unquoted, while §5.6 returns it as a string -- carrying "+
			"the read value straight through is the bug.", v, v)
	}

	if got, ok := update.Body["category_id"]; !ok {
		t.Errorf("the update body has no category_id: %v\n"+
			"§5.7's parameter table names the field `category` and its own example "+
			"and request sample both spell it category_id, which is also how §5.6 "+
			"reads it back. A write under the other name is accepted, sets nothing, "+
			"and answers {\"empty\": \"\"} either way.", update.Body)
	} else if got != "2000027" {
		t.Errorf("category_id = %v, want the id the search resolved (2000027)", got)
	}

	if update.Body["live_title"] != "Tonight's show" {
		t.Errorf("live_title = %v", update.Body["live_title"])
	}
	if len(res.Applied) != 2 {
		t.Errorf("applied = %v, want the title and the category", res.Applied)
	}
	if res.Category != "Apex Legends" {
		t.Errorf("reported category = %q, want Trovo's own spelling", res.Category)
	}
}

// TestTrovoReportsTheFieldsItCannotWriteRatherThanDroppingThem. Trovo's channel
// update takes four fields and neither a description nor a tag list is among
// them, so an operator who typed one has to be told it did not go.
func TestTrovoReportsTheFieldsItCannotWriteRatherThanDroppingThem(t *testing.T) {
	tv, _ := trovoFixture(t)

	res, err := tv.PushMetadata(context.Background(), "cid", "tok", "100000021", Metadata{
		Title: "Tonight's show", Description: "a long description", Tags: []string{"chess"},
	})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}
	skipped := map[MetadataField]bool{}
	for _, f := range res.Skipped {
		skipped[f] = true
	}
	if !skipped[FieldDescription] || !skipped[FieldTags] {
		t.Errorf("skipped = %v, want both the description and the tags reported", res.Skipped)
	}
	if len(res.Warnings) < 2 {
		t.Errorf("warnings = %v, want one per skipped field -- a skip with no reason "+
			"renders as a field that silently vanished", res.Warnings)
	}
}

// TestTrovoKeepsTheTitleWhenTheCategoryCannotBeResolved: the category is looked
// up BEFORE the write so a name Trovo has never heard of costs a warning rather
// than the title change the operator is about to go live with.
func TestTrovoKeepsTheTitleWhenTheCategoryCannotBeResolved(t *testing.T) {
	tv, rec := trovoFixture(t)

	res, err := tv.PushMetadata(context.Background(), "cid", "tok", "100000021",
		Metadata{Title: "Tonight's show", Category: "a game nobody has made"})
	if err != nil {
		t.Fatalf("PushMetadata: %v", err)
	}
	if len(res.Applied) != 1 || res.Applied[0] != FieldTitle {
		t.Errorf("applied = %v, want the title alone", res.Applied)
	}
	if len(res.Warnings) == 0 {
		t.Error("the category was dropped with no warning")
	}
	var wrote bool
	for _, c := range rec.calls() {
		if c.Path == trovoUpdatePath {
			wrote = true
			if _, ok := c.Body["category_id"]; ok {
				t.Errorf("an unresolved category still reached the write: %v", c.Body)
			}
		}
	}
	if !wrote {
		t.Error("the title was never written; an unresolvable category must not cost it")
	}
}

// ------------------------------------------------------------------- stats

// TestTrovoStatsReadsCurrentViewersAndReportsNoCountOffline.
//
// The count comes from the channel resource this provider already fetches, NOT
// from Trovo's dedicated viewers endpoint: that one's `total` is documented as
// the channel's "total login users", so it counts signed-in viewers only and
// would under-report an audience without saying so. LiveStats.ViewerCount's own
// comment is entirely about numbers that mean something other than they appear
// to.
//
// Offline the count is nil rather than zero, matching Twitch: "nobody is
// watching" and "the channel is not on" are different facts.
func TestTrovoStatsReadsCurrentViewersAndReportsNoCountOffline(t *testing.T) {
	t.Run("offline reports no count at all", func(t *testing.T) {
		// The documented sample is an offline channel with current_viewers 0.
		tv, _ := trovoFixture(t)
		got, err := tv.Stats(context.Background(), "cid", "tok")
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if got.Live {
			t.Error("is_live is false in the sample and Stats reported live")
		}
		if got.ViewerCount != nil {
			t.Errorf("viewer count = %d on an offline channel, want no count. A zero "+
				"reported as a number is indistinguishable from a real audience of "+
				"none, and the UI renders it as one.", *got.ViewerCount)
		}
		if got.Title != "Genshin impact with leafinsummer" || got.Category != "Genshin Impact" {
			t.Errorf("title/category = %q/%q", got.Title, got.Category)
		}
	})

	t.Run("live reports the number Trovo gave", func(t *testing.T) {
		srv := checkFixture(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"channel_id":"100000021","username":"leafinsummer",
				"is_live":true,"current_viewers":42,"live_title":"on air","language_code":"EN"}`))
		})
		tv := NewTrovo(WithBaseURL(srv.URL))

		got, err := tv.Stats(context.Background(), "cid", "tok")
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if !got.Live {
			t.Error("is_live is true and Stats reported offline")
		}
		if got.ViewerCount == nil || *got.ViewerCount != 42 {
			t.Errorf("viewer count = %v, want 42", got.ViewerCount)
		}
		if got.Source != trovoChannelPath {
			t.Errorf("source = %q, want the endpoint the numbers came from", got.Source)
		}
	})
}

// ---------------------------------------------------------------- the wiring

// TestTrovoIsRegisteredWithTheCapabilitiesItClaims is the join the drift tests
// make platform by platform, asserted once for this platform in one place so a
// failure names Trovo rather than a generic row.
func TestTrovoIsRegisteredWithTheCapabilitiesItClaims(t *testing.T) {
	if _, err := Get(db.PlatformTrovo); err != nil {
		t.Fatalf("Trovo is not a registered provider: %v", err)
	}
	if _, ok := MetadataFor(db.PlatformTrovo); !ok {
		t.Error("Trovo does not implement MetadataPusher, so the composer cannot push to it")
	}
	if _, ok := StatsFor(db.PlatformTrovo); !ok {
		t.Error("Trovo does not implement LiveStatter, so /stats answers supported:false")
	}
	// The three capabilities deliberately NOT built, asserted as absent so that
	// building one without moving its matrix cell fails here rather than
	// leaving the cell reading Unverified over working code.
	if _, ok := LifecycleFor(db.PlatformTrovo); ok {
		t.Error("Trovo implements BroadcastLifecycler, but Trovo publishes no start or " +
			"end endpoint at all -- whatever that method calls was invented")
	}
	if _, ok := ManualKeyFor(db.PlatformTrovo); ok {
		t.Error("Trovo declares ManualKey, which tells the UI the KEY is pasted. It is " +
			"fetched; it is the ingest URL that is typed, and that is a different field")
	}
}

// TestTrovoNeverPutsASecretInAnError. The client secret and the access token
// both pass through this provider, and a rejection message is rendered in the
// UI and written to logs.
func TestTrovoNeverPutsASecretInAnError(t *testing.T) {
	const (
		secret = "s3cr3t-do-not-print-me"
		token  = "t0ken-do-not-print-me"
	)
	srv := checkFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":10703,"error":"Unauthorized","message":"Authorization failed."}`))
	})
	tv := NewTrovo(WithBaseURL(srv.URL))
	ctx := context.Background()

	errs := []error{}
	if _, err := tv.Exchange(ctx, "cid", secret, "https://r.test/cb", "code", ""); err != nil {
		errs = append(errs, err)
	}
	if _, err := tv.Refresh(ctx, "cid", secret, token); err != nil {
		errs = append(errs, err)
	}
	if _, err := tv.Ingest(ctx, "cid", token); err != nil {
		errs = append(errs, err)
	}
	if _, err := tv.PushMetadata(ctx, "cid", token, "100000021", Metadata{Title: "x"}); err != nil {
		errs = append(errs, err)
	}
	if len(errs) != 4 {
		t.Fatalf("want every call to fail against a 401, got %d errors", len(errs))
	}
	for _, err := range errs {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("an error text carries the client secret: %v", err)
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("an error text carries the access token: %v", err)
		}
	}
}
