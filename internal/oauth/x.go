package oauth

// X (Twitter) live video: the first code polyemesis has ever written against it,
// and it exists because the belief recorded everywhere else in this repository —
// that X has no live-video API — is wrong.
//
// capabilities.go still says "X's developer platform covers posts, users, media
// and the post firehose, not live-video ingest", and db/platforms.go's "x" preset
// says "none is planned". Both were written from X's navigation index, which has
// no Broadcasts section. The served spec does: GET https://api.x.com/2/openapi.json
// (read 2026-08-16, "X API v2", version 2.167) publishes 149 paths, of which nine
// are broadcast paths carrying thirteen operations under the tag Broadcasts,
// glossed "Endpoints related to live broadcasts and their chat", and two scopes
// nothing else in the spec uses:
//
//	broadcast.read   "View your live broadcasts and their chat."
//	broadcast.write  "Manage your live broadcasts and send chat messages on your behalf."
//
// Those capability cells are NOT corrected here. They live in the four files the
// orchestrator owns (capabilities.go, ui/src/lib/capabilities.ts, docs/PLATFORMS.md
// and the drift tests that pin them together), and a partial mirror edit fails
// four tests. See docs/evidence/facebook-chat-rumble-x-2026-08-16.md for the cells
// this file implies, and the report that accompanied this commit.
//
// SCOPE OF THIS FILE, deliberately narrow: the Provider interface and the two
// broadcast READS. Chat send, chat read, moderation and go-live are documented,
// are follow-on commits, and are not started here — bundling them would make the
// first code against an undocumented-until-now family unreviewable.
//
// This provider is NOT registered in ProvidersWith. Registration needs a
// db.Platform constant, a capability row keyed to it, a setup guide, a
// credential-check verdict and a regenerated testdata/provider-scopes.json —
// several of which are in files this task may not touch. The provider satisfies
// the interface (see the compile-time assertions below), so registration is the
// one-line change it looks like once those land.

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// X implements X's OAuth 2.0 authorization-code flow and the broadcast reads.
type X struct {
	// endpoints carries the base URLs; zero value is production. Both the
	// authorization server and the data API are the same host here, which is
	// unusual in this package -- Twitch, Kick and YouTube each split consent
	// from data across two hostnames -- so a reader looking for the second
	// constant should stop looking. See endpoints.go.
	endpoints
}

// xAPIBase is the one production host: consent, tokens and data all live on
// api.x.com. Both accessors below resolve to it, which is what keeps a provider
// built with WithBaseURL from reaching the real X for any call.
const xAPIBase = "https://api.x.com"

func (x *X) authEndpoint() string { return x.authBase(xAPIBase) }
func (x *X) apiEndpoint() string  { return x.apiBase(xAPIBase) }

// NewX builds a single provider. It sits here rather than beside NewYouTube and
// friends in endpoints.go because that file documents "the set", and X is
// deliberately not in it yet -- see the header.
func NewX(opts ...ProviderOption) *X {
	return &X{endpoints: newEndpoints(opts)}
}

// The interface assertions are the point of the whole file: everything else is
// downstream of X being a Provider that a later commit can register.
var (
	_ Provider  = (*X)(nil)
	_ ManualKey = (*X)(nil)
)

// xPlatform is spelled here rather than as db.PlatformX, and that is a decision
// rather than an omission.
//
// The string is "x" because that is already this platform's identity everywhere
// else: the destination preset id in db/platforms.go and the capability row's
// PresetID in capabilities.go. Adding db.PlatformX to internal/db is a
// cross-package change that also implies the capability row gains a Platform
// field -- and that row is in a file this task may not edit. Doing half of it
// would leave a constant nothing uses next to a matrix that still says X cannot
// sign in. When the pair lands together the value does not change.
const xPlatform db.Platform = "x"

func (x *X) Platform() db.Platform { return xPlatform }

// Scopes is exactly the two the Broadcasts family declares, and nothing else.
//
// Swept across all 178 operations in the served spec, broadcast.read and
// broadcast.write appear on exactly the thirteen Broadcasts operations and
// nowhere else, and every one of those thirteen accepts them (or OAuth 1.0a,
// which polyemesis does not implement). Both are requested at once rather than
// read-first-write-later for the reason kick.go's Scopes comment gives at
// length: granting a scope never upgrades a token already issued, so a scope
// added later costs every operator a disconnect-and-reconnect, and finding that
// out mid-broadcast is the worst possible moment.
//
// TWO SCOPES ARE MISSING FROM THIS LIST ON PURPOSE, and an implementer will hit
// both:
//
//   - offline.access, verbatim "Request a refresh token for the app." Without
//     it X issues no refresh token at all, and Refresh below has nothing to
//     spend. See the comment on Refresh: this is the single most consequential
//     open question about this list, and it is a scope decision -- which is to
//     say a capability-matrix decision -- rather than a code one.
//   - users.read, which with tweet.read is what GET /2/users/me declares. It is
//     the only endpoint in the spec that would hand back a handle or a display
//     name. Account below therefore identifies the connection by the numeric id
//     X puts on a broadcast object, and cannot name it.
//
// Neither is added here because the capability text the orchestrator owns names
// these two scopes and only these two. Adding either is a ScopeVersion bump.
func (x *X) Scopes() []string {
	return []string{
		"broadcast.read",  // list and read the account's broadcasts, and their chat
		"broadcast.write", // create, update and publish a scheduled broadcast
	}
}

// ScopeVersion 1: the first list. Bump it BY HAND whenever Scopes changes --
// offline.access and users.read are the two changes already known to be
// candidates, and either would leave existing accounts holding a token that
// silently cannot do the new thing.
func (x *X) ScopeVersion() int { return 1 }

// PKCE is OFF, and the challenge/verifier arguments below are deliberately
// discarded. This is the same call twitch.go makes, for the same reason, and it
// is the one decision in this file most likely to be revisited — so here is the
// argument in full.
//
// What is established: the spec's securitySchemes.OAuth2UserToken declares an
// authorizationCode flow with authorizationUrl https://api.x.com/2/oauth2/authorize
// and tokenUrl https://api.x.com/2/oauth2/token, and 23 scopes. It says nothing
// about RFC 7636 — no code_challenge, no code_challenge_method, no PKCE
// extension of any kind. X's human-facing OAuth 2.0 pages describe PKCE as its
// practice, but this repository's rule is that the spec is what was verified and
// a practice described elsewhere is not a fact this spec supplies.
//
// The rejected alternative, and why: sending code_challenge on a hunch. An
// authorization server that validates its query string strictly rejects an
// unknown parameter outright, which locks every operator out of sign-in at the
// consent screen — a far worse outcome than doing without defence in depth on a
// confidential client. The secret never leaves the server, the code is bound to
// a whitelisted redirect URI, and the state is single-use.
//
// The counter-argument, recorded because it is real: if X's authorize endpoint
// REQUIRES code_challenge, this is broken in the other direction and sign-in
// fails at the same screen. That is a symmetric risk here in a way it was not
// for Twitch, and it is decidable in one live request. The failure looks like an
// authorize error naming code_challenge; that is the evidence to flip this to
// true, and it costs nothing else — PKCE is not a scope, so no ScopeVersion bump
// and no reconnect.
func (x *X) PKCE() bool { return false }

func (x *X) AuthURL(clientID, redirectURI, state, _ string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(x.Scopes(), " "))
	q.Set("state", state)
	return x.authEndpoint() + "/2/oauth2/authorize?" + q.Encode()
}

// Exchange sends the client pair in the request body, and that is a guess this
// comment is here to flag rather than hide.
//
// The spec supplies the tokenUrl and nothing else: no client-authentication
// method is declared anywhere in securitySchemes. RFC 6749 requires an
// authorization server to support HTTP Basic and merely PERMITS the body form,
// so an X that accepts only Basic would answer invalid_client here. The body
// form is what every other provider in this package sends, and one shape across
// five providers is worth more than a second guess — but if a live exchange
// comes back invalid_client with a valid pair, move the pair into an
// Authorization: Basic header rather than looking for the bug in the form.
func (x *X) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, _ string) (*Token, error) {
	return postForm(ctx, x.tokenEndpoint(), url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}, nil)
}

func (x *X) tokenEndpoint() string { return x.authEndpoint() + "/2/oauth2/token" }

// Refresh will have nothing to refresh until offline.access is requested.
//
// X's scope list defines offline.access as "Request a refresh token for the
// app." — so a token minted under broadcast.read + broadcast.write alone comes
// back with no refresh_token, and this method's argument is empty. It is
// implemented anyway, correctly, because the fix is one scope and a version
// bump: the day offline.access is granted, refreshing must already work rather
// than being discovered missing by an access token expiring mid-broadcast.
//
// The empty-refresh-token case is NOT pre-refused locally. X's own answer names
// what it objects to, and a local guess about it would be one more thing to get
// wrong the day the scope lands.
func (x *X) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	tok, err := postForm(ctx, x.tokenEndpoint(), url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}, nil)
	if err != nil {
		return nil, err
	}
	// A response that omits a rotated token must not silently disconnect the
	// account an hour later; same guard as Twitch and Kick.
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

// ------------------------------------------------------------ broadcast reads

// XBroadcast is the subset of X's Broadcast schema polyemesis decodes.
//
// NOT ONE OF THAT SCHEMA'S 26 PROPERTIES CARRIES A DESCRIPTION. They are bare
// types in the spec, so every meaning below is read off the property NAME, and
// this struct deliberately decodes only the ones whose name is an identifier or
// an opaque string rather than a claim. The rest stay on the wire: restating
// twenty-six names in Go would present a guess about each of them as though the
// meaning were known, and a field nothing reads is a field nobody checks.
//
// The two absent on purpose, and loudly: total_watching and total_watched. Both
// exist, both are string-typed, both are undescribed, and the word "viewer"
// appears zero times in the entire spec — concurrent-versus-cumulative is a
// reading of two names. Viewer stats are out of scope for this commit precisely
// so that nothing here can be mistaken for an authoritative viewer count, and
// stats.go's rule (a count a platform declines to give is nil, never 0) is why a
// wrong reading would be worse than no reading.
type XBroadcast struct {
	// ID and BroadcastID are separate properties in the schema and both are
	// decoded rather than one being assumed to mirror the other. The path
	// parameter for get/update/delete/live is documented as the "Alphanumeric
	// UBS broadcast id", pattern ^[a-zA-Z0-9]{1,13}$ — which is a constraint on
	// the PATH, not a statement about which of these two fields fills it.
	ID          string `json:"id"`
	BroadcastID string `json:"broadcast_id"`
	// TwitterUserID is the only identity X hands a broadcast.read token; see
	// Account.
	TwitterUserID string `json:"twitter_user_id"`
	// SourceID is X's own name for the bound ingest. Read back, never minted
	// here — see ManualKeyReason.
	SourceID string `json:"source_id"`
	Title    string `json:"title"`
	ShareURL string `json:"share_url"`
	MediaKey string `json:"media_key"`
	// State is documented ONLY on the scheduled-broadcast schemas, and even
	// there the published list ends in an ellipsis: "Scheduler state (Created,
	// Scheduled, Running, …)". X is explicitly declining to enumerate its own
	// states, and Broadcast.state is undescribed, so it may not even share that
	// vocabulary. NEVER write an exhaustive switch over this. Log what arrives.
	State string `json:"state"`
	// StartMS and EndMS are kept as the strings X sends. The suffix implies
	// milliseconds and the spec does not say so — only scheduled_start_ms and
	// scheduled_end_ms carry "ms since Unix epoch (decimal string)", and those
	// are on the REQUEST schema. Parsing these into a time.Time would encode
	// that inference as a fact, and a factor-of-1000 error in a timestamp is
	// the kind of bug that reads as data corruption. A caller that needs a
	// time can convert with the assumption written at its own call site.
	StartMS string `json:"start_ms"`
	EndMS   string `json:"end_ms"`
}

// xBroadcastFields is the broadcast.fields query parameter, and it is sent
// explicitly on every read.
//
// The parameter is optional and the spec does not state what a request without
// it returns. Asking for exactly what this struct decodes means a field
// arriving empty is X saying so, rather than polyemesis having failed to ask.
// TestXAsksForEveryFieldItDecodes pins the two together in both directions.
const xBroadcastFields = "id,broadcast_id,twitter_user_id,source_id,title,share_url,media_key,state,start_ms,end_ms"

// xProblem is X's typed error object. It arrives two ways: as the body of a
// failed request, and -- the trap -- inside the `errors` array of a 200 that
// also carries no data. Decoding it is how a refusal keeps X's own words.
type xProblem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

func (p xProblem) String() string {
	parts := make([]string, 0, 3)
	for _, s := range []string{p.Title, p.Detail, p.Type} {
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ": ")
}

// xListBroadcastsResponse mirrors ListBroadcastsResponse. meta.next_token is
// decoded and deliberately not followed: pagination needs a caller with an
// opinion about page size, and there is none yet. Decoding it means the next
// person can see that more pages exist rather than concluding from a short list
// that the account has few broadcasts.
type xListBroadcastsResponse struct {
	Data   []XBroadcast `json:"data"`
	Errors []xProblem   `json:"errors"`
	Meta   struct {
		NextToken   string `json:"next_token"`
		ResultCount int    `json:"result_count"`
	} `json:"meta"`
}

// Broadcasts lists the authenticated account's broadcasts, described by X as
// "the authenticated user's live-video broadcasts".
//
// clientID is unused, here and throughout this provider: X authenticates with
// the bearer token alone. It stays in the signature because Twitch's Helix needs
// it and Provider is one interface.
func (x *X) Broadcasts(ctx context.Context, clientID, accessToken string) ([]XBroadcast, error) {
	var out xListBroadcastsResponse
	endpoint := x.apiEndpoint() + "/2/broadcasts?" + url.Values{
		"broadcast.fields": {xBroadcastFields},
	}.Encode()
	if err := getJSON(ctx, endpoint, accessToken, nil, &out); err != nil {
		return nil, xRefused("list broadcasts", err)
	}
	// A 200 carrying problems and no data is a refusal wearing a success code.
	if len(out.Data) == 0 && len(out.Errors) > 0 {
		return nil, xRefused("list broadcasts", xProblems(out.Errors))
	}
	return out.Data, nil
}

// BroadcastByID reads one broadcast, described by X as "a broadcast owned by the
// authenticated user". A broadcast belonging to somebody else is not readable
// with this token, so this is not a lookup for arbitrary ids.
func (x *X) BroadcastByID(ctx context.Context, clientID, accessToken, id string) (*XBroadcast, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("no broadcast id given")
	}
	var out struct {
		Data   XBroadcast `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	endpoint := x.apiEndpoint() + "/2/broadcasts/" + url.PathEscape(id) + "?" + url.Values{
		"broadcast.fields": {xBroadcastFields},
	}.Encode()
	if err := getJSON(ctx, endpoint, accessToken, nil, &out); err != nil {
		return nil, xRefused("get broadcast "+id, err)
	}
	if out.Data.ID == "" && out.Data.BroadcastID == "" && len(out.Errors) > 0 {
		return nil, xRefused("get broadcast "+id, xProblems(out.Errors))
	}
	return &out.Data, nil
}

func xProblems(ps []xProblem) error {
	msgs := make([]string, 0, len(ps))
	for _, p := range ps {
		if s := p.String(); s != "" {
			msgs = append(msgs, s)
		}
	}
	if len(msgs) == 0 {
		// X sent an errors array this build could not read a single word out
		// of. Saying that beats an empty parenthesis.
		return fmt.Errorf("X reported an error it did not describe in any field polyemesis decodes")
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

// xRefused is how every Broadcasts failure reaches an operator, and its job is
// to add no cause.
//
// X publishes NO error taxonomy for this family: all thirteen operations declare
// a success code and a single generic `default`, described verbatim as "The
// request has failed." There is no status code, no error title and no `type` URI
// documented for any particular refusal — not even for the one behavioural
// precondition X does describe in prose (go-live rejects a broadcast that was
// not created with manual_publish). So the only honest thing to show is what X
// said, and the only honest thing to add is that nothing more was published.
//
// The second sentence is not padding. No X pricing or tier page names the
// Broadcasts family at all, so whether a given developer app may call these
// endpoints is answerable only by a live request — which means a refusal here is
// exactly as likely to be "your app has no access to this family" as it is to be
// anything about the request. An operator debugging a request that is in fact
// fine deserves to know that before they rewrite it.
func xRefused(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("X refused to %s: %w. X documents no error taxonomy for its "+
		"broadcast endpoints — every one of them declares only success and a generic "+
		"\"The request has failed.\" — so that is X's own wording and polyemesis has no "+
		"documented cause to add. Note also that no X pricing or tier page names the "+
		"broadcast endpoints, so a refusal can equally mean this developer app has no "+
		"access to them", op, err)
}

// Account identifies the connection by the numeric id X stamps on a broadcast,
// because with these scopes there is nowhere else it appears.
//
// GET /2/users/me — the one endpoint in the spec that returns a handle or a
// display name — declares users.read and tweet.read, neither of which polyemesis
// requests; see Scopes. Sweeping all 178 operations, the thirteen Broadcasts
// operations are the ONLY ones a broadcast.read token may call, and
// twitter_user_id on the broadcast object is the only identity any of them
// returns. So Name is that id too: X publishes no handle to this token, and
// synthesising "@something" from a numeric id would put a handle on the screen
// that X never said.
//
// The consequence, which is a real product limitation rather than a bug: an
// account that has never broadcast has no broadcast object, so there is nothing
// to read an id off and connecting cannot complete. That is said out loud in the
// error rather than rendered as an empty account row, because an account stored
// with no ref is a connection that fails later at a worse moment.
func (x *X) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	bs, err := x.Broadcasts(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}
	for _, b := range bs {
		if b.TwitterUserID != "" {
			return &Account{Name: b.TwitterUserID, Ref: b.TwitterUserID}, nil
		}
	}
	if len(bs) == 0 {
		return nil, fmt.Errorf("X returned no broadcasts for this token, and a broadcast is " +
			"the only place X puts an account id that these scopes may read: the endpoint that " +
			"returns your handle needs scopes polyemesis does not ask for. Create a broadcast " +
			"on the account — a scheduled one is enough — and connect again")
	}
	return nil, fmt.Errorf("X returned %d broadcast(s), none of which carried twitter_user_id, "+
		"which is the only account identifier these scopes can read", len(bs))
}

// -------------------------------------------------------------- the missing key

// ManualKeyReason is what an operator reads next to the stream-key field.
//
// It carries no URL and no protocol scheme on purpose, and a test enforces that:
// an ingest hostname invented to make the message look complete is exactly the
// failure this branch of the package exists to avoid. kick.go's comment says the
// same thing and it is worth repeating here, because X makes the temptation
// sharper than Kick ever did — the spec NAMES the thing it will not give you.
// CreateScheduledBroadcastRequest.source_id is required and described as an
// ingest id "same as sources rtmp_stream_key", every scheduled response echoes
// the bound value back, and there is no `sources` collection among the 149
// published paths for any of that to refer to. A key polyemesis can read back
// off a broadcast that already exists is not a key polyemesis can obtain, and no
// ingest host appears anywhere in the spec either.
func (x *X) ManualKeyReason() string {
	return "X publishes no endpoint that creates or lists a stream key: its API requires " +
		"a source id when you create a broadcast and hands the same value back afterwards, " +
		"but nothing documented mints one, and no ingest server address appears in its API " +
		"at all. Create the source in X's own producer tooling and paste the server URL and " +
		"key here. Everything else on this account — sign-in, and reading your broadcasts — " +
		"works without them."
}

// Ingest refuses without making a request, because there is no request to make.
//
// It returns ErrNoStreamKeyAPI so callers can errors.Is it and send the operator
// to the paste field: a platform that never had a key endpoint is not a
// transport problem, and reporting it as a bad gateway invites a retry that can
// never work.
//
// A future agent will be tempted to "finish" this against /2/sources, because
// the spec's own source_id description refers to "sources rtmp_stream_key". DO
// NOT. That collection is not among the 149 paths, and a fabricated endpoint
// turns a clear instruction into a 404 nobody can diagnose. If X publishes a
// sources API, this becomes a real fetch and this comment is the record of what
// changed.
func (x *X) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	return nil, fmt.Errorf("%w. %s", ErrNoStreamKeyAPI, x.ManualKeyReason())
}
