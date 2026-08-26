package oauth

// Trovo's open platform, which is the most completely documented API of any
// platform polyemesis has integrated -- and the one with the most ways to write
// a client that compiles, passes its own tests and fails on first contact.
//
// FOUR OF THEM ARE ENCODED HERE RATHER THAN LEFT TO BE REDISCOVERED. Each is a
// place where the obvious Go is wrong in a way that looks right:
//
//  1. expires_in IS A QUOTED STRING in every token response Trovo publishes
//     ("expires_in": "14400"). An `int` field does not decode it, and a test
//     whose fake server answers with a NUMBER passes while production fails at
//     the first exchange. See trovoSeconds, and note that the fixtures in
//     trovo_test.go are copied from the documented samples byte for byte for
//     exactly this reason.
//  2. The authorization header is `OAuth <token>`, not `Bearer <token>`. A
//     Bearer produces a 401, which reads as a bad token rather than a bad
//     header -- so the operator is sent to reconnect an account that was fine.
//     This is why nothing here calls getJSON/postJSON/requestJSON, all three of
//     which hardcode Bearer: trovoRequest is the only transport in this file.
//  3. The client id travels in a `Client-ID` HEADER on every call, not as a
//     query parameter. Omitting it fails with Trovo's own invalidHeader error.
//  4. A refresh token holds at most FIFTY access tokens at once, and Trovo
//     refuses the refresh once that is exceeded. Refresh() is therefore called
//     on expiry (internal/api's tokenFor, plus the RefreshLoop's expiry check)
//     and never on a timer; there is nothing to add here beyond not adding a
//     timer, which is why this note is a comment rather than code.
//
// Sources, all read 2026-08-26 from https://developer.trovo.live/docs/APIs.html
// and recorded in docs/evidence/vimeo-trovo-oauth-2026-08-26.md: §3.2 for the
// authorize URL and the exchange, §4.3 for refresh, §5.6 for the channel and
// its stream key, §5.7 for the channel update, §5.2 for the category search.
//
// THERE IS NO BROADCAST START OR END ENDPOINT. The reference was read end to
// end and the lifecycle is absent, exactly as it is on Twitch and Kick: the
// stream is the trigger, and liveness can only be observed. That is a fact
// about Trovo rather than a gap in this file, and BroadcastLifecycler is
// deliberately not implemented.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Trovo's production hosts. Two of them, because the consent page and the API
// are different services: constants, reached only through authBase/apiBase, so
// a test redirects an instance rather than the package. See endpoints.go.
const (
	trovoLoginBase = "https://open.trovo.live"
	trovoAPIBase   = "https://open-api.trovo.live"
)

// The paths, named so a failure message and a stub handler can share them.
const (
	trovoExchangePath = "/openplatform/exchangetoken"
	trovoRefreshPath  = "/openplatform/refreshtoken"
	trovoChannelPath  = "/openplatform/channel"
	trovoUpdatePath   = "/openplatform/channels/update"
	trovoSearchPath   = "/openplatform/searchcategory"
)

// Trovo implements Trovo's authorization-code flow plus the parts of its open
// platform polyemesis uses today: channel identity, the stream key, the
// title/category push, and the live viewer count.
type Trovo struct {
	endpoints
}

func (t *Trovo) loginEndpoint() string { return t.authBase(trovoLoginBase) }
func (t *Trovo) apiEndpoint() string   { return t.apiBase(trovoAPIBase) }

func (t *Trovo) Platform() db.Platform { return db.PlatformTrovo }

// Scopes is deliberately the SMALLEST set that covers what this build does, and
// that is a choice against Kick's, which settles the whole list in one go so a
// later feature does not force every operator to reconnect.
//
// Both arguments are real and they point opposite ways:
//
//	FOR asking now: adding a scope later does not upgrade a token somebody
//	already holds, so shipping chat afterwards makes every connected Trovo
//	account reconnect once. That is the cost Kick's Scopes() comment weighs.
//
//	AGAINST: manage_messages is, in Trovo's own words, "Perform chat commands
//	and delete chat messages" -- the power to ban and to mod. Asking a
//	streamer's consent screen for it while nothing in polyemesis moderates
//	Trovo is asking for authority we do not use, and a consent screen that
//	over-asks is the one an operator declines.
//
// The second wins because the first is already handled: ScopeVersion plus
// AccountNeedsReconnect turn "this account predates the feature" into a prompt
// in the account list rather than a 401 mid-broadcast. That mechanism exists so
// that scopes can be requested honestly and late.
//
// So when chat read/send or moderation lands here, the scopes to add are
// chat_send_self, send_to_my_channel and manage_messages, and the SAME COMMIT
// must bump ScopeVersion and append a row to testdata/scope-versions.json.
func (t *Trovo) Scopes() []string {
	return []string{
		"channel_details_self", // channel identity, live state, and the stream key
		"channel_update_self",  // the title and category push
	}
}

// ScopeVersion 1: the first version this platform has ever had. See the
// interface comment in oauth.go -- it is bumped BY HAND, and the ledger in
// testdata/scope-versions.json records what each version meant.
func (t *Trovo) ScopeVersion() int { return 1 }

// PKCE is false, and stays false until Trovo documents it. The reference
// describes exactly two grants -- implicit and authorization code with a
// client_secret -- and mentions RFC 7636 nowhere. Sending a code_challenge to
// an authorization server that validates its query string strictly locks every
// operator out of sign-in, which is the failure Provider.PKCE exists to avoid.
func (t *Trovo) PKCE() bool { return false }

// AuthURL builds the consent URL.
//
// THE SCOPE SEPARATOR IS A LITERAL '+' AND IT CANNOT GO THROUGH url.Values.
// Trovo documents "'+' separated list of scopes" and its own example shows
// scope=channel_details_self+channel_update_self+user_details_self unencoded.
// url.Values.Encode percent-encodes a '+' inside a value to %2B, so putting the
// joined string through it sends a byte sequence that differs from every
// documented example -- and whether Trovo's parser treats %2B, a literal '+'
// and a space as the same thing is not something the reference answers. The
// documented spelling is the one that is known to work, so scope is appended
// verbatim; every other parameter still goes through Encode, because
// redirect_uri and state genuinely need escaping.
//
// challenge is ignored. It is not merely unused: a provider that reports
// PKCE() false must not put the parameter in the URL even when a caller hands
// it one, and pkce_test.go asserts exactly that for every provider.
func (t *Trovo) AuthURL(clientID, redirectURI, state, challenge string) string {
	q := make([]string, 0, 4)
	for _, kv := range [][2]string{
		{"client_id", clientID},
		{"response_type", "code"},
		{"redirect_uri", redirectURI},
		{"state", state},
	} {
		q = append(q, kv[0]+"="+url.QueryEscape(kv[1]))
	}
	return t.loginEndpoint() + "/page/login.html?" + strings.Join(q, "&") +
		"&scope=" + strings.Join(t.Scopes(), "+")
}

// Exchange trades the authorization code for tokens.
//
// A JSON BODY, NOT A FORM. Trovo's exchange takes application/json with the
// client id in a header, which is why postForm -- the helper every other
// provider in this package uses -- cannot be reused. verifier is ignored
// because PKCE is not documented here; see PKCE above.
func (t *Trovo) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error) {
	return t.token(ctx, trovoExchangePath, clientID, map[string]string{
		"client_secret": clientSecret,
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  redirectURI,
	})
}

// Refresh exchanges the refresh token for a new access token.
//
// Called ON EXPIRY and never on a timer -- see trap 4 at the top of this file.
// Trovo rotates the refresh token and keeps the old one working for its full 30
// days, so a response that omits one is not a disconnect: the stored token is
// carried forward, the same way Kick's is.
func (t *Trovo) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	tok, err := t.token(ctx, trovoRefreshPath, clientID, map[string]string{
		"client_secret": clientSecret,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

// trovoSeconds is expires_in, WHICH TROVO SENDS AS A QUOTED STRING.
//
// Both of Trovo's documented token samples -- §3.2's exchange and §4.3's
// refresh -- read "expires_in": "14400". An int field fails to unmarshal that,
// and json.Unmarshal fails the WHOLE object, so the access token is lost too
// and the operator sees "decode token response" from a response that was
// perfectly good.
//
// A bare number is accepted as well, and that is not defensive padding: it is
// what keeps this type honest about the one thing it asserts. The string form
// is what the platform documents and what trovo_test.go's fixtures copy byte
// for byte; the number form is what the SHARED provider tests in this package
// serve to every provider at once, and a type that refused it would have made
// this file's correctness depend on editing those.
type trovoSeconds int

func (s *trovoSeconds) UnmarshalJSON(raw []byte) error {
	txt := strings.TrimSpace(string(raw))
	if txt == "" || txt == "null" {
		return nil
	}
	txt = strings.Trim(txt, `"`)
	if txt == "" {
		return nil
	}
	n, err := strconv.Atoi(txt)
	if err != nil {
		return fmt.Errorf("expires_in %q is neither a number of seconds nor a quoted one", txt)
	}
	*s = trovoSeconds(n)
	return nil
}

// token performs one of the two token calls and decodes Trovo's response.
//
// Kept apart from postForm rather than folded into it: postForm speaks
// form-encoded bodies and an int expires_in, and widening it to cover Trovo
// would put a Trovo-shaped branch on the path four other platforms take.
func (t *Trovo) token(ctx context.Context, path, clientID string, body map[string]string) (*Token, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.apiEndpoint()+path, strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	// Trap 3: the client id is a header on every Trovo call, including this one.
	req.Header.Set("client-id", clientID)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &tokenStatusError{code: resp.StatusCode, body: snippet(payload)}
	}

	var out struct {
		AccessToken  string       `json:"access_token"`
		RefreshToken string       `json:"refresh_token"`
		ExpiresIn    trovoSeconds `json:"expires_in"`
		// Trovo reports an application-level failure in its own envelope --
		// {"status":11706,"error":"...","message":"..."} is the documented
		// rate-limit body -- and the reference does not promise that always
		// arrives with a non-2xx status. Decoded so a 200-shaped refusal is
		// reported as one rather than as "no access_token", which names the
		// wrong cause.
		Status  int    `json:"status"`
		Err     string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if out.AccessToken == "" {
		if out.Message != "" || out.Err != "" {
			return nil, fmt.Errorf("Trovo refused the token request (%d): %s",
				out.Status, firstNonEmpty(out.Message, out.Err))
		}
		return nil, fmt.Errorf("token response contained no access_token")
	}

	// Scopes are left empty because Trovo's token responses carry no scope
	// field -- the granted list is readable only from GET /openplatform/validate,
	// which is a whole extra round trip on the sign-in path. scopever.go treats
	// an empty granted string as "no verdict" rather than "everything is
	// missing" for exactly this case, and ScopeVersion is what actually decides
	// whether an account needs reconnecting.
	tok := &Token{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken}
	if out.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// ------------------------------------------------------------------ transport

// trovoRequest is the ONLY transport in this file, and that is the point.
//
// getJSON, postJSON and requestJSON all set `Authorization: Bearer`, which
// Trovo answers with a 401 that is indistinguishable from an expired token --
// so a call routed through one of them sends the operator to reconnect a
// perfectly good account. Trap 2. Every Trovo call goes through here, and
// TestEveryTrovoCallSendsTheOAuthSchemeAndTheClientIDHeader drives them all to
// prove it.
//
// accessToken may be empty: Trovo's category search and its channel-info reads
// need only the client id, and sending an empty Authorization header would be a
// different request from sending none.
//
// A non-2xx becomes a *statusError so scopeAdvice can turn a 401/403 on the
// metadata write into the reconnect instruction that actually fixes it.
func trovoRequest(ctx context.Context, method, endpoint, clientID, accessToken string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Client-ID", clientID)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "OAuth "+accessToken)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &statusError{Status: resp.StatusCode, URL: endpoint, Body: snippet(raw), full: string(raw)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// --------------------------------------------------------------- the channel

// trovoChannel is GET /openplatform/channel, §5.6.
//
// THE TYPES ARE THE DOCUMENTED SAMPLE'S, NOT THE ONES THE FIELD NAMES SUGGEST.
// uid, channel_id and created_at arrive as quoted strings while current_viewers,
// followers and subscriber_num arrive as bare numbers, in the same object. A
// struct that guesses uniformly fails to decode the whole response, which loses
// the stream key along with the field that was mistyped.
type trovoChannel struct {
	ChannelID    string `json:"channel_id"`
	Username     string `json:"username"`
	IsLive       bool   `json:"is_live"`
	CategoryID   string `json:"category_id"`
	CategoryName string `json:"category_name"`
	LiveTitle    string `json:"live_title"`
	LanguageCode string `json:"language_code"`
	// StreamKey is present only when the token carries channel_details_self;
	// it is the whole reason this endpoint is separate from §5.5's public one.
	StreamKey string `json:"stream_key"`
	// CurrentViewers is the live audience. Trovo documents it as "Number of
	// current viewers" with no opt-out clause, unlike Kick -- so on a live
	// channel it is a real number and Stats reports it as one.
	CurrentViewers int `json:"current_viewers"`
}

// channel reads the token's own channel. There is no id to pass: Trovo scopes
// this endpoint to whoever the access token belongs to.
func (t *Trovo) channel(ctx context.Context, clientID, accessToken string) (*trovoChannel, error) {
	var out trovoChannel
	if err := trovoRequest(ctx, http.MethodGet, t.apiEndpoint()+trovoChannelPath,
		clientID, accessToken, nil, &out); err != nil {
		return nil, err
	}
	if out.ChannelID == "" {
		return nil, fmt.Errorf("Trovo returned no channel for this token; make sure the app " +
			"requested the channel_details_self scope")
	}
	return &out, nil
}

func (t *Trovo) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	c, err := t.channel(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}
	return &Account{Name: firstNonEmpty(c.Username, c.ChannelID), Ref: c.ChannelID}, nil
}

// Ingest returns the stream key AND NO URL, which is the honest answer rather
// than an incomplete one.
//
// Trovo publishes the key on the channel resource (§5.6) and publishes the
// ingest HOSTNAME nowhere at all: it varies by region, is shown only in the
// creator dashboard, and the destination preset in internal/db/platforms.go has
// said so since before any of this existed. Inventing a host here is the
// mistake kick.go's Ingest spends a paragraph warning about -- a stale constant
// publishes to nowhere and looks like a polyemesis bug.
//
// An empty URL is therefore a supported result and not a failure. The caller
// that stores it -- internal/api's handleRefreshKey -- keeps whatever URL the
// destination already had rather than blanking it, and says what to do when
// there is none.
func (t *Trovo) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	c, err := t.channel(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}
	if c.StreamKey == "" {
		// Almost always a token minted before channel_details_self was
		// requested. Granting a scope never upgrades a token already issued, so
		// the only fix is a reconnect -- and saying that is the whole value of
		// this branch, because the symptom otherwise looks like Trovo being
		// broken.
		return nil, fmt.Errorf("Trovo returned no stream key for this account. The key rides on " +
			"the channel resource behind the channel_details_self scope, and granting a scope " +
			"never upgrades a token that has already been issued — disconnect the account and " +
			"connect it again")
	}
	return &Ingest{Key: c.StreamKey}, nil
}

// ------------------------------------------------------------------- metadata

func (t *Trovo) MetadataCaps() MetadataCaps {
	return MetadataCaps{
		// No description and no tags: §5.7 accepts exactly four writable fields
		// -- live_title, category, language_code and audi_type -- and neither a
		// description nor a tag list is among them. Advertising one would put a
		// control in the composer that reports nothing.
		Fields:        []MetadataField{FieldTitle, FieldCategory},
		CategoryLabel: "Category",
		CategoryHint:  "A Trovo game category, e.g. Just Chatting, Genshin Impact. A numeric category id also works.",
		// TitleMax is left at zero: Trovo publishes no title length anywhere in
		// §5.7, and a limit invented here would truncate a title Trovo would
		// have accepted.
		Scope: "channel_update_self",
	}
}

// TrovoCategory is one entry in Trovo's category directory (§5.2).
type TrovoCategory struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
}

// SearchCategories looks categories up by name. Trovo's channel update takes a
// category id and nothing else, so without this the operator has to find a
// number by hand -- which in practice means never setting a category.
//
// The search needs the client id and no access token, which is why it is safe
// to run before a metadata write rather than being another thing a scope can
// refuse.
func (t *Trovo) SearchCategories(ctx context.Context, clientID, accessToken, query string) ([]TrovoCategory, error) {
	var out struct {
		CategoryInfo []TrovoCategory `json:"category_info"`
	}
	err := trovoRequest(ctx, http.MethodPost, t.apiEndpoint()+trovoSearchPath,
		clientID, accessToken, map[string]any{"query": query, "limit": 20}, &out)
	if err != nil {
		return nil, err
	}
	return out.CategoryInfo, nil
}

// categoryID turns what the operator typed into the id Trovo wants.
//
// A string of digits is taken at its word rather than searched for, exactly as
// on Kick: an operator who already knows the id -- or whose category is too new
// for the directory to match -- can still set one instead of being refused by a
// lookup that is only ever advisory.
func (t *Trovo) categoryID(ctx context.Context, clientID, accessToken, name string) (id, resolved string, err error) {
	name = strings.TrimSpace(name)
	if _, convErr := strconv.Atoi(name); convErr == nil && name != "" {
		return name, "", nil
	}

	found, err := t.SearchCategories(ctx, clientID, accessToken, name)
	if err != nil {
		return "", "", err
	}
	want := normaliseCategory(name)
	for _, c := range found {
		if normaliseCategory(c.Name) == want || normaliseCategory(c.ShortName) == want {
			return c.ID, c.Name, nil
		}
	}
	if len(found) > 0 {
		names := make([]string, 0, len(found))
		for _, c := range found {
			names = append(names, c.Name)
		}
		if len(names) > 5 {
			names = names[:5]
		}
		return "", "", fmt.Errorf("Trovo has no category called %q. Did you mean: %s?",
			name, strings.Join(names, ", "))
	}
	return "", "", fmt.Errorf("Trovo has no category matching %q", name)
}

// TrovoChannelUpdate is the writable part of a Trovo channel. Zero fields are
// omitted, so a caller that only wants a title cannot blank the category.
type TrovoChannelUpdate struct {
	LiveTitle  string
	CategoryID string
}

// Empty reports whether there is nothing to write.
func (u TrovoChannelUpdate) Empty() bool { return u.LiveTitle == "" && u.CategoryID == "" }

// UpdateChannel writes the channel's live metadata.
//
// TWO SHAPES HERE ARE NOT WHAT §5.7's PARAMETER TABLE SAYS, and both produce a
// request that is accepted and changes nothing -- Trovo answers a successful
// update with {"empty":""} whatever it did or did not apply, so neither shows
// up as an error:
//
//	channel_id IS A JSON NUMBER. The table types it `int` and the request sample
//	sends {"channel_id":100000031,...} unquoted -- while §5.6 returns the very
//	same id as the STRING "100000021". Carrying the read value straight into the
//	write sends a string where an int is documented.
//
//	THE CATEGORY FIELD IS SPELLED category_id ON THE WIRE. The parameter table
//	names the parameter `category`, and then gives its own example as
//	(e.g."category_id":"10023"); §5.7's request SAMPLE sends "category_id", and
//	§5.6 reads the field back as category_id. Two of the three mentions and the
//	read side agree, so category_id is what is sent here. If a Trovo category
//	push is ever observed to do nothing, this is the first line to re-read.
func (t *Trovo) UpdateChannel(ctx context.Context, clientID, accessToken, channelID string, u TrovoChannelUpdate) error {
	if u.Empty() {
		return nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(channelID), 10, 64)
	if err != nil {
		return fmt.Errorf("Trovo channel id %q is not a number, so there is nothing to address "+
			"this update to; reconnect the account", channelID)
	}
	body := map[string]any{"channel_id": id}
	if u.LiveTitle != "" {
		body["live_title"] = u.LiveTitle
	}
	if u.CategoryID != "" {
		body["category_id"] = u.CategoryID
	}
	return trovoRequest(ctx, http.MethodPost, t.apiEndpoint()+trovoUpdatePath,
		clientID, accessToken, body, nil)
}

// PushMetadata writes the title and category.
//
// accountRef is the channel id recorded when the account was connected, and it
// is USED here rather than ignored -- Trovo's update addresses a channel by id,
// so this is the platform that would otherwise cost a second round trip on the
// path an operator runs seconds before air. An empty ref falls back to reading
// the channel, so an account stored before this existed still works.
func (t *Trovo) PushMetadata(ctx context.Context, clientID, accessToken, accountRef string, m Metadata) (*MetadataResult, error) {
	m = m.Trimmed()
	res := &MetadataResult{}

	if m.Description != "" {
		res.Skipped = append(res.Skipped, FieldDescription)
		res.Warnings = append(res.Warnings,
			"Trovo's channel update takes no description, so it was left out")
	}
	if len(m.Tags) > 0 {
		res.Skipped = append(res.Skipped, FieldTags)
		res.Warnings = append(res.Warnings, "Trovo has no tags on a channel")
	}

	upd := TrovoChannelUpdate{LiveTitle: m.Title}

	// Resolved before the write so a category we cannot find costs a warning
	// rather than the title change.
	var categoryName string
	if m.Category != "" {
		id, name, err := t.categoryID(ctx, clientID, accessToken, m.Category)
		if err != nil {
			res.Skipped = append(res.Skipped, FieldCategory)
			res.Warnings = append(res.Warnings, err.Error())
		} else {
			upd.CategoryID = id
			categoryName = firstNonEmpty(name, id)
		}
	}

	if upd.Empty() {
		return res, nil
	}

	channelID := strings.TrimSpace(accountRef)
	if channelID == "" {
		c, err := t.channel(ctx, clientID, accessToken)
		if err != nil {
			return nil, err
		}
		channelID = c.ChannelID
	}
	if err := t.UpdateChannel(ctx, clientID, accessToken, channelID, upd); err != nil {
		return nil, scopeAdvice(err, db.PlatformTrovo, t.MetadataCaps().Scope)
	}

	if upd.LiveTitle != "" {
		res.Applied = append(res.Applied, FieldTitle)
		res.Target = upd.LiveTitle
	}
	if upd.CategoryID != "" {
		res.Applied = append(res.Applied, FieldCategory)
		res.Category = categoryName
	}
	return res, nil
}

// ---------------------------------------------------------------------- stats

// Stats reads the connected channel's live state and viewer count.
//
// ONE CALL, AND IT IS THE ONE Ingest AND Account ALREADY MAKE. Trovo does
// publish a dedicated viewers endpoint -- POST
// /openplatform/channels/{channel_id}/viewers, §5.11, with no scope and no
// access token at all -- and it is deliberately NOT what this reads. Its `total`
// is documented as "The channel's total login users", so it counts signed-in
// viewers only and under-reports an audience by however many people are
// watching logged out. Showing an operator a number that means something other
// than "people watching" is the failure LiveStats.ViewerCount's comment is
// entirely about. current_viewers on the channel resource is Trovo's own
// "Number of current viewers", and it arrives on a response this provider
// fetches anyway.
//
// The count is reported only while the channel is live. Offline it is nil
// rather than zero, matching Twitch: a stream nobody is watching and a stream
// that is not running are different facts, and only one of them is an audience
// of none.
func (t *Trovo) Stats(ctx context.Context, clientID, accessToken string) (*LiveStats, error) {
	c, err := t.channel(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}
	stats := &LiveStats{
		Live:     c.IsLive,
		Title:    c.LiveTitle,
		Category: c.CategoryName,
		Language: c.LanguageCode,
		Slug:     c.Username,
		Source:   trovoChannelPath,
	}
	if c.IsLive {
		v := c.CurrentViewers
		stats.ViewerCount = &v
	}
	return stats, nil
}
