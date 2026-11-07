package oauth

// X (Twitter) Livestream: the sign-in, the stream key, the broadcast lifecycle
// and the metadata that X's Livestream API (docs.x.com/livestream-api) exposes.
//
// SWITCHED OFF BY DEFAULT. X no longer asks for a whitelist or an access form:
// docs.x.com/livestream-api/getting-started lists a developer app, OAuth 2.0
// with broadcast.read and broadcast.write, an account allowed to go live, and an
// RTMP encoder. polyemesis still keeps the family opt-in, and until the operator
// sets POLYEMESIS_X_LIVESTREAM ("1" or "true", read once when the provider is
// built) every operation in this file that touches it refuses WITHOUT making a
// request, and the copy the operator sees is "needs X Livestream switched on
// (POLYEMESIS_X_LIVESTREAM=1)". Paste-the-key keeps working regardless.
//
// The calls follow X's Livestream docs as of 2026-11-06, including the migration
// guide (docs.x.com/livestream-api/migration-guide). The deprecated
// /2/users/{id}/sources and /2/users/{id}/broadcasts routes are removed on
// 2027-01-01, so nothing here calls them: sources are /2/sources, going live is
// POST /2/broadcasts, ending is PUT /2/broadcasts/{id}/state, and the owner comes
// from the access token. The ones most likely to need a change after a live run
// are noted at the call sites.
//
// Scopes requested: broadcast.read and broadcast.write (the Livestream family),
// offline.access (a refresh token), users.read (GET /2/users/me, the only endpoint
// that returns a handle), and tweet.read (the broadcast.chat activity
// subscription, which the activity API gates on tweet.read for every event that is
// not a DM, like or mute).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// xAPIBase is the production host for tokens and data.
const xAPIBase = platform.XAPIHost

// xAuthorizeBase and xAuthorizePath are the consent page, kept apart from the
// token and data host so a change to one does not move the other.
//
// X's OpenAPI spec lists https://api.x.com/2/oauth2/authorize, but that address
// answers a browser with HTTP 403 "Access to api.x.com was denied". The consent
// page is the user-access-token guide's https://x.com/i/oauth2/authorize.
// Confirmed by a live request on 2026-11-06. The token endpoint stays on
// api.x.com (/2/oauth2/token).
const (
	xAuthorizeBase = "https://x.com"
	xAuthorizePath = "/i/oauth2/authorize"
)

// xGateCopy is the copy shown when the Livestream family is switched off. It names
// the switch, not an X access form, and the tests pin it.
const xGateCopy = "needs X Livestream switched on (POLYEMESIS_X_LIVESTREAM=1)"

// xSourceName labels the stream source polyemesis mints for itself, so a
// connect reuses it rather than adding one per attempt.
const xSourceName = "polyemesis"

// ErrXLivestreamOff is returned by every Livestream operation while the gate is
// closed. Callers errors.Is against it to choose their wording.
var ErrXLivestreamOff = errors.New("X Livestream is switched off: " + xGateCopy)

// X implements X's OAuth 2.0 authorization-code flow (with PKCE), the Livestream
// family behind the gate, and the account read.
type X struct {
	endpoints
	// livestream is the gate. False is the production default.
	livestream bool
	// calls holds the rate-limit holds X's answers set and the cached user id (x_ratelimit.go).
	calls xCallState
}

// Compile-time assertions. The EntitlementGated one is the gate's probe.
var (
	_ Provider            = (*X)(nil)
	_ ManualKey           = (*X)(nil)
	_ EntitlementGated    = (*X)(nil)
	_ BroadcastLifecycler = (*X)(nil)
	_ MetadataPusher      = (*X)(nil)
	_ StreamKeyReader     = (*X)(nil)
)

// NewX builds the provider. The Livestream gate is closed unless
// WithXLivestream(true) says the operator opened it (startupenv reads
// POLYEMESIS_X_LIVESTREAM once at startup; nothing here reads the environment).
func NewX(opts ...ProviderOption) *X {
	e := newEndpoints(opts)
	return &X{endpoints: e, livestream: e.xLivestream}
}

// WithLivestreamAccess opens the gate on this instance. Tests use it;
// production passes WithXLivestream when it builds the provider set.
func (x *X) WithLivestreamAccess() *X {
	x.livestream = true
	return x
}

// LivestreamAccess reports whether the Livestream family is switched on.
func (x *X) LivestreamAccess() bool { return x.livestream }

func (x *X) authEndpoint() string { return x.authBase(xAPIBase) }
func (x *X) apiEndpoint() string  { return x.apiBase(xAPIBase) }

func (x *X) Platform() db.Platform { return db.PlatformX }

// Scopes is the list the consent screen shows. See the file header for why each
// one is here.
//
// The last two are what clip publishing needs (tweet.write, media.write), read
// from the platform registry so publish and sign-in cannot disagree.
func (x *X) Scopes() []string {
	return append([]string{
		"broadcast.read",
		"broadcast.write",
		"offline.access",
		"users.read",
		"tweet.read",
	}, platform.Must(platform.X).PublishScopes...)
}

// ScopeVersion is 2. Version 1 was the five Livestream scopes; version 2 adds
// tweet.write and media.write so clips can be posted, which an account connected
// at version 1 does not hold and has to reconnect once to get. Bump it by hand
// when Scopes changes; the scope ledger in testdata/scope-versions.json must
// match.
func (x *X) ScopeVersion() int { return 2 }

// PKCE is ON. X's Livestream authentication guide recommends the PKCE flow for
// every new integration; the challenge is sent at authorize and the verifier at
// exchange.
func (x *X) PKCE() bool { return true }

func (x *X) AuthURL(clientID, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(x.Scopes(), " "))
	q.Set("state", state)
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	return x.authBase(xAuthorizeBase) + xAuthorizePath + "?" + q.Encode()
}

// Exchange trades the code for tokens. A confidential client (one with a secret)
// authenticates with HTTP Basic, which is what X documents for /2/oauth2/token
// (docs-x section 1, CONFIRMED); the secret never travels in the body. A public
// client sends client_id in the body and no secret. client_id stays in the body
// in both cases, as every example in X's guide has it.
func (x *X) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error) {
	form := url.Values{
		"client_id":    {clientID},
		"code":         {code},
		"grant_type":   {"authorization_code"},
		"redirect_uri": {redirectURI},
	}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	return postForm(ctx, x.tokenEndpoint(), form, xClientAuth(clientID, clientSecret))
}

// xClientAuth is the Authorization header a confidential client sends to the
// token endpoint, or nil for a public client. RFC 6749 section 2.3.1: both halves
// are form-encoded before they are joined and base64-encoded.
func xClientAuth(clientID, clientSecret string) map[string]string {
	if clientSecret == "" {
		return nil
	}
	pair := url.QueryEscape(clientID) + ":" + url.QueryEscape(clientSecret)
	return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(pair))}
}

func (x *X) tokenEndpoint() string { return x.authEndpoint() + "/2/oauth2/token" }

// Refresh spends the refresh token that offline.access issues. A response that
// omits a rotated token keeps the old one, so the account does not disconnect an
// hour later.
func (x *X) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	tok, err := postForm(ctx, x.tokenEndpoint(), url.Values{
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}, xClientAuth(clientID, clientSecret))
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

// ------------------------------------------------------------------ transport

// xStatusError is a non-2xx answer from X. It carries the status for the gate
// probe and a message built from X's own problem objects. It never carries the
// raw body, because a body can hold a stream key and an error is logged.
type xStatusError struct {
	Status   int
	Problems string
}

func (e *xStatusError) Error() string {
	if e.Problems == "" {
		return fmt.Sprintf("X returned %d", e.Status)
	}
	return fmt.Sprintf("X returned %d: %s", e.Status, e.Problems)
}

// do performs one authenticated JSON call. payload nil sends no body. out nil
// discards the response. A rate-limit hold on the token refuses the call before
// any request (x_ratelimit.go).
func (x *X) do(ctx context.Context, method, endpoint, accessToken string, payload, out any) error {
	_, err := x.doStatus(ctx, method, endpoint, accessToken, payload, out)
	return err
}

// doStatus is do, and it also returns the HTTP status of an answer that was read (0 when none was).
func (x *X) doStatus(ctx context.Context, method, endpoint, accessToken string, payload, out any) (int, error) {
	fp := xTokenFingerprint(accessToken, x.apiEndpoint())
	if until, held := x.calls.heldUntil(fp); held {
		return 0, &xRateLimitedError{Until: until, Held: true}
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if until, limited := x.calls.rateHoldFrom(resp); limited {
		x.calls.hold(fp, until)
		if resp.StatusCode == http.StatusTooManyRequests {
			return resp.StatusCode, &xRateLimitedError{Until: until, Problems: xProblemText(raw)}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, &xStatusError{Status: resp.StatusCode, Problems: xProblemText(raw)}
	}
	if readErr != nil {
		// A body cut off mid-read is not an answer: a truncated body that happens to parse, or a write whose
		// answer is discarded, would otherwise read as a success.
		return resp.StatusCode, fmt.Errorf("X's answer was cut off: %w", readErr)
	}
	if out == nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, json.Unmarshal(raw, out)
}

// xProblemText extracts X's own words from an error body and nothing else. A
// body that is not JSON yields nothing rather than a snippet, so no unexpected
// field can reach a message.
func xProblemText(raw []byte) string {
	var env struct {
		Errors []xProblem `json:"errors"`
		xProblem
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	ps := env.Errors
	if env.xProblem.String() != "" {
		ps = append(ps, env.xProblem)
	}
	var parts []string
	for _, p := range ps {
		if s := p.String(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "; ")
}

// -------------------------------------------------------------- identity

// xUser is GET /2/users/me's data object.
type xUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// me returns the authenticated user, for the account read. The Livestream routes take the user from the access token
// and need no id.
func (x *X) me(ctx context.Context, accessToken string) (xUser, error) {
	fp := xTokenFingerprint(accessToken, x.apiEndpoint())
	if u, ok := x.calls.cachedUser(fp); ok {
		return u, nil
	}
	var out struct {
		Data   xUser      `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	endpoint := x.apiEndpoint() + "/2/users/me?" + url.Values{
		"user.fields": {"username,name"},
	}.Encode()
	if err := x.do(ctx, http.MethodGet, endpoint, accessToken, nil, &out); err != nil {
		return xUser{}, fmt.Errorf("X would not say which account this token belongs to: %w", err)
	}
	if out.Data.ID == "" {
		if len(out.Errors) > 0 {
			return xUser{}, fmt.Errorf("X would not say which account this token belongs to: %s", xProblems(out.Errors))
		}
		return xUser{}, errors.New("X returned no user id for this token")
	}
	x.calls.rememberUser(fp, out.Data)
	return out.Data, nil
}

// Account names the connection by the handle and keys it by the numeric id.
// /2/users/me needs no broadcast, so an account that has never streamed connects.
func (x *X) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	u, err := x.me(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	name := u.Username
	if name == "" {
		name = u.ID
	}
	return &Account{Name: name, Ref: u.ID}, nil
}

// ------------------------------------------------------------ the gate probe

// EntitlementReason is the gate in X's terms, usable before anything is
// connected. It is what the setup guide and the capability row show.
func (x *X) EntitlementReason() string {
	return "Livestream " + xGateCopy + ". It also needs an account that X allows to go live " +
		"(not protected). Signing in works without it. Until it is switched on, or when the account cannot " +
		"use the API, paste the stream key from X's producer tooling."
}

// xGateError is what the Livestream family returns while the switch is off. Its
// text is the gate copy the operator reads, and it names the switch, not the
// operator's account. It matches the platform-wide ErrNotEntitled, which the
// connect warning keys on, and ErrXLivestreamOff, which names the cause.
type xGateError struct{}

func (xGateError) Error() string {
	return "Livestream " + xGateCopy + ". Sign-in works without it; paste the stream key from " +
		"X's producer tooling until it is switched on."
}

func (xGateError) Is(target error) bool {
	return target == ErrNotEntitled || target == ErrXLivestreamOff
}

// CheckEntitlement probes the Livestream family once with the operator's fresh
// token. It lists the account's stream sources (GET /2/sources, which takes the
// user from the token), a read that changes nothing. A 403 or 412 is a refusal of
// the family, such as a token without broadcast.read. Any other failure is
// reported as a failed probe, never as a gate.
//
// An account that cannot go live is not found here: the list answers for any
// account, and X reports the ineligibility (403) on Go Live. That is surfaced when
// the broadcast is opened.
//
// While the gate is closed it refuses WITHOUT a request and the refusal is
// xGateError, which carries the gate copy.
func (x *X) CheckEntitlement(ctx context.Context, clientID, accessToken string) error {
	if !x.livestream {
		return xGateError{}
	}
	var out struct {
		Data []xSource `json:"data"`
	}
	err := x.do(ctx, http.MethodGet, x.sourcesEndpoint(), accessToken, nil, &out)
	if err == nil {
		return nil
	}
	var se *xStatusError
	if errors.As(err, &se) && (se.Status == http.StatusForbidden || se.Status == http.StatusPreconditionFailed) {
		return fmt.Errorf("%w: %s", ErrNotEntitled, err.Error())
	}
	return err
}

// --------------------------------------------------------- the stream key

// xSource is a stream source as GET /2/sources and POST /2/sources return it. The id is X's own 19-digit source id,
// which is NOT secret and is NOT the stream key: on /2/sources the key is a separate credential, returned only by the
// create answer (StreamKey) and by GET /2/sources/{id}/stream_key. The list carries no key.
type xSource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RTMPURL   string `json:"rtmp_url"`
	RTMPSURL  string `json:"rtmps_url"`
	StreamKey string `json:"stream_key"`
	IsActive  bool   `json:"is_stream_active"`
}

// ingestURL prefers RTMPS, which X documents as the port-443 endpoint. The host
// is whatever X returned; nothing here constructs one.
func (s xSource) ingestURL() string {
	if s.RTMPSURL != "" {
		return s.RTMPSURL
	}
	return s.RTMPURL
}

// xSourceIDPattern is the shape of a source id: digits, 19 of them on X's side. X answers 400 to anything else, so a
// value outside it is refused before a request.
var xSourceIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// sourcesEndpoint is the source list and create address. There is no user in it: X takes the owner from the token.
func (x *X) sourcesEndpoint() string { return x.apiEndpoint() + "/2/sources" }

// StreamKeys lists every source's stream key, read-only. It backs the cross-check that compares a stored key with what
// X holds. The list carries no keys, so each is read from its own source: one call for the list plus one per source.
func (x *X) StreamKeys(ctx context.Context, clientID, accessToken string) ([]string, error) {
	if !x.livestream {
		return nil, ErrXLivestreamOff
	}
	srcs, err := x.listSources(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(srcs))
	for _, s := range srcs {
		k, err := x.sourceStreamKey(ctx, accessToken, s.ID)
		if err != nil {
			return nil, err
		}
		if k != "" {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// listSources is GET /2/sources: {"data":[...],"meta":{"result_count":n}}.
func (x *X) listSources(ctx context.Context, accessToken string) ([]xSource, error) {
	var out struct {
		Data   []xSource  `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	if err := x.do(ctx, http.MethodGet, x.sourcesEndpoint(), accessToken, nil, &out); err != nil {
		return nil, fmt.Errorf("X would not list stream sources: %w", err)
	}
	if len(out.Data) == 0 && len(out.Errors) > 0 {
		return nil, fmt.Errorf("X would not list stream sources: %s", xProblems(out.Errors))
	}
	return out.Data, nil
}

// sourceStreamKey is GET /2/sources/{id}/stream_key, the only read of a key after the create. The key is a credential,
// so it is never put in an error here.
func (x *X) sourceStreamKey(ctx context.Context, accessToken, sourceID string) (string, error) {
	if !xSourceIDPattern.MatchString(sourceID) {
		return "", errors.New("X returned a stream source without a usable id")
	}
	var out struct {
		Data struct {
			StreamKey string `json:"stream_key"`
		} `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	if err := x.do(ctx, http.MethodGet, x.sourcesEndpoint()+"/"+sourceID+"/stream_key", accessToken, nil, &out); err != nil {
		return "", fmt.Errorf("X would not give the stream key of source %s: %w", sourceID, err)
	}
	if out.Data.StreamKey == "" && len(out.Errors) > 0 {
		return "", fmt.Errorf("X would not give the stream key of source %s: %s", sourceID, xProblems(out.Errors))
	}
	return out.Data.StreamKey, nil
}

// recommendedRegion is GET /2/region, which POST /2/sources needs: the create requires a region and no longer defaults.
func (x *X) recommendedRegion(ctx context.Context, accessToken string) (string, error) {
	var out struct {
		Region string `json:"region"`
	}
	if err := x.do(ctx, http.MethodGet, x.apiEndpoint()+"/2/region", accessToken, nil, &out); err != nil {
		return "", fmt.Errorf("X would not say which ingest region to use: %w", err)
	}
	if strings.TrimSpace(out.Region) == "" {
		return "", errors.New("X returned no ingest region")
	}
	return out.Region, nil
}

// Ingest returns the polyemesis source's URL and key, minting the source the
// first time. While the gate is closed it refuses without a request and sends the
// operator to the paste field, with ErrNoStreamKeyAPI so callers route it the
// same way they route Kick and Vimeo.
func (x *X) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	if !x.livestream {
		return nil, fmt.Errorf("%w. X Livestream %s", ErrNoStreamKeyAPI, xGateCopy)
	}
	srcs, err := x.listSources(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	for _, s := range srcs {
		if s.Name != xSourceName || s.ingestURL() == "" {
			continue
		}
		key, err := x.sourceStreamKey(ctx, accessToken, s.ID)
		if err != nil {
			return nil, err
		}
		if key != "" {
			return &Ingest{URL: s.ingestURL(), Key: key}, nil
		}
	}
	// The create takes exactly {name, region}; any other field is a 400. The region is the one X recommends.
	region, err := x.recommendedRegion(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data   xSource    `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	if err := x.do(ctx, http.MethodPost, x.sourcesEndpoint(), accessToken,
		map[string]string{"name": xSourceName, "region": region}, &out); err != nil {
		return nil, fmt.Errorf("X would not create a stream source: %w", err)
	}
	s := out.Data
	if s.StreamKey == "" || s.ingestURL() == "" {
		if len(out.Errors) > 0 {
			return nil, fmt.Errorf("X would not create a stream source: %s", xProblems(out.Errors))
		}
		return nil, errors.New("X created the stream source but returned no ingest address or key")
	}
	return &Ingest{URL: s.ingestURL(), Key: s.StreamKey}, nil
}

// --------------------------------------------------------- broadcast reads

// XBroadcast is the subset of X's Broadcast schema polyemesis decodes. The
// spec's property descriptions are sparse, so only identifiers and state are
// decoded. The viewer counts are left out on purpose: X does not describe them,
// and a number that nothing defines should not reach an operator as a count.
type XBroadcast struct {
	ID            string `json:"id"`
	BroadcastID   string `json:"broadcast_id"`
	TwitterUserID string `json:"twitter_user_id"`
	SourceID      string `json:"source_id"`
	Title         string `json:"title"`
	ShareURL      string `json:"share_url"`
	MediaKey      string `json:"media_key"`
	// State is one of NOT_STARTED, PRE_PUBLISHED, RUNNING, TIMED_OUT, ENDED or
	// CANCELED, as X's Broadcast schema describes it. It is passed through
	// unmapped, so the operator sees X's own word.
	State   string `json:"state"`
	StartMS string `json:"start_ms"`
	EndMS   string `json:"end_ms"`
}

// xBroadcastFields is the broadcast.fields parameter, sent explicitly on every
// read so an empty field means X sent nothing rather than that it was not asked.
const xBroadcastFields = "id,broadcast_id,twitter_user_id,source_id,title,share_url,media_key,state,start_ms,end_ms"

// xProblem is X's typed error object. Two shapes reach it: the RFC 7807 problem
// (title, detail, type) X uses for a 400, and {error_type, message}, which the
// broadcasts overview lists for 403, 404, 412 and 503 (docs-x section 10). A 412
// such as "requires X Premium" arrives in the second shape, and without these
// two fields it reached the operator as a bare "X returned 412".
type xProblem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Status    int    `json:"status"`
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

func (p xProblem) String() string {
	parts := make([]string, 0, 5)
	for _, s := range []string{p.Title, p.Message, p.Detail, p.ErrorType, p.Type} {
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ": ")
}

func xProblems(ps []xProblem) string {
	msgs := make([]string, 0, len(ps))
	for _, p := range ps {
		if s := p.String(); s != "" {
			msgs = append(msgs, s)
		}
	}
	if len(msgs) == 0 {
		return "X reported an error with no message"
	}
	return strings.Join(msgs, "; ")
}

// xListBroadcastsResponse mirrors ListBroadcastsResponse.
type xListBroadcastsResponse struct {
	Data   []XBroadcast `json:"data"`
	Errors []xProblem   `json:"errors"`
}

// Broadcasts lists the authenticated account's broadcasts. Gated.
func (x *X) Broadcasts(ctx context.Context, clientID, accessToken string) ([]XBroadcast, error) {
	if !x.livestream {
		return nil, ErrXLivestreamOff
	}
	var out xListBroadcastsResponse
	endpoint := x.apiEndpoint() + "/2/broadcasts?" + url.Values{
		"broadcast.fields": {xBroadcastFields},
	}.Encode()
	if err := x.do(ctx, http.MethodGet, endpoint, accessToken, nil, &out); err != nil {
		return nil, fmt.Errorf("X would not list broadcasts: %w", err)
	}
	if len(out.Data) == 0 && len(out.Errors) > 0 {
		return nil, fmt.Errorf("X would not list broadcasts: %s", xProblems(out.Errors))
	}
	return out.Data, nil
}

// BroadcastByID reads one broadcast owned by the authenticated user. Gated.
func (x *X) BroadcastByID(ctx context.Context, clientID, accessToken, id string) (*XBroadcast, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("no broadcast id given")
	}
	if !x.livestream {
		return nil, ErrXLivestreamOff
	}
	var out struct {
		Data   XBroadcast `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	endpoint := x.apiEndpoint() + "/2/broadcasts/" + url.PathEscape(id) + "?" + url.Values{
		"broadcast.fields": {xBroadcastFields},
	}.Encode()
	if err := x.do(ctx, http.MethodGet, endpoint, accessToken, nil, &out); err != nil {
		return nil, fmt.Errorf("X would not read broadcast %s: %w", id, err)
	}
	if out.Data.ID == "" && out.Data.BroadcastID == "" {
		if len(out.Errors) > 0 {
			return nil, fmt.Errorf("X would not read broadcast %s: %s", id, xProblems(out.Errors))
		}
		// An empty 2xx is not a broadcast with no state: read as one, the coordinator would plan from a zero value.
		return nil, fmt.Errorf("X answered the read of broadcast %s with no broadcast", id)
	}
	return &out.Data, nil
}

// BroadcastState reads the broadcast for the lifecycle. X publishes no
// stream-liveness read on the broadcast, so StreamActive is read from the bound
// source's is_stream_active, the only receive-side signal X exposes. It is nil
// when the source is not found, so an unknown answer is never reported as "no
// bytes". MonitorStream has no X equivalent and stays nil.
//
// A pending handle (OpenBroadcast's) names no broadcast on X, so none is read by
// id. But the go-live behind it may have succeeded with its answer lost, or with
// the write that records the real id failing, so the account's broadcasts are
// listed for a live one on the handle's source. If there is one, that broadcast
// (real id, its state) is what is returned and the caller adopts it, so a switch-off
// still ends it. If there is none the state is a synthetic NOT_STARTED, and the
// source is the only other thing asked about. A source that is gone from X is an
// error, because nothing can ever go live on it.
func (x *X) BroadcastState(ctx context.Context, accessToken, broadcastID string) (*BroadcastLifecycleState, error) {
	st := &BroadcastLifecycleState{BroadcastID: broadcastID}
	if sourceID, pending := xPendingSource(broadcastID); pending {
		if !x.livestream {
			return nil, ErrXLivestreamOff
		}
		bs, err := x.Broadcasts(ctx, "", accessToken)
		if err != nil {
			return nil, err
		}
		st.Status, st.BoundStreamID = XStateNotStarted, sourceID
		for _, b := range bs {
			if b.SourceID != sourceID || (b.State != XStateRunning && b.State != XStatePrePublished) {
				continue
			}
			id := b.ID
			if id == "" {
				id = b.BroadcastID
			}
			if id != "" {
				st.BroadcastID, st.Status, st.Title = id, b.State, b.Title
				break
			}
		}
		if err := x.fillStreamActive(ctx, accessToken, st, true); err != nil {
			return nil, err
		}
		return st, nil
	}
	b, err := x.BroadcastByID(ctx, "", accessToken, broadcastID)
	if err != nil {
		return nil, err
	}
	st.Title, st.Status, st.BoundStreamID = b.Title, b.State, b.SourceID
	if err := x.fillStreamActive(ctx, accessToken, st, false); err != nil {
		return nil, err
	}
	return st, nil
}

// fillStreamActive sets StreamActive and StreamStatus from the source st is bound to. A source that is not in the list
// leaves them unknown (nil), unless mustExist is set: a pending handle's source that X no longer has is an error.
func (x *X) fillStreamActive(ctx context.Context, accessToken string, st *BroadcastLifecycleState, mustExist bool) error {
	if st.BoundStreamID == "" {
		return nil
	}
	srcs, err := x.listSources(ctx, accessToken)
	if err != nil {
		return err
	}
	for _, s := range srcs {
		// The broadcast's source_id is the source id, the same value as the source's id in the list.
		if s.ID == st.BoundStreamID {
			active := s.IsActive
			st.StreamActive = &active
			if active {
				st.StreamStatus = "active"
			} else {
				st.StreamStatus = "inactive"
			}
			return nil
		}
	}
	if mustExist {
		return errors.New("X no longer has the stream source this destination was set up on; it may have been " +
			"deleted in X. Reconnect the X account in polyemesis so it can create a new one")
	}
	return nil
}

// ------------------------------------------------------------ go live

// X's six broadcast states, as its Broadcast schema and the overview page name
// them. Passed through unmapped in Status; XStateKnown is the only gate on them.
const (
	XStateNotStarted   = "NOT_STARTED"
	XStatePrePublished = "PRE_PUBLISHED"
	XStateRunning      = "RUNNING"
	XStateEnded        = "ENDED"
	XStateTimedOut     = "TIMED_OUT"
	XStateCanceled     = "CANCELED"
)

// XStateKnown reports whether s is one of X's six documented states, matched
// exactly. An unrecognised word is unknown, and the coordinator treats unknown as
// "do not act", never as live.
func XStateKnown(s string) bool {
	switch s {
	case XStateNotStarted, XStatePrePublished, XStateRunning, XStateEnded, XStateTimedOut, XStateCanceled:
		return true
	}
	return false
}

// xChatOptionMax is the largest chat_option X documents: 0 default through 6.
const xChatOptionMax = 6

// xStreamKeyPattern is the shape polyemesis accepts for a stored stream key before it looks the key up. It is a guard
// against a pasted value with spaces or a path in it, and its refusal never echoes the value, which is a credential.
var xStreamKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9]{1,32}$`)

// xPendingPrefix starts the handle OpenBroadcast returns. X's Go Live (POST /2/broadcasts) creates the broadcast and
// publishes it in one call, with no unpublished NOT_STARTED step, and the broadcast is public the moment it returns
// (it is ended as TIMED_OUT if no video arrives within five minutes). The coordinator, though, opens early and only goes
// live once the encoder is sending, so what it holds until then is this handle: "pending:" plus the source id. It names
// no broadcast on X, and nothing here ever sends it to X as one.
const xPendingPrefix = "pending:"

// xPendingHandle is the handle for a source that has no broadcast yet.
func xPendingHandle(sourceID string) string { return xPendingPrefix + sourceID }

// XPendingHandle is xPendingHandle for the lifecycle coordinator, which stores the source id and rebuilds the handle.
func XPendingHandle(sourceID string) string { return xPendingHandle(sourceID) }

// XPendingSource is xPendingSource for the lifecycle coordinator.
func XPendingSource(id string) (string, bool) { return xPendingSource(id) }

// xPendingSource reports whether id is a pending handle and, if so, the source it stands for. A real broadcast id is
// never mistaken for one: it needs the prefix and a source id after it.
func xPendingSource(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, xPendingPrefix)
	if !ok || !xSourceIDPattern.MatchString(rest) {
		return "", false
	}
	return rest, true
}

// XPublish is what going live carries.
type XPublish struct {
	// Title is the broadcast title. Empty means the title is left out and X uses the broadcaster's display name.
	// X documents no character limit beyond its post length, so none is enforced here.
	Title string
	// ChatOption is nil when unset, which leaves the field out so X applies its
	// default. Otherwise it must be 0 to 6, and it is sent as a JSON integer.
	ChatOption *int
	// ShouldNotTweet suppresses the announcement post X makes by default. False
	// leaves the field out, so X posts as it always has. It is sent as a JSON
	// boolean, which is the only type the route accepts.
	ShouldNotTweet bool
}

// XPublishResult is what going live answered.
type XPublishResult struct {
	// ID is the real broadcast id. It replaces the pending handle the caller held.
	ID    string
	State string
	// Existing is true when X answered 200: the source already had a live broadcast and this is it, unchanged, with
	// the request's title and options ignored. The usual cause is a repeat after a lost answer.
	Existing bool
	// Warnings is kept for callers that report them. X's Go Live answer has no tweet_error: a missing tweet_id is read
	// later from GET /2/broadcasts/{id}, so nothing sets this today.
	Warnings []string
}

// classifyXGoLive turns a refused go-live into what the coordinator can act on. X's three documented refusals are told
// apart, because they need three different responses: 403 (the account cannot go live) is terminal, a 409 that names
// another broadcast is a fault the operator fixes, and any other 409 is the expected wait for the source to receive
// video. Anything else passes through as the wrapped error. X's own words stay in the text.
func classifyXGoLive(err error, pending bool, broadcastID string) error {
	wrapped := fmt.Errorf("X would not take the stream source live: %w", err)
	var se *xStatusError
	if !errors.As(err, &se) {
		return wrapped
	}
	id := broadcastID
	if pending {
		id = ""
	}
	refuse := func(r TransitionRefusal, reason string) error {
		return &TransitionRefused{BroadcastID: id, Requested: PhaseLive, Refusal: r, Reason: reason,
			Platform: "X", err: err}
	}
	switch {
	case se.Status == http.StatusForbidden && !strings.Contains(se.Problems, "missing-scopes"):
		return refuse(RefusalNotEligible, "notEligible")
	case se.Status == http.StatusConflict && strings.Contains(strings.ToLower(se.Problems), "broadcast"):
		return refuse(RefusalSourceBusy, "sourceBusy")
	case se.Status == http.StatusConflict:
		return refuse(RefusalStreamInactive, "sourceNotReady")
	}
	return wrapped
}

// OpenBroadcast implements BroadcastCreator. It resolves the stored stream key to its source id and returns a pending
// handle. NOTHING is created on X: a broadcast made by Go Live is public at once, so it is made by PublishBroadcast,
// when the coordinator sees the encoder sending. Gated.
func (x *X) OpenBroadcast(ctx context.Context, accessToken, sourceKey string) (string, error) {
	if !x.livestream {
		return "", ErrXLivestreamOff
	}
	if !xStreamKeyPattern.MatchString(sourceKey) {
		// The message names the rule, not the value: the value is the stream key.
		return "", errors.New("the stream source's key does not have the shape X requires " +
			"(1 to 32 letters or digits); re-fetch the stream key from polyemesis")
	}
	id, err := x.sourceIDForKey(ctx, accessToken, sourceKey)
	if err != nil {
		return "", err
	}
	return xPendingHandle(id), nil
}

// sourceIDForKey finds the source whose stream key is key. The /2/sources routes identify a source by its id and the
// stored key is the credential, so each source's key is read until one matches. polyemesis's own source is tried first,
// which is the usual match and costs one extra read.
func (x *X) sourceIDForKey(ctx context.Context, accessToken, key string) (string, error) {
	srcs, err := x.listSources(ctx, accessToken)
	if err != nil {
		return "", err
	}
	sort.SliceStable(srcs, func(i, j int) bool {
		return srcs[i].Name == xSourceName && srcs[j].Name != xSourceName
	})
	for _, s := range srcs {
		k, err := x.sourceStreamKey(ctx, accessToken, s.ID)
		if err != nil {
			return "", err
		}
		if k == key {
			return s.ID, nil
		}
	}
	return "", errors.New("no stream source on this X account holds the stored stream key; " +
		"reconnect the account so polyemesis can fetch it again")
}

// PublishBroadcast takes a source live: POST /2/broadcasts with the source id, in one call, and it answers the real
// broadcast. broadcastID is the pending handle OpenBroadcast returned, or, for a destination recorded by an earlier
// version, the id of a NOT_STARTED broadcast made on the removed create route; the new routes cannot publish that one,
// so its source is read and taken live instead. A source that already has a live broadcast answers 200 with that
// broadcast, unchanged, and the request's title is ignored. Gated.
func (x *X) PublishBroadcast(ctx context.Context, accessToken, broadcastID string, p XPublish) (*XPublishResult, error) {
	if !x.livestream {
		return nil, ErrXLivestreamOff
	}
	if strings.TrimSpace(broadcastID) == "" {
		return nil, errors.New("no broadcast id given to publish")
	}
	body := map[string]any{}
	if title := strings.TrimSpace(p.Title); title != "" {
		body["title"] = title
	}
	if p.ChatOption != nil {
		if *p.ChatOption < 0 || *p.ChatOption > xChatOptionMax {
			return nil, fmt.Errorf("chat option must be 0 to %d, which X documents", xChatOptionMax)
		}
		body["chat_option"] = *p.ChatOption
	}
	if p.ShouldNotTweet {
		body["should_not_tweet"] = true
	}
	sourceID, pending := xPendingSource(broadcastID)
	if !pending {
		b, err := x.BroadcastByID(ctx, "", accessToken, broadcastID)
		if err != nil {
			return nil, err
		}
		sourceID = b.SourceID
		if !xSourceIDPattern.MatchString(sourceID) {
			return nil, fmt.Errorf("broadcast %s has no stream source X can take live", broadcastID)
		}
	}
	body["source_id"] = sourceID
	var out struct {
		Data   XBroadcast `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	status, err := x.doStatus(ctx, http.MethodPost, x.apiEndpoint()+"/2/broadcasts", accessToken, body, &out)
	if err != nil {
		return nil, classifyXGoLive(err, pending, broadcastID)
	}
	id := out.Data.ID
	if id == "" {
		id = out.Data.BroadcastID
	}
	if id == "" {
		// A 2xx can still carry X's errors. With no broadcast the go-live did nothing, so it is a failure in X's words.
		if len(out.Errors) > 0 {
			return nil, fmt.Errorf("X would not take the stream source live: %s", xProblems(out.Errors))
		}
		return nil, errors.New("X answered the go-live with no broadcast, so it cannot be shown to be live")
	}
	if st := out.Data.State; st != XStateRunning && st != XStatePrePublished {
		return nil, fmt.Errorf("X answered the go-live of broadcast %s with state %q, not %s", id, st, XStateRunning)
	}
	res := &XPublishResult{ID: id, State: out.Data.State, Existing: status == http.StatusOK}
	if len(out.Errors) > 0 {
		res.Warnings = append(res.Warnings, "X took the source live but reported: "+xProblems(out.Errors))
	}
	return res, nil
}

// --------------------------------------------------------------- lifecycle

// TransitionBroadcast maps the three phases onto X's two writes. X has no testing
// state, so asking for PhaseTesting is refused with that reason. PhaseLive takes
// the source live (POST /2/broadcasts) and the result carries the REAL broadcast
// id, which replaces the pending handle the caller passed. PhaseComplete ends it
// (PUT /2/broadcasts/{id}/state with END); a pending handle has nothing on X to
// end, so it is reported as redundant without a request. Gated.
//
// PROVISIONAL: the result shapes are from the docs; no live run has confirmed them.
func (x *X) TransitionBroadcast(ctx context.Context, accessToken, broadcastID string, to BroadcastPhase) (*TransitionResult, error) {
	if !x.livestream {
		return nil, ErrXLivestreamOff
	}
	if !to.Valid() {
		return nil, fmt.Errorf("%q is not a phase polyemesis knows how to ask X for", to)
	}
	if strings.TrimSpace(broadcastID) == "" {
		// Refused before any call: an empty id would address /2/broadcasts//state.
		return nil, errors.New("no broadcast id given to move")
	}
	res := &TransitionResult{BroadcastID: broadcastID, Requested: to}
	switch to {
	case PhaseTesting:
		return nil, errors.New("X has no testing state: a broadcast is created RUNNING when its source " +
			"is taken live, so there is no testing step to take")
	case PhaseLive:
		// The generic transition carries no title or chat option. The coordinator
		// uses PublishBroadcast directly when it has them.
		pub, err := x.PublishBroadcast(ctx, accessToken, broadcastID, XPublish{})
		if err != nil {
			return nil, err
		}
		res.BroadcastID, res.Status = pub.ID, pub.State
		res.Warnings = pub.Warnings
	case PhaseComplete:
		if _, pending := xPendingSource(broadcastID); pending {
			// No broadcast was ever made on X for this handle, so nothing is on air and nothing can be ended.
			res.Redundant = true
			return res, nil
		}
		var out struct {
			Data struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"data"`
			Errors []xProblem `json:"errors"`
		}
		endpoint := x.apiEndpoint() + "/2/broadcasts/" + url.PathEscape(broadcastID) + "/state"
		if err := x.do(ctx, http.MethodPut, endpoint, accessToken, map[string]string{"state": "END"}, &out); err != nil {
			return nil, fmt.Errorf("X would not end broadcast %s: %w", broadcastID, err)
		}
		// END answers {"data":{"id":...,"state":"ENDED"}}. A 2xx with errors and no state is a failed end, and a 2xx
		// with neither shows nothing ended: a disabled row recorded as ended would never be tried again.
		res.Status = out.Data.State
		switch {
		case res.Status == "" && len(out.Errors) > 0:
			return nil, fmt.Errorf("X would not end broadcast %s: %s", broadcastID, xProblems(out.Errors))
		case res.Status == "":
			return nil, fmt.Errorf("X answered the end of broadcast %s with no state, so it cannot be shown to have ended", broadcastID)
		case res.Status != XStateEnded && res.Status != XStateTimedOut && res.Status != XStateCanceled:
			return nil, fmt.Errorf("X answered the end of broadcast %s with state %s, which is not ended", broadcastID, res.Status)
		}
		if len(out.Errors) > 0 {
			res.Warnings = append(res.Warnings, "X ended the broadcast but reported: "+xProblems(out.Errors))
		}
	}
	return res, nil
}

// ---------------------------------------------------------------- metadata

// MetadataCaps reports no writable fields. X is NOT at parity with YouTube on
// metadata. The Livestream API takes the title and the chat option only on the
// PUBLISH transition, and it has no endpoint that changes them on a running
// broadcast; END rejects a title (the Livestream reference at docs.x.com/livestream-api,
// read 2026-10-31). Nothing here can change a live broadcast.
func (x *X) MetadataCaps() MetadataCaps {
	return MetadataCaps{Scope: "broadcast.write"}
}

// PushMetadata refuses every change, because X has no way to change a live
// broadcast's title or chat option (see MetadataCaps). It says so in terms the
// operator can act on, and it never sends a request.
func (x *X) PushMetadata(ctx context.Context, clientID, accessToken, accountRef string, m Metadata) (*MetadataResult, error) {
	return nil, errors.New("X sets the title only when a broadcast goes live: the publish " +
		"step is the one write that takes one, and no write changes a broadcast that is already " +
		"running. Set the title before you go live")
}

// ManualKeyReason is what the operator reads next to the stream-key field while
// the gate is closed.
func (x *X) ManualKeyReason() string {
	return "Paste the server URL and stream key from X's producer tooling. " +
		"Once X Livestream is switched on (POLYEMESIS_X_LIVESTREAM=1) and the account can go live, " +
		"polyemesis can create the stream source itself. Sign-in works without that."
}
