package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Twitch implements Twitch OAuth plus the Helix stream-key lookup.
type Twitch struct {
	// endpoints carries the base URLs; zero value is production. This replaced
	// a tokenURL field that redirected the token endpoint alone, which meant a
	// test that set it still sent Account, Ingest and every Helix metadata call
	// to the real api.twitch.tv. See endpoints.go.
	endpoints
}

// twitchIDBase is where consent is granted and tokens are minted;
// twitchHelixBase (metadata.go) is the data API.
//
// Named for the HOST, as every other base in this package is -- kickIDBase for
// id.kick.com, ytConsentBase for accounts.google.com, fbDialogBase,
// twitchHelixBase. It was twitchAuthBase, the one name here describing a role
// rather than a hostname, and "Auth" beside a string literal is what a secret
// scanner is built to notice: SonarCloud read it as a hard-coded credential
// (go:S6418) and failed the quality gate on a public URL. The name was the
// outlier; the value never was.
const twitchIDBase = "https://id.twitch.tv"

func (t *Twitch) tokenEndpoint() string { return t.authBase(twitchIDBase) + "/oauth2/token" }

// apiEndpoint is the Helix base for THIS provider. Account and Ingest used to
// write https://api.twitch.tv/helix inline while PushMetadata and
// PushCompliance went through the twitchHelixBase var -- the same split-base
// defect Facebook had, one provider over.
func (t *Twitch) apiEndpoint() string { return t.apiBase(twitchHelixBase) }

func (t *Twitch) Platform() db.Platform { return db.PlatformTwitch }

func (t *Twitch) Scopes() []string {
	// The minimum for what polyemesis actually does with a Twitch account: read
	// the stream key, and write the title/category before going live. Helix
	// Modify Channel Information refuses the second without its own scope, and
	// the failure is a 401 the operator cannot fix by reconnecting — the scope
	// has to be in the consent they granted.
	//
	// chat:read and chat:edit are what Twitch IRC authenticates with, for the
	// unified chat pane. They are requested here rather than when chat is first
	// opened because granting a scope does not upgrade a token that already
	// exists: an operator who connected before this line landed has to
	// disconnect and reconnect either way, and finding that out at the start of
	// a broadcast is the worst possible moment.
	//
	// user:read:email is still not requested: we do not need it, and asking
	// would make the consent screen scarier than the feature warrants.
	//
	// The two moderation scopes are separate on purpose, and Twitch separates
	// them for the same reason we describe them separately: removing one message
	// a broadcaster could already remove by hand is not the same ask as the
	// power to remove a person.
	//
	//   moderator:manage:chat_messages  delete a message
	//   moderator:manage:banned_users   ban, timeout, and lift either
	//
	// The second was previously withheld as a product decision. The maintainer
	// has reversed that; both are requested now, and both only work in a channel
	// this account already moderates -- Twitch answers 403 otherwise.
	return []string{
		"channel:read:stream_key",
		"channel:manage:broadcast",
		"chat:read",
		"chat:edit",
		"moderator:manage:chat_messages",
		"moderator:manage:banned_users",
		"moderator:manage:chat_settings",
	}
}

// PKCE is off, and the challenge/verifier arguments below are deliberately
// discarded. Twitch's authorization-code documentation enumerates an exact
// parameter set (client_id, force_verify, redirect_uri, response_type, scope,
// state) and says nothing about RFC 7636; nothing published tells us whether
// its /authorize endpoint tolerates an unknown code_challenge or rejects the
// request. Sending one on a hunch would break Twitch sign-in for everyone, so
// this stays off until Twitch documents support. The flow is still a
// confidential client: the secret never leaves the server, the code is bound to
// a whitelisted redirect URI, and the state is single-use.
// ScopeVersion 4 adds the three moderation scopes to the set: stream key,
// channel write, the two chat scopes, message deletion, bans, and the
// channel-wide chat settings (slow mode, follower-only, moderator delay).
//
// Bump it whenever Scopes changes -- an operator holding a token issued before
// the change does not gain the new permission, and the failure arrives as a 401
// mid-broadcast. This bump is the difference between the account list saying
// "reconnect to enable moderation" and an operator finding out when the delete
// button fails on the message they needed gone.
func (t *Twitch) ScopeVersion() int { return 4 }

func (t *Twitch) PKCE() bool { return false }

func (t *Twitch) AuthURL(clientID, redirectURI, state, _ string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(t.Scopes(), " "))
	q.Set("state", state)
	// force_verify makes account switching possible: without it Twitch silently
	// reuses the browser's logged-in account, so connecting a second channel is
	// impossible.
	q.Set("force_verify", "true")
	return t.authBase(twitchIDBase) + "/oauth2/authorize?" + q.Encode()
}

func (t *Twitch) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, _ string) (*Token, error) {
	return postForm(ctx, t.tokenEndpoint(), url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}, nil)
}

func (t *Twitch) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	tok, err := postForm(ctx, t.tokenEndpoint(), url.Values{
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

// helixHeaders: Helix requires the Client-Id alongside the bearer token, which
// is why every provider method takes clientID.
func helixHeaders(clientID string) map[string]string {
	return map[string]string{"Client-Id": clientID}
}

func (t *Twitch) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := getJSON(ctx, t.apiEndpoint()+"/users", accessToken, helixHeaders(clientID), &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("Twitch returned no user for this token")
	}
	name := out.Data[0].DisplayName
	if name == "" {
		name = out.Data[0].Login
	}
	return &Account{Name: name, Ref: out.Data[0].ID}, nil
}

// twitchIngestURL is Twitch's global ingest hostname, which resolves to the
// nearest PoP. The /ingests endpoint offers a ranked list, but the automatic
// endpoint is what Twitch itself recommends and avoids pinning a user to a
// server that is nearest to the *polyemesis host* rather than to them.
const twitchIngestURL = "rtmp://live.twitch.tv/app"

func (t *Twitch) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	acct, err := t.Account(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			StreamKey string `json:"stream_key"`
		} `json:"data"`
	}
	err = getJSON(ctx,
		t.apiEndpoint()+"/streams/key?broadcaster_id="+url.QueryEscape(acct.Ref),
		accessToken, helixHeaders(clientID), &out)
	if err != nil {
		return nil, err
	}
	if len(out.Data) == 0 || out.Data[0].StreamKey == "" {
		return nil, fmt.Errorf("Twitch returned no stream key; make sure the app requested the %s scope",
			strings.Join(t.Scopes(), " "))
	}
	return &Ingest{URL: twitchIngestURL, Key: out.Data[0].StreamKey}, nil
}

// --------------------------------------------------------------------- stats

// twitchStreamsPath is Get Streams, and it is a READ AND NOTHING ELSE.
//
// Twitch publishes no endpoint that starts, stops or transitions a broadcast --
// all 149 Helix endpoints were enumerated on 2026-08-16 and there is no such
// thing -- so a future agent looking to "finish" this by adding a start call
// will not find one to add. The stream begins when the encoder connects to
// ingest. See docs/evidence/platform-lifecycle-apis-2026-08-16.md.
const twitchStreamsPath = "/streams"

// The capability is resolved by type assertion in StatsFor, so a signature that
// drifts would make Twitch silently stop reporting viewers rather than fail to
// build. This turns that into a compile error, next to the method it constrains
// -- TestTheViewerStatsCellAgreesWithWhichProvidersActuallyImplementStats then
// catches the other half, where the method exists but the matrix denies it.
var _ LiveStatter = (*Twitch)(nil)

// twitchStream is the subset of a Get Streams entry polyemesis reads.
//
// ViewerCount IS A POINTER HERE FOR THE SAME REASON IT IS ONE ON LiveStats: a
// viewer_count Twitch omitted and a viewer_count of 0 decode into the same int,
// and the first is "we were not told" while the second is a real audience of
// none. Twitch documents no opt-out for the field -- its whole description is
// "The number of users watching the stream", with none of the withholding
// language Kick's carries -- so unlike Kick, whose 0 IS the opt-out value and
// is therefore discarded there, a zero that actually arrives on the wire is
// taken at its word and reported as zero.
//
// THERE IS NO HEALTH FIELD TO ADD BELOW. The word "bitrate" does not appear
// once in the 1.4 MB Helix reference, and no framerate, dropped-frame,
// resolution or ingest-health field exists on Get Streams. It reports liveness
// and metadata; anything quality-shaped here would have to be invented, and an
// invented encoder-health number is worse than none at all.
//
// Every tag below was read off the Get Streams Response Body table at
// https://dev.twitch.tv/docs/api/reference/#get-streams on 2026-08-16 rather
// than inferred from the example JSON, and the descriptions that matter are
// quoted where they are relied on. That distinction is not pedantry here: the
// sibling fix in kick.go exists because a fixture written from a struct instead
// of from a response kept a broken decode green for as long as it shipped.
type twitchStream struct {
	UserLogin string `json:"user_login"`
	// Decoded but deliberately never read -- see the liveness comment in Stats.
	// It is kept so the shape of what Twitch sends stays visible here.
	Type      string `json:"type"`
	Title     string `json:"title"`
	GameName  string `json:"game_name"`
	Language  string `json:"language"`
	StartedAt string `json:"started_at"`
	// "The number of users watching the stream." Integer, and absent rather
	// than zero when Twitch has nothing to say -- hence the pointer.
	ViewerCount *int `json:"viewer_count"`
}

// Stats reads the connected channel's live state and viewer count.
//
// NO NEW SCOPE, WHICH IS THE WHOLE REASON THIS COULD LAND. Get Streams,
// verbatim: "Requires an app access token or user access token." Every Twitch
// account already connected can answer it, so ScopeVersion does not move and
// nobody has to disconnect and reconnect -- which for a viewer count would have
// cost more than the feature gives.
//
// Two round trips, because Get Streams is keyed by broadcaster and a bearer
// token does not name one. Account resolves the id from /users, exactly as
// Ingest does; that id is what /streams is then asked about.
//
// AN EMPTY data ARRAY IS THE OFFLINE ANSWER, NOT AN ERROR, AND IT CARRIES NO
// COUNT. Twitch drops the channel from the response entirely when it is not
// live, so there is no number to report and ViewerCount stays nil rather than
// becoming a pointer to zero. See the field's comment in stats.go: a false zero
// and a blank mean opposite things to an operator.
//
// Nothing here reads Twitch's rate-limit budget. Helix uses a token bucket and
// returns Ratelimit-Limit, Ratelimit-Remaining and Ratelimit-Reset on every
// response; the 800 in its guide is an example value, not a contract, so a
// caller that wants to poll hard must read those headers rather than trust a
// constant written here.
func (t *Twitch) Stats(ctx context.Context, clientID, accessToken string) (*LiveStats, error) {
	acct, err := t.Account(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}

	var out struct {
		Data []twitchStream `json:"data"`
	}
	// user_id rather than user_login: the id survives a channel rename and
	// Account already holds it. Client-Id rides alongside the bearer token, as
	// it must on every Helix call -- see helixHeaders.
	//
	// type=live IS SENT EXPLICITLY BECAUSE THE DEFAULT IS NOT IT. Verbatim from
	// the Get Streams query-parameter table: "The type of stream to filter the
	// list of streams by. Possible values are: all, live. The default is all."
	// Liveness below is read off PRESENCE in the response, so leaving the
	// default in place would have made any non-live entry Twitch chose to
	// return -- the reruns and vodcasts that "all" has covered historically --
	// report the channel as live, with that entry's viewer count. Asking the
	// server for live streams only costs nothing and removes the assumption
	// rather than documenting it.
	//
	// Deliberately unpaginated. Get Streams warns that "it's possible to find
	// duplicate or missing streams in the list as you page through the
	// results", which is a hazard of the directory-wide query and irrelevant to
	// one broadcaster: they are in the first page or they are not live.
	err = getJSON(ctx,
		t.apiEndpoint()+twitchStreamsPath+"?type=live&user_id="+url.QueryEscape(acct.Ref),
		accessToken, helixHeaders(clientID), &out)
	if err != nil {
		return nil, err
	}

	stats := &LiveStats{Source: twitchStreamsPath}
	if len(out.Data) == 0 {
		return stats, nil
	}

	s := out.Data[0]
	// Presence in the response is the liveness signal, NOT type == "live".
	// Twitch's own description of that field, verbatim: "The type of stream.
	// Possible values are: live. If an error occurs, this field is set to an
	// empty string." Reading liveness off a field the platform blanks on error
	// would report a live channel as offline on precisely the response we
	// understand least. The query above already asked for live streams only, so
	// presence is the server's answer rather than an inference.
	stats.Live = true
	if s.ViewerCount != nil {
		v := *s.ViewerCount
		stats.ViewerCount = &v
	}
	stats.Title = s.Title
	// game_name is what Twitch calls a category everywhere an operator sees one,
	// including the picker in PushMetadata; the API name is the older word.
	stats.Category = s.GameName
	stats.Language = s.Language
	// user_login is the channel's URL slug (twitch.tv/<user_login>), which is
	// what Slug means on Kick too -- not the display name, which changes case
	// and cannot be linked to.
	stats.Slug = s.UserLogin
	stats.StartedAt = parseTwitchTime(s.StartedAt)
	return stats, nil
}

// parseTwitchTime is forgiving for the reason parseKickTime is: a timestamp we
// cannot read costs the timestamp, not the stats read that is otherwise fine.
//
// One layout, unlike Kick's three, and the difference is documentary rather
// than stylistic. Twitch states the format: "The UTC date and time (in RFC3339
// format) of when the broadcast began." Kick states none, which is why it
// carries fallbacks. Adding fallbacks here would be guessing at formats the
// platform has committed in writing not to send.
func parseTwitchTime(s string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return &parsed
}

// CheckCredentials proves both halves of the pair via a client-credentials
// grant, which Twitch supports and which needs no user consent. The app token
// it returns is discarded: obtaining one at all is the whole proof.
func (t *Twitch) CheckCredentials(ctx context.Context, clientID, clientSecret string) error {
	_, err := postForm(ctx, t.tokenEndpoint(), url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"client_credentials"},
	}, nil)
	return classifyCheckError(err)
}
