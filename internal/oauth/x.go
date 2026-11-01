package oauth

// X (Twitter) Livestream: the sign-in, the stream key, the broadcast lifecycle
// and the metadata that X's Livestream API (docs.x.com/livestream-api) exposes.
//
// SWITCHED OFF BY DEFAULT. X whitelists apps for the Livestream family
// (docs.x.com/livestream-api/getting-started, access via the Livestream access
// form). Until the owner's developer app is whitelisted every operation in this
// file that touches the Livestream family refuses WITHOUT making a request, and
// the copy the operator sees is "needs X Livestream access (app must be
// whitelisted by X)". The switch is the POLYEMESIS_X_LIVESTREAM environment
// variable ("1" or "true"), read once when the provider is built. Paste-the-key
// keeps working regardless.
//
// Everything below was read from X's served OpenAPI spec (api.x.com/2/openapi.json,
// version 2.173, 2026-10-31) and the Livestream guides. Live behaviour is NOT
// verified: no X app has been whitelisted, so every path here is PROVISIONAL until
// a whitelisted app runs it. The ones most likely to need a change are noted at
// the call sites.
//
// Scopes requested: broadcast.read and broadcast.write (the Livestream family),
// offline.access (a refresh token), users.read (GET /2/users/me, the only endpoint
// that returns a handle), and tweet.read (the broadcast.chat activity
// subscription, which the activity API gates on tweet.read for every event that is
// not a DM, like or mute).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// xAPIBase is the one production host: consent, tokens and data all live on
// api.x.com.
const xAPIBase = "https://api.x.com"

// xLivestreamEnv is the operator's switch for the Livestream family. Set it to
// 1 or true once X has whitelisted the app.
const xLivestreamEnv = "POLYEMESIS_X_LIVESTREAM"

// xGateCopy is the copy shown when the Livestream family is switched off. It is
// the owner's agreed wording and the tests pin it.
const xGateCopy = "needs X Livestream access (app must be whitelisted by X)"

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

// NewX builds the provider. The Livestream gate is read from the environment
// here, so every call to ProvidersWith sees the same switch.
func NewX(opts ...ProviderOption) *X {
	return &X{endpoints: newEndpoints(opts), livestream: xLivestreamFromEnv()}
}

// xLivestreamFromEnv reads the operator's switch. Anything other than 1 or
// true (case-insensitive) leaves the gate closed.
func xLivestreamFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(xLivestreamEnv))) {
	case "1", "true":
		return true
	}
	return false
}

// WithLivestreamAccess opens the gate on this instance. Tests and the environment
// switch use it; production sets the environment variable instead.
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
func (x *X) Scopes() []string {
	return []string{
		"broadcast.read",
		"broadcast.write",
		"offline.access",
		"users.read",
		"tweet.read",
	}
}

// ScopeVersion is 1. X was never registered before this, so no account holds an
// earlier grant and nothing needs a reconnect prompt. Bump it by hand when Scopes
// changes; the scope ledger in testdata/scope-versions.json must match.
func (x *X) ScopeVersion() int { return 1 }

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
	return x.authEndpoint() + "/2/oauth2/authorize?" + q.Encode()
}

// Exchange trades the code for tokens. The client pair travels in the body, the
// shape every provider here uses. X's spec names no client-authentication method
// for the token endpoint, so if a live exchange answers invalid_client with a
// valid pair, switch to HTTP Basic. That is PROVISIONAL until a live exchange.
func (x *X) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error) {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	return postForm(ctx, x.tokenEndpoint(), form, nil)
}

func (x *X) tokenEndpoint() string { return x.authEndpoint() + "/2/oauth2/token" }

// Refresh spends the refresh token that offline.access issues. A response that
// omits a rotated token keeps the old one, so the account does not disconnect an
// hour later.
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

// xDo performs one authenticated JSON call. payload nil sends no body. out nil
// discards the response.
func xDo(ctx context.Context, method, endpoint, accessToken string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &xStatusError{Status: resp.StatusCode, Problems: xProblemText(raw)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
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

// me returns the authenticated user. It is needed by every Livestream path that
// has a /2/users/{id}/... address and by the account read.
func (x *X) me(ctx context.Context, accessToken string) (xUser, error) {
	var out struct {
		Data   xUser      `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	endpoint := x.apiEndpoint() + "/2/users/me?" + url.Values{
		"user.fields": {"username,name"},
	}.Encode()
	if err := xDo(ctx, http.MethodGet, endpoint, accessToken, nil, &out); err != nil {
		return xUser{}, fmt.Errorf("X would not say which account this token belongs to: %w", err)
	}
	if out.Data.ID == "" {
		if len(out.Errors) > 0 {
			return xUser{}, fmt.Errorf("X would not say which account this token belongs to: %s", xProblems(out.Errors))
		}
		return xUser{}, errors.New("X returned no user id for this token")
	}
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
	return "Livestream " + xGateCopy + ". X's Livestream API is open only to apps " +
		"X has whitelisted, and it also needs an account that X allows to broadcast. " +
		"Signing in works without it. Until polyemesis's app is whitelisted, paste " +
		"the stream key from X's producer tooling."
}

// xGateError is what the Livestream family returns while the switch is off. Its
// text is the gate copy the operator reads, and it names the app's whitelist, not
// the operator's account. It matches the platform-wide ErrNotEntitled, which the
// connect warning keys on, and ErrXLivestreamOff, which names the cause.
type xGateError struct{}

func (xGateError) Error() string {
	return "Livestream " + xGateCopy + ". Sign-in works without it; paste the stream key from " +
		"X's producer tooling until the app is whitelisted."
}

func (xGateError) Is(target error) bool {
	return target == ErrNotEntitled || target == ErrXLivestreamOff
}

// CheckEntitlement probes the Livestream family once with the operator's fresh
// token. It lists the account's stream sources, a read that changes nothing. A
// 403 or 412 is the gate. Any other failure is reported as a failed probe, never
// as a gate.
//
// While the gate is closed it refuses WITHOUT a request, /2/users/me included,
// and the refusal is xGateError, which carries the gate copy.
func (x *X) CheckEntitlement(ctx context.Context, clientID, accessToken string) error {
	if !x.livestream {
		return xGateError{}
	}
	u, err := x.me(ctx, accessToken)
	if err != nil {
		return err
	}
	var out struct {
		Data struct {
			Sources []xSource `json:"sources"`
		} `json:"data"`
	}
	err = xDo(ctx, http.MethodGet, x.sourcesEndpoint(u.ID), accessToken, nil, &out)
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

// xSource is a stream source as the list and create calls return it.
type xSource struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	RTMPURL       string `json:"rtmp_url"`
	RTMPSURL      string `json:"rtmps_url"`
	RTMPStreamKey string `json:"rtmp_stream_key"`
	IsActive      bool   `json:"is_stream_active"`
}

// key returns the stream key. X documents the source id as the RTMP stream key
// too, so the id is the fallback.
func (s xSource) key() string {
	if s.RTMPStreamKey != "" {
		return s.RTMPStreamKey
	}
	return s.ID
}

// ingestURL prefers RTMPS, which X documents as the port-443 endpoint. The host
// is whatever X returned; nothing here constructs one.
func (s xSource) ingestURL() string {
	if s.RTMPSURL != "" {
		return s.RTMPSURL
	}
	return s.RTMPURL
}

func (x *X) sourcesEndpoint(userID string) string {
	return x.apiEndpoint() + "/2/users/" + url.PathEscape(userID) + "/sources"
}

// StreamKeys lists every source key on the account, read-only. It backs the
// cross-check that compares a stored key with what X holds.
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
		if k := s.key(); k != "" {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (x *X) listSources(ctx context.Context, accessToken string) ([]xSource, error) {
	u, err := x.me(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Sources []xSource `json:"sources"`
		} `json:"data"`
	}
	if err := xDo(ctx, http.MethodGet, x.sourcesEndpoint(u.ID), accessToken, nil, &out); err != nil {
		return nil, fmt.Errorf("X would not list stream sources: %w", err)
	}
	return out.Data.Sources, nil
}

// Ingest returns the polyemesis source's URL and key, minting the source the
// first time. While the gate is closed it refuses without a request and sends the
// operator to the paste field, with ErrNoStreamKeyAPI so callers route it the
// same way they route Kick and Vimeo.
func (x *X) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	if !x.livestream {
		return nil, fmt.Errorf("%w. X Livestream %s", ErrNoStreamKeyAPI, xGateCopy)
	}
	u, err := x.me(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	srcs, err := x.listSources(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	for _, s := range srcs {
		if s.Name == xSourceName && s.key() != "" && s.ingestURL() != "" {
			return &Ingest{URL: s.ingestURL(), Key: s.key()}, nil
		}
	}
	// PROVISIONAL: the create body is {"name": ...}. The spec also accepts a
	// region, which is left to X's default.
	var out struct {
		Data struct {
			Source xSource `json:"source"`
		} `json:"data"`
	}
	if err := xDo(ctx, http.MethodPost, x.sourcesEndpoint(u.ID), accessToken,
		map[string]string{"name": xSourceName}, &out); err != nil {
		return nil, fmt.Errorf("X would not create a stream source: %w", err)
	}
	s := out.Data.Source
	if s.key() == "" || s.ingestURL() == "" {
		return nil, errors.New("X created the stream source but returned no ingest address or key")
	}
	return &Ingest{URL: s.ingestURL(), Key: s.key()}, nil
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

// xProblem is X's typed error object.
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
	if err := xDo(ctx, http.MethodGet, endpoint, accessToken, nil, &out); err != nil {
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
	if err := xDo(ctx, http.MethodGet, endpoint, accessToken, nil, &out); err != nil {
		return nil, fmt.Errorf("X would not read broadcast %s: %w", id, err)
	}
	if out.Data.ID == "" && out.Data.BroadcastID == "" && len(out.Errors) > 0 {
		return nil, fmt.Errorf("X would not read broadcast %s: %s", id, xProblems(out.Errors))
	}
	return &out.Data, nil
}

// BroadcastState reads the broadcast for the lifecycle. X publishes no
// stream-liveness read, so StreamActive and MonitorStream stay nil, meaning
// unknown, rather than false.
//
// StreamActive is read from the bound source's is_stream_active, which is the
// only receive-side signal X exposes. It is nil when the source is not found,
// so an unknown answer is never reported as "no bytes". MonitorStream has no X
// equivalent and stays nil.
func (x *X) BroadcastState(ctx context.Context, accessToken, broadcastID string) (*BroadcastLifecycleState, error) {
	b, err := x.BroadcastByID(ctx, "", accessToken, broadcastID)
	if err != nil {
		return nil, err
	}
	st := &BroadcastLifecycleState{
		BroadcastID:   broadcastID,
		Title:         b.Title,
		Status:        b.State,
		BoundStreamID: b.SourceID,
	}
	if b.SourceID != "" {
		srcs, err := x.listSources(ctx, accessToken)
		if err != nil {
			return nil, err
		}
		for _, s := range srcs {
			if s.key() == b.SourceID {
				active := s.IsActive
				st.StreamActive = &active
				if active {
					st.StreamStatus = "active"
				} else {
					st.StreamStatus = "inactive"
				}
				break
			}
		}
	}
	return st, nil
}

// --------------------------------------------------------- create and publish

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

// xSourceKeyPattern is the source_id shape the create spec requires. source_id
// is the source's rtmp_stream_key, which is a secret, so a value that does not
// match is refused without being echoed back.
var xSourceKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9]{1,32}$`)

// XPublish is what the PUBLISH transition carries besides the state.
type XPublish struct {
	// Title is the broadcast title. Empty means the title is left out and X keeps
	// whatever it has. X documents no character limit, so none is enforced here.
	Title string
	// ChatOption is nil when unset, which leaves the field out so X applies its
	// default. Otherwise it must be 0 to 6.
	ChatOption *int
}

// XPublishResult is what PUBLISH answered.
type XPublishResult struct {
	ID    string
	State string
	// Warnings carries tweet_error, the reason the announcing Post failed. The
	// broadcast is still live when this is set.
	Warnings []string
}

// XBroadcastCreated is the broadcast X returned from the create call.
type XBroadcastCreated struct {
	ID       string
	State    string
	ShareURL string
}

// CreateBroadcast opens a broadcast on the source whose rtmp_stream_key is
// sourceKey. The broadcast starts NOT_STARTED, which is not public until
// PublishBroadcast. X returns a broadcast that is already running on that source
// instead of making a second one, and the caller sees it as RUNNING. Gated.
func (x *X) CreateBroadcast(ctx context.Context, clientID, accessToken, sourceKey string) (*XBroadcastCreated, error) {
	if !x.livestream {
		return nil, ErrXLivestreamOff
	}
	if !xSourceKeyPattern.MatchString(sourceKey) {
		// The message names the rule, not the value: the value is the stream key.
		return nil, errors.New("the stream source's key does not have the shape X requires " +
			"(1 to 32 letters or digits); re-fetch the stream key from polyemesis")
	}
	u, err := x.me(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Broadcast XBroadcast `json:"broadcast"`
			ShareURL  string     `json:"share_url"`
		} `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	endpoint := x.apiEndpoint() + "/2/users/" + url.PathEscape(u.ID) + "/broadcasts"
	if err := xDo(ctx, http.MethodPost, endpoint, accessToken,
		map[string]string{"source_id": sourceKey}, &out); err != nil {
		return nil, fmt.Errorf("X would not create a broadcast on the stream source: %w", err)
	}
	if out.Data.Broadcast.ID == "" && out.Data.Broadcast.BroadcastID == "" {
		if len(out.Errors) > 0 {
			return nil, fmt.Errorf("X would not create a broadcast: %s", xProblems(out.Errors))
		}
		return nil, errors.New("X created no broadcast")
	}
	id := out.Data.Broadcast.ID
	if id == "" {
		id = out.Data.Broadcast.BroadcastID
	}
	return &XBroadcastCreated{ID: id, State: out.Data.Broadcast.State, ShareURL: out.Data.ShareURL}, nil
}

// PublishBroadcast takes a NOT_STARTED broadcast to RUNNING. It is the only
// write that can carry a title or a chat option, and it goes to the user-scoped
// route, because the newer /2/broadcasts/{id}/state route accepts END only.
// Gated.
//
// PROVISIONAL: the chat_option value is validated locally, but no live PUBLISH
// has run against an account, so the response shape is from the spec.
func (x *X) PublishBroadcast(ctx context.Context, accessToken, broadcastID string, p XPublish) (*XPublishResult, error) {
	if !x.livestream {
		return nil, ErrXLivestreamOff
	}
	if strings.TrimSpace(broadcastID) == "" {
		return nil, errors.New("no broadcast id given to publish")
	}
	body := map[string]any{"state": "PUBLISH"}
	if title := strings.TrimSpace(p.Title); title != "" {
		body["title"] = title
	}
	if p.ChatOption != nil {
		if *p.ChatOption < 0 || *p.ChatOption > xChatOptionMax {
			return nil, fmt.Errorf("chat option must be 0 to %d, which X documents", xChatOptionMax)
		}
		body["chat_option"] = *p.ChatOption
	}
	u, err := x.me(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Broadcast struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"broadcast"`
			TweetError string `json:"tweet_error"`
		} `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	endpoint := x.apiEndpoint() + "/2/users/" + url.PathEscape(u.ID) + "/broadcasts/" +
		url.PathEscape(broadcastID) + "/state"
	if err := xDo(ctx, http.MethodPut, endpoint, accessToken, body, &out); err != nil {
		return nil, fmt.Errorf("X would not publish broadcast %s: %w", broadcastID, err)
	}
	res := &XPublishResult{ID: out.Data.Broadcast.ID, State: out.Data.Broadcast.State}
	if res.ID == "" {
		res.ID = broadcastID
	}
	if strings.TrimSpace(out.Data.TweetError) != "" {
		res.Warnings = []string{"the broadcast is live, but X did not post its announcement: " +
			out.Data.TweetError}
	}
	return res, nil
}

// --------------------------------------------------------------- lifecycle

// TransitionBroadcast maps the three phases onto X's two writes. X has no testing
// state: a broadcast goes from NOT_STARTED straight to RUNNING, so asking for
// PhaseTesting is refused with that reason. PhaseLive publishes, PhaseComplete
// ends. Gated.
//
// PROVISIONAL: nothing has yet been published on a live account.
func (x *X) TransitionBroadcast(ctx context.Context, accessToken, broadcastID string, to BroadcastPhase) (*TransitionResult, error) {
	if !x.livestream {
		return nil, ErrXLivestreamOff
	}
	if !to.Valid() {
		return nil, fmt.Errorf("%q is not a phase polyemesis knows how to ask X for", to)
	}
	res := &TransitionResult{BroadcastID: broadcastID, Requested: to}
	var out struct {
		Data struct {
			State     string `json:"state"`
			Broadcast struct {
				State string `json:"state"`
			} `json:"broadcast"`
		} `json:"data"`
		Errors []xProblem `json:"errors"`
	}
	switch to {
	case PhaseTesting:
		return nil, errors.New("X has no testing state: a broadcast goes from NOT_STARTED " +
			"straight to RUNNING when it is published, so there is no testing step to take")
	case PhaseLive:
		// The generic transition carries no title or chat option. The coordinator
		// uses PublishBroadcast directly when it has them.
		pub, err := x.PublishBroadcast(ctx, accessToken, broadcastID, XPublish{})
		if err != nil {
			return nil, err
		}
		res.Status = pub.State
		res.Warnings = pub.Warnings
	case PhaseComplete:
		endpoint := x.apiEndpoint() + "/2/broadcasts/" + url.PathEscape(broadcastID) + "/state"
		if err := xDo(ctx, http.MethodPut, endpoint, accessToken, map[string]string{"state": "END"}, &out); err != nil {
			return nil, fmt.Errorf("X would not end broadcast %s: %w", broadcastID, err)
		}
		res.Status = out.Data.State
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
		"Once X has whitelisted polyemesis's app, polyemesis can create the stream " +
		"source itself. Sign-in works without that."
}
