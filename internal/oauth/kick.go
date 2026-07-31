package oauth

// Kick is the first platform polyemesis can sign into but cannot fetch a
// stream key from. Its public API covers the channel, the category directory,
// chat and livestream stats, and every one of those is worth having — but the
// key itself is absent from Channels, Livestreams and Users alike. Rather than
// leave Kick as "unsupported" (which loses the title push, the category push
// and the viewer count along with it), it is a full Provider whose Ingest
// returns ErrNoStreamKeyAPI. See ManualKey below: the inability is a capability
// the UI can read up front, not a surprise 502 at the moment the operator
// clicks Fetch key.
//
// A future agent will be tempted to "finish" Ingest by guessing a URL. Do not.
// The absence was verified against Kick's published API, and a fabricated
// endpoint would turn a clear instruction into a 404 nobody can diagnose.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ErrNoStreamKeyAPI marks the ingest lookup as impossible rather than failed.
// Callers should errors.Is against it and send the operator to the paste field:
// a platform that never had a key endpoint is not a transport problem, and
// reporting it as a bad gateway invites a retry that can never work.
var ErrNoStreamKeyAPI = errors.New("this platform publishes no stream-key endpoint, so the key must be pasted by hand")

// ManualKey is the negative capability that goes with ErrNoStreamKeyAPI.
// Discover it with ManualKeyFor; like MetadataPusher it is a second interface
// rather than a method on Provider, so the three platforms that can fetch a key
// stay unaware of it and the UI has exactly one place to ask.
type ManualKey interface {
	Provider
	// ManualKeyReason is shown next to the stream-key field. It says where the
	// key lives and what still works without it, in the operator's words.
	ManualKeyReason() string
}

// ManualKeyFor reports whether a platform authenticates but cannot supply an
// ingest. False covers both "fetches its key fine" and "has no provider at
// all", because neither needs the paste-it-yourself hint.
func ManualKeyFor(p db.Platform) (ManualKey, bool) {
	pr, ok := Providers()[p]
	if !ok {
		return nil, false
	}
	mk, ok := pr.(ManualKey)
	return mk, ok
}

// Kick implements Kick's OAuth 2.1 flow plus the parts of its public API that
// polyemesis can use: channel identity, title/category push, category search
// and livestream stats.
type Kick struct {
	// idBase overrides https://id.kick.com. Empty in production; set by tests.
	idBase string
}

func (k *Kick) idEndpoint() string {
	if k.idBase != "" {
		return k.idBase
	}
	return kickIDBase
}

// Endpoints are vars so tests can point the provider at a stub. Nothing at
// runtime rewrites them.
var (
	kickIDBase  = "https://id.kick.com"
	kickAPIBase = "https://api.kick.com"
)

func (k *Kick) Platform() db.Platform { return db.PlatformKick }

// Scopes covers what polyemesis does with a Kick account today plus the chat
// surface landing alongside it. The list is deliberately settled in one go:
// adding a scope later does not upgrade an existing connection, it forces every
// operator to disconnect and reconnect, and discovering that mid-broadcast is
// the worst possible moment.
//
// moderation:ban was previously omitted, on the grounds that nothing here banned
// a viewer and that asking a restreamer's audience for the power to do so read
// as overreach. That was a product decision and the maintainer has reversed it:
// banning and timing out are implemented, so the scope is requested. The old
// reasoning is kept rather than deleted because it is the argument to re-read if
// the decision is ever revisited. See docs/roadmap/CHAT-MODERATION.md.
func (k *Kick) Scopes() []string {
	return []string{
		"user:read",                      // who the token belongs to
		"channel:read",                   // channel identity and live state
		"channel:write",                  // title and category push
		"chat:write",                     // send chat as the user or a bot
		"moderation:chat_message:manage", // delete a message from the unified chat
		"moderation:ban",                 // ban and timeout, and lift either
		"events:subscribe",               // webhooks for chat and livestream state
		// The stream key. NOT covered by channel:read, which is what made this
		// look impossible for so long: the key rides as stream.key on the very
		// same GET /public/v1/channels response that channel:read already
		// fetches, but the field is omitted unless this scope was granted too.
		// There is no /streamkey endpoint to find, so reading the endpoint list
		// suggests the capability does not exist.
		"streamkey:read",
	}
}

// PKCE is on, and unlike the other providers it is not optional: Kick's
// authorization server speaks OAuth 2.1, which folds RFC 7636 into the
// authorization-code grant itself. An exchange without a verifier is refused.
// ScopeVersion 2 adds moderation:ban. Version 1 added streamkey:read, which was
// exactly the case this mechanism exists for: an account connected before it
// landed holds a token without the scope, and the stream key silently never
// arrives. The same applies here — an account on version 1 can delete a message
// and cannot ban anybody, and the account list says so instead of letting the
// button fail.
func (k *Kick) ScopeVersion() int { return 2 }

func (k *Kick) PKCE() bool { return true }

func (k *Kick) AuthURL(clientID, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(k.Scopes(), " "))
	q.Set("state", state)
	// Guarded even though PKCE reports true: the shared pkce tests feed every
	// provider an empty challenge to prove none of them sends a bare parameter,
	// and an empty code_challenge is rejected differently — and far more
	// confusingly — than an absent one.
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	return k.idEndpoint() + "/oauth/authorize?" + q.Encode()
}

func (k *Kick) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error) {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}
	// A missing verifier is not pre-refused here. Kick will say so itself, in
	// its own words, and a local guess about what its authorization server
	// requires would be one more thing to get wrong the day Kick relaxes it.
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	return postForm(ctx, k.idEndpoint()+"/oauth/token", form, nil)
}

func (k *Kick) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	tok, err := postForm(ctx, k.idEndpoint()+"/oauth/token", url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}, nil)
	if err != nil {
		return nil, err
	}
	// Kick rotates refresh tokens, but a response that omits one must not
	// silently disconnect the account an hour later.
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

// kickChannel is the subset of GET /public/v1/channels we use. Kick keys a
// channel by broadcaster_user_id and names it by slug; there is no separate
// display name on this resource.
type kickChannel struct {
	BroadcasterUserID int    `json:"broadcaster_user_id"`
	Slug              string `json:"slug"`
	StreamTitle       string `json:"stream_title"`
	Category          struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"category"`
	Stream struct {
		IsLive      bool   `json:"is_live"`
		ViewerCount int    `json:"viewer_count"`
		StartTime   string `json:"start_time"`
		Language    string `json:"language"`
		// The ingest pair, present only when the token carries streamkey:read.
		// Absent — not empty-and-present — for a token granted before that
		// scope was requested, which is what Ingest distinguishes on.
		URL string `json:"url"`
		Key string `json:"key"`
	} `json:"stream"`
}

// channel reads the token's own channel. Kick authenticates with the bearer
// token alone, so clientID is unused throughout this provider — it stays in the
// signatures because Twitch's Helix needs it and Provider is one interface.
func (k *Kick) channel(ctx context.Context, accessToken string) (*kickChannel, error) {
	var out struct {
		Data []kickChannel `json:"data"`
	}
	// No parameters: Kick reads the channel belonging to the token.
	if err := getJSON(ctx, kickAPIBase+"/public/v1/channels", accessToken, nil, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("Kick returned no channel for this token; make sure the app requested the channel:read scope")
	}
	c := out.Data[0]
	return &c, nil
}

func (k *Kick) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	c, err := k.channel(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	ref := strconv.Itoa(c.BroadcasterUserID)
	name := c.Slug
	if name == "" {
		name = ref
	}
	return &Account{Name: name, Ref: ref}, nil
}

// ManualKeyReason is the whole of the bad news, plus the good news that
// follows it. It carries no URL on purpose: an ingest hostname invented to make
// the message look complete is exactly the failure this provider exists to
// avoid, and the Kick preset already links the dashboard.
// ManualKeyReason is no longer the normal path. Kick DOES expose the key, on
// the channels resource, behind the streamkey:read scope — see Ingest. This is
// what an operator sees when their token predates that scope, which is the one
// case left where the key really cannot be fetched.
func (k *Kick) ManualKeyReason() string {
	return "This Kick account was connected before polyemesis asked for the stream-key " +
		"scope, and granting a scope never upgrades a token that has already been issued. " +
		"Disconnect the account and connect it again — once, and the key is fetched " +
		"automatically from then on. Until then, paste the stream URL and key from your " +
		"Kick dashboard under Settings → Stream."
}

// Ingest reads the channel's stream URL and key.
//
// This used to return ErrNoStreamKeyAPI unconditionally, and the reasoning
// recorded for that was wrong in an instructive way. Kick publishes no
// /streamkey endpoint, so an endpoint-by-endpoint reading of the API finds
// nothing and concludes the capability is absent. The key is actually a field
// on the channels resource we were ALREADY fetching for identity and live
// state -- withheld unless the token also carries streamkey:read, which the
// Get Channels page does not list under its required scopes.
//
// So the field was invisible twice over: absent from the endpoint list, and
// absent from the response we were looking at.
func (k *Kick) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	c, err := k.channel(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	if c.Stream.Key == "" {
		// Almost always a token minted before streamkey:read was requested.
		// Granting a scope never upgrades a token already issued, so the only
		// fix is a reconnect -- and saying that is the whole value of this
		// branch, because the symptom otherwise looks like Kick being broken.
		return nil, fmt.Errorf("%w. %s", ErrNoStreamKeyAPI, k.ManualKeyReason())
	}
	if c.Stream.URL == "" {
		// Deliberately NOT defaulted to a hardcoded ingest host. Kick fronts
		// its ingest with a CDN and the host has changed before; a stale
		// constant here would publish to nowhere and look like a polyemesis
		// bug. Returning the key we did read, and asking for the URL, is the
		// honest failure.
		return nil, fmt.Errorf("Kick returned a stream key but no ingest URL. "+
			"Copy the Stream URL from your Kick dashboard (Settings %s Stream) "+
			"into this destination; the key has been filled in for you", "→")
	}
	return &Ingest{URL: c.Stream.URL, Key: c.Stream.Key}, nil
}

// ---------------------------------------------------------------- categories

// KickCategory is one entry in Kick's category directory.
type KickCategory struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Thumbnail string `json:"thumbnail,omitempty"`
}

// SearchCategories looks up categories by name. Kick's channel update takes a
// numeric category_id and nothing else, so without this the operator has to
// find an integer by hand — which in practice means not setting a category at
// all.
func (k *Kick) SearchCategories(ctx context.Context, clientID, accessToken, query string) ([]KickCategory, error) {
	var out struct {
		Data []KickCategory `json:"data"`
	}
	err := getJSON(ctx, kickAPIBase+"/public/v1/categories?q="+url.QueryEscape(query), accessToken, nil, &out)
	if err != nil {
		return nil, err
	}
	return out.Data, nil
}

// categoryID turns what the operator typed into the id Kick wants.
//
// A string of digits is taken at its word rather than searched for. That is the
// escape hatch: an operator who already knows the id — or whose category is too
// new for the directory to match — can still set one, instead of being told no
// by a lookup that is only ever advisory.
func (k *Kick) categoryID(ctx context.Context, clientID, accessToken, name string) (id int, resolved string, err error) {
	if n, convErr := strconv.Atoi(strings.TrimSpace(name)); convErr == nil && n > 0 {
		return n, "", nil
	}

	found, err := k.SearchCategories(ctx, clientID, accessToken, name)
	if err != nil {
		return 0, "", err
	}
	want := normaliseCategory(name)
	for _, c := range found {
		if normaliseCategory(c.Name) == want {
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
		return 0, "", fmt.Errorf("Kick has no category called %q. Did you mean: %s?",
			name, strings.Join(names, ", "))
	}
	return 0, "", fmt.Errorf("Kick has no category matching %q", name)
}

// ------------------------------------------------------------------ metadata

// KickChannelUpdate is the writable part of a Kick channel. Zero fields are
// omitted, so a caller that only wants a title cannot blank the category.
type KickChannelUpdate struct {
	StreamTitle string
	CategoryID  int
	// CustomTags replaces the channel's tags. Kick documents a limit of ten and
	// this does not enforce it: Kick's own rejection names the limit, stays
	// right if the limit moves, and beats silently dropping the eleventh tag
	// the operator meant to set.
	CustomTags []string
}

// Empty reports whether there is nothing to write.
func (u KickChannelUpdate) Empty() bool {
	return u.StreamTitle == "" && u.CategoryID == 0 && u.CustomTags == nil
}

// UpdateChannel writes the channel's live metadata. Kick scopes the PATCH to
// the token's own channel, so there is no id to pass.
func (k *Kick) UpdateChannel(ctx context.Context, accessToken string, u KickChannelUpdate) error {
	if u.Empty() {
		return nil
	}
	body := map[string]any{}
	if u.StreamTitle != "" {
		body["stream_title"] = u.StreamTitle
	}
	if u.CategoryID > 0 {
		body["category_id"] = u.CategoryID
	}
	if u.CustomTags != nil {
		body["custom_tags"] = u.CustomTags
	}
	return requestJSON(ctx, http.MethodPatch, kickAPIBase+"/public/v1/channels", accessToken, body, nil, nil)
}

// PushBroadcastSettings writes the one field of BroadcastSettings that Kick
// has: custom_tags.
//
// Kick has no broadcast RESOURCE at all -- no scheduled start, no DVR, no
// editing window -- so most of this type does not apply and is reported as
// skipped rather than silently ignored. An operator who set a DVR toggle and
// saw nothing happen deserves to be told the platform has no such thing.
//
// Implementing the same optional interface as YouTube rather than inventing a
// second path: the field the two share is tags, and one code path in the API
// layer beats two that must be kept in step.
func (k *Kick) PushBroadcastSettings(ctx context.Context, clientID, accessToken string, s BroadcastSettings) (*MetadataResult, error) {
	res := &MetadataResult{}
	if s.ScheduledStart != nil {
		res.Skipped = append(res.Skipped, FieldScheduledStart)
		res.Warnings = append(res.Warnings,
			"Kick has no scheduled start; it has no broadcast resource to schedule")
	}
	if s.TouchesContentDetails() {
		res.Skipped = append(res.Skipped, FieldContentDetails)
		res.Warnings = append(res.Warnings,
			"Kick has no DVR, auto-start or monitor-stream settings")
	}
	if s.Tags == nil {
		return res, nil
	}

	// Tags REPLACE, exactly as they do on YouTube. Kick documents a limit of
	// ten and this does not enforce it, for the reason KickChannelUpdate
	// gives: Kick's own rejection names the limit and stays right if it moves.
	if err := k.UpdateChannel(ctx, accessToken, KickChannelUpdate{CustomTags: *s.Tags}); err != nil {
		return nil, scopeAdvice(err, db.PlatformKick, k.MetadataCaps().Scope)
	}
	res.Applied = append(res.Applied, FieldTags)
	return res, nil
}

func (k *Kick) MetadataCaps() MetadataCaps {
	return MetadataCaps{
		// No description: a Kick channel has a description, but the live
		// broadcast does not, and the channel update accepts only a title, a
		// category and tags. Saying so here keeps it out of the failure list.
		// Tags ARE supported: custom_tags is one of the three fields the
		// channel PATCH takes. Advertised so the composer offers the control
		// for Kick as well as YouTube rather than greying it out.
		Fields:        []MetadataField{FieldTitle, FieldCategory, FieldTags},
		CategoryLabel: "Category",
		CategoryHint:  "A Kick category, e.g. Just Chatting, Grand Theft Auto V. A numeric category id also works.",
		// TitleMax is left at zero: Kick publishes no title length, and a limit
		// invented here would truncate a title Kick would have accepted.
		Scope: "channel:write",
	}
}

// PushMetadata ignores accountRef for the same reason UpdateChannel takes no
// id: the token identifies the channel.
func (k *Kick) PushMetadata(ctx context.Context, clientID, accessToken, accountRef string, m Metadata) (*MetadataResult, error) {
	m = m.Trimmed()
	res := &MetadataResult{}

	if m.Description != "" {
		res.Skipped = append(res.Skipped, FieldDescription)
		res.Warnings = append(res.Warnings, "Kick has no description on a live broadcast, so it was left out")
	}

	upd := KickChannelUpdate{StreamTitle: m.Title}

	// Resolved before the write so a category we cannot find costs a warning
	// rather than the title change.
	var categoryName string
	if m.Category != "" {
		id, name, err := k.categoryID(ctx, clientID, accessToken, m.Category)
		if err != nil {
			res.Skipped = append(res.Skipped, FieldCategory)
			res.Warnings = append(res.Warnings, err.Error())
		} else {
			upd.CategoryID = id
			categoryName = firstNonEmpty(name, strconv.Itoa(id))
		}
	}

	if upd.Empty() {
		return res, nil
	}
	if err := k.UpdateChannel(ctx, accessToken, upd); err != nil {
		return nil, scopeAdvice(err, db.PlatformKick, k.MetadataCaps().Scope)
	}

	if upd.StreamTitle != "" {
		res.Applied = append(res.Applied, FieldTitle)
		res.Target = upd.StreamTitle
	}
	if upd.CategoryID > 0 {
		res.Applied = append(res.Applied, FieldCategory)
		res.Category = categoryName
	}
	return res, nil
}

// --------------------------------------------------------------------- stats

// KickStats is a point-in-time read of the connected channel's broadcast.
// Offline is a normal answer, not an error: a channel that is not live has a
// viewer count of zero and nothing has gone wrong.
type KickStats struct {
	Live        bool      `json:"live"`
	ViewerCount int       `json:"viewerCount"`
	Title       string    `json:"title,omitempty"`
	Category    string    `json:"category,omitempty"`
	Language    string    `json:"language,omitempty"`
	Slug        string    `json:"slug,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	// Source names the endpoint the numbers came from, so a viewer count that
	// disagrees with the Kick dashboard can be traced without a packet capture.
	Source string `json:"source,omitempty"`
}

// kickLivestream is the livestream resource, shared by the user-scoped list and
// the stats endpoint. Every field is optional; Kick documents the endpoints but
// not a guarantee that all of them are populated when a channel is offline.
type kickLivestream struct {
	BroadcasterUserID int    `json:"broadcaster_user_id"`
	Slug              string `json:"slug"`
	StreamTitle       string `json:"stream_title"`
	Language          string `json:"language"`
	StartedAt         string `json:"started_at"`
	ViewerCount       int    `json:"viewer_count"`
	// Viewers is an accepted alternate spelling. The stats endpoint's body is
	// not published field by field, and reporting zero viewers to someone with
	// an audience is a worse lie than a redundant struct tag.
	Viewers  int `json:"viewers"`
	Category struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"category"`
}

func (l kickLivestream) viewers() int {
	if l.ViewerCount > 0 {
		return l.ViewerCount
	}
	return l.Viewers
}

// decodeKickData accepts both shapes Kick's envelopes use: a list of
// livestreams and a single object. Guessing one and getting it wrong would turn
// a working stats read into a decode error.
func decodeKickData(raw json.RawMessage) []kickLivestream {
	if len(raw) == 0 {
		return nil
	}
	var list []kickLivestream
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var one kickLivestream
	if err := json.Unmarshal(raw, &one); err == nil {
		return []kickLivestream{one}
	}
	return nil
}

func (k *Kick) livestreams(ctx context.Context, accessToken, endpoint string) ([]kickLivestream, error) {
	var out struct {
		Data json.RawMessage `json:"data"`
	}
	if err := getJSON(ctx, kickAPIBase+endpoint, accessToken, nil, &out); err != nil {
		return nil, err
	}
	return decodeKickData(out.Data), nil
}

const (
	kickUserLivestreamsPath = "/public/v1/users/livestreams"
	kickLivestreamStatsPath = "/public/v1/livestreams/stats"
)

// Stats reads the connected channel's live state and viewer count.
//
// The user-scoped list is tried first because it carries the title and category
// alongside the count; the stats endpoint is the fallback and also fills in a
// count the first call left at zero. Only a failure of both is an error — one
// endpoint being unavailable to a given app's scopes must not cost the operator
// the number the other one returned.
func (k *Kick) Stats(ctx context.Context, clientID, accessToken string) (*KickStats, error) {
	stats := &KickStats{}

	users, userErr := k.livestreams(ctx, accessToken, kickUserLivestreamsPath)
	if userErr == nil && len(users) > 0 {
		l := users[0]
		stats.Live = true
		stats.ViewerCount = l.viewers()
		stats.Title = l.StreamTitle
		stats.Category = l.Category.Name
		stats.Language = l.Language
		stats.Slug = l.Slug
		stats.StartedAt = parseKickTime(l.StartedAt)
		stats.Source = kickUserLivestreamsPath
	}

	// The second call is skipped once the first one has both a live channel and
	// a count: it exists to fill gaps, not to spend a round trip proving the
	// first answer.
	if stats.Live && stats.ViewerCount > 0 {
		return stats, nil
	}

	agg, aggErr := k.livestreams(ctx, accessToken, kickLivestreamStatsPath)
	if aggErr != nil {
		if userErr != nil {
			return nil, userErr
		}
		// The user list answered; the aggregate one not answering is not the
		// operator's problem.
		return stats, nil
	}
	for _, l := range agg {
		if v := l.viewers(); v > stats.ViewerCount {
			stats.ViewerCount = v
			if stats.Source == "" {
				stats.Source = kickLivestreamStatsPath
			}
		}
		if v := l.viewers(); v > 0 {
			stats.Live = true
		}
	}
	if userErr != nil && stats.Source == "" {
		stats.Source = kickLivestreamStatsPath
	}
	return stats, nil
}

// parseKickTime is deliberately forgiving: an unparseable timestamp yields the
// zero time rather than failing a stats read that is otherwise fine.
func parseKickTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// CheckCredentials proves the pair through Kick's app-access-token flow, which
// its OAuth 2.1 documentation exposes at POST /oauth/token with
// grant_type=client_credentials and needs no user consent.
func (k *Kick) CheckCredentials(ctx context.Context, clientID, clientSecret string) error {
	_, err := postForm(ctx, k.idEndpoint()+"/oauth/token", url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"client_credentials"},
	}, nil)
	return classifyCheckError(err)
}
