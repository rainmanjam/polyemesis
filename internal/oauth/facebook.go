package oauth

// Facebook Live.
//
// Two things make this provider unlike YouTube and Twitch, and both of them
// shape everything below.
//
//  1. There is no persistent stream key. A Facebook broadcast is a live_video
//     object, created per go-live, and its ingest dies with it. "Fetch the
//     key" therefore means "create the broadcast", which is why Ingest here has
//     a side effect the other providers do not have. The live_video id it
//     returns is the handle for everything that comes afterwards — ending the
//     broadcast, editing its title, reading its comments — so it is carried out
//     of here rather than dropped on the floor. See Broadcast.
//
//  2. One connected login can publish to more than one place: the person's own
//     profile, or any Page they manage. Those need different permissions
//     (publish_video versus pages_manage_posts + pages_read_engagement) and,
//     for a Page, a different access token entirely. So this provider carries a
//     target alongside the account — see TargetedProvider — and Provider's
//     target-less methods mean "the profile", which is the configuration that
//     needs the fewest permissions and covers the most people.
//
// Every capability check in here fails open. A token that cannot list Pages is
// a profile-only connection, not an error; a permission we cannot see is one
// the platform gets to refuse with a message we turn into advice.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// fbGraphBase and fbDialogBase are Facebook's production hosts. They are
// constants rather than vars because nothing -- runtime or test -- rewrites
// them any more: a test redirects a provider with NewFacebook(WithBaseURL(...)),
// which is per-instance. They were vars, and facebook_test.go assigned to them
// under a t.Cleanup restore, which is what made these tests order-dependent
// under -count=N and impossible to run in parallel.
//
// The version is pinned rather than left off: an unversioned Graph call follows
// whatever Meta has made current, and a broadcast that starts working
// differently on a Tuesday is not a failure mode worth having. v24.0 is also
// the first version that removed overlay_url, which we deliberately never send.
const (
	fbGraphBase  = "https://graph.facebook.com/v24.0"
	fbDialogBase = "https://www.facebook.com/v24.0/dialog/oauth"
)

// Facebook implements Facebook Login plus the Live Video API.
type Facebook struct {
	// endpoints carries the base URLs. Zero value is production; see
	// endpoints.go for why this replaced both an unexported graphBase field
	// and a package var, and what went wrong while there were two of them.
	endpoints
}

// graphEndpoint is where every Graph call goes. It is a method on *Facebook,
// not a package-level string, because fbGet and fbPost are the only two
// functions that build a Graph URL and both must honour THIS provider's base --
// the previous split, where graphEndpoint covered the credential check and a
// package var covered the other twelve call sites, is the defect this fixes.
func (f *Facebook) graphEndpoint() string { return f.apiBase(fbGraphBase) }

func (f *Facebook) Platform() db.Platform { return db.PlatformFacebook }

// Scopes covers both targets at once. A creator streaming to their own profile
// only needs publish_video and will never exercise the Page permissions; a
// business streaming to a Page needs the other three. Asking for the union is
// what lets one connection serve both, and Facebook lets a user decline
// individual permissions on the consent screen, so the Page scopes cost a
// profile-only user nothing but a line on that screen.
//
// pages_show_list is what makes /me/accounts readable at all — without it we
// cannot even offer the Page choice, whatever else was granted.
func (f *Facebook) Scopes() []string {
	return []string{
		"public_profile",
		"publish_video",
		"pages_show_list",
		"pages_manage_posts",
		"pages_read_engagement",
	}
}

// PKCE is off, and the challenge/verifier arguments are deliberately discarded,
// for the same reason Twitch's are. Meta's Facebook Login manual-flow
// documentation enumerates its parameters (client_id, redirect_uri, state,
// response_type, scope, auth_type) and says nothing about RFC 7636; nothing
// published tells us whether the dialog tolerates an unknown code_challenge or
// rejects the request outright. Guessing wrong here does not degrade a
// defence-in-depth measure, it locks every user out of sign-in — so this stays
// off until Meta documents support. The flow remains a confidential client: the
// secret never leaves the server, the code is bound to a whitelisted redirect
// URI, and the state is single-use.
// ScopeVersion 1 is the set above. Bump whenever Scopes changes.
func (f *Facebook) ScopeVersion() int { return 1 }

func (f *Facebook) PKCE() bool { return false }

func (f *Facebook) AuthURL(clientID, redirectURI, state, _ string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	// Facebook's scope parameter is comma-delimited, not space-delimited like
	// the rest of the world's.
	q.Set("scope", strings.Join(f.Scopes(), ","))
	q.Set("state", state)
	// rerequest is Facebook's equivalent of Twitch's force_verify: without it a
	// user who declined publish_video the first time is bounced straight back
	// with the same partial grant and no chance to fix it.
	q.Set("auth_type", "rerequest")
	return f.authBase(fbDialogBase) + "?" + q.Encode()
}

// tokenURL is where both the code exchange and the long-lived upgrade go. It
// was a free function reading the package var, which meant a provider aimed at
// a stub still exchanged codes against the real graph.facebook.com.
func (f *Facebook) tokenURL() string { return f.authBase(fbGraphBase) + "/oauth/access_token" }

// Exchange trades the code for a short-lived user token.
//
// Facebook issues no refresh token at all. What it has instead is
// fb_exchange_token, which trades a valid token for a longer-lived one, so the
// access token doubles as its own refresh credential — hence the assignment
// below. Refresh performs that upgrade, which means the first refresh turns the
// ~2-hour token from this call into a ~60-day one. Doing the upgrade here
// instead would make sign-in depend on a second network call that, if it
// failed, would fail a sign-in that had already succeeded.
func (f *Facebook) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, _ string) (*Token, error) {
	tok, err := postForm(ctx, f.tokenURL(), url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"code":          {code},
	}, nil)
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = tok.AccessToken
	}
	// Facebook returns granted permissions on /me/permissions rather than in
	// the token response, so record what we asked for. It is advisory only —
	// nothing in polyemesis refuses a call because this list looks short.
	if tok.Scopes == "" {
		tok.Scopes = strings.Join(f.Scopes(), " ")
	}
	return tok, nil
}

// Refresh re-exchanges the stored token for a fresh long-lived one. This works
// while the current token is still valid; once it has expired the only cure is
// reconnecting, which is what the 190 branch of fbAdvice tells the operator.
func (f *Facebook) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	tok, err := postForm(ctx, f.tokenURL(), url.Values{
		"grant_type":        {"fb_exchange_token"},
		"client_id":         {clientID},
		"client_secret":     {clientSecret},
		"fb_exchange_token": {refreshToken},
	}, nil)
	if err != nil {
		return nil, err
	}
	// Self-referential on purpose: see Exchange.
	tok.RefreshToken = tok.AccessToken
	return tok, nil
}

// --------------------------------------------------------------- targets

// Facebook's implementation of TargetedProvider. The interface itself, and
// BroadcastTarget/Broadcast/IngestOptions with it, moved to oauth.go beside
// Provider -- see the note there.

// Target ref spellings. A bare id with no prefix is treated as "look it up",
// which is what keeps a ref stored by an older build working.
const (
	fbRefUser = "user:"
	fbRefPage = "page:"
)

type fbKind int

const (
	fbKindUser fbKind = iota
	fbKindPage
	fbKindAuto
)

func parseTargetRef(ref string) (fbKind, string) {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return fbKindUser, ""
	case strings.HasPrefix(ref, fbRefUser):
		return fbKindUser, strings.TrimPrefix(ref, fbRefUser)
	case strings.HasPrefix(ref, fbRefPage):
		return fbKindPage, strings.TrimPrefix(ref, fbRefPage)
	default:
		return fbKindAuto, ref
	}
}

// fbTarget is a resolved target: the Graph node to address and the token that
// may address it. The token is unexported and never rendered — a Page access
// token is as sensitive as the user token it came from.
type fbTarget struct {
	ref   string
	kind  fbKind
	node  string
	name  string
	token string
}

type fbUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type fbPage struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	AccessToken string `json:"access_token"`
}

func (f *Facebook) me(ctx context.Context, accessToken string) (*fbUser, error) {
	var out fbUser
	if err := f.get(ctx, accessToken, "/me", url.Values{"fields": {"id,name"}}, &out); err != nil {
		return nil, fbAdvice(err, "read the Facebook profile", f.Scopes())
	}
	if out.ID == "" {
		return nil, fmt.Errorf("Facebook returned no user for this token; reconnect the account")
	}
	return &out, nil
}

// pages lists the Pages this login manages. The access_token field is the Page
// token, which is what a Page broadcast must be created with.
func (f *Facebook) pages(ctx context.Context, accessToken string) ([]fbPage, error) {
	var out struct {
		Data []fbPage `json:"data"`
	}
	err := f.get(ctx, accessToken, "/me/accounts",
		url.Values{"fields": {"id,name,category,access_token"}, "limit": {"100"}}, &out)
	if err != nil {
		return nil, fbAdvice(err, "list Facebook Pages", []string{"pages_show_list"})
	}
	return out.Data, nil
}

// Targets lists the profile first, then every Page.
//
// A failure to read Pages is swallowed on purpose: streaming to your own
// profile needs nothing but publish_video, and turning "you did not grant
// pages_show_list" into an error would refuse a setup that works perfectly.
func (f *Facebook) Targets(ctx context.Context, clientID, accessToken string) ([]BroadcastTarget, error) {
	me, err := f.me(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	out := []BroadcastTarget{{
		Ref:  fbRefUser + me.ID,
		Kind: "user",
		Name: me.Name,
	}}
	pages, err := f.pages(ctx, accessToken)
	if err != nil {
		return out, nil
	}
	for _, p := range pages {
		out = append(out, BroadcastTarget{
			Ref:      fbRefPage + p.ID,
			Kind:     "page",
			Name:     p.Name,
			Category: p.Category,
		})
	}
	return out, nil
}

// resolveTarget turns a ref into the node to POST to and the token to do it
// with.
func (f *Facebook) resolveTarget(ctx context.Context, accessToken, ref string) (*fbTarget, error) {
	kind, id := parseTargetRef(ref)

	if kind == fbKindPage || kind == fbKindAuto {
		pages, err := f.pages(ctx, accessToken)
		if err != nil {
			// An explicit Page ref cannot fall back to the profile: publishing a
			// business broadcast to someone's personal timeline because we could
			// not read the Page list would be worse than refusing.
			if kind == fbKindPage {
				return nil, err
			}
			pages = nil
		}
		for _, p := range pages {
			if p.ID == id {
				return &fbTarget{
					ref: fbRefPage + p.ID, kind: fbKindPage,
					node: p.ID, name: p.Name, token: p.AccessToken,
				}, nil
			}
		}
		if kind == fbKindPage {
			return nil, fmt.Errorf("this Facebook login no longer manages Page %s. "+
				"Reconnect the account and grant access to that Page, or pick a different target", id)
		}
	}

	// The profile. "me" resolves against whichever user the token belongs to,
	// so no lookup is needed to publish — the id only matters for display.
	return &fbTarget{ref: ref, kind: fbKindUser, node: "me", token: accessToken}, nil
}

// Account identifies the default target: the person's own profile.
func (f *Facebook) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	return f.AccountFor(ctx, clientID, accessToken, "")
}

// AccountFor identifies one chosen target. The returned Ref is the prefixed
// form, and is what IngestFor and PushMetadata expect back.
func (f *Facebook) AccountFor(ctx context.Context, clientID, accessToken, targetRef string) (*Account, error) {
	kind, id := parseTargetRef(targetRef)

	if kind == fbKindPage || kind == fbKindAuto {
		pages, err := f.pages(ctx, accessToken)
		if err != nil && kind == fbKindPage {
			return nil, err
		}
		for _, p := range pages {
			if p.ID == id {
				// "(Page)" is worth the noise: a person and their Page routinely
				// share a name, and the two are not interchangeable.
				return &Account{Name: p.Name + " (Page)", Ref: fbRefPage + p.ID}, nil
			}
		}
		if kind == fbKindPage {
			return nil, fmt.Errorf("this Facebook login does not manage Page %s; "+
				"reconnect the account and grant access to it", id)
		}
	}

	me, err := f.me(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	return &Account{Name: me.Name, Ref: fbRefUser + me.ID}, nil
}

// --------------------------------------------------------------- ingest

type fbLiveVideo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Title  string `json:"title"`
	// Facebook returns four ingest fields: an rtmp:// and an rtmps:// primary,
	// and a list of each for the backup encoder.
	StreamURL          string   `json:"stream_url"`
	SecureStreamURL    string   `json:"secure_stream_url"`
	StreamSecondary    []string `json:"stream_secondary_urls"`
	SecureStreamSecond []string `json:"secure_stream_secondary_urls"`
}

// fbLiveVideoFields is what a follow-up read asks for. Requested explicitly
// because a Graph read returns a default field set that does not include the
// ingest URLs.
const fbLiveVideoFields = "id,status,title,stream_url,secure_stream_url," +
	"stream_secondary_urls,secure_stream_secondary_urls"

// Ingest creates a broadcast on the default target and returns its ingest.
//
// Note the side effect, which no other provider has: there is no persistent
// Facebook stream key, so every call here creates a new live_video. Pressing
// "refresh key" on a Facebook destination starts a new broadcast object rather
// than re-reading an existing one. The Provider interface has nowhere to put
// the resulting id, which is why IngestFor exists and why a caller that wants
// to end the broadcast or push metadata to it should use that instead.
func (f *Facebook) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	b, err := f.IngestFor(ctx, clientID, accessToken, "", IngestOptions{})
	if err != nil {
		return nil, err
	}
	ing := b.Ingest
	return &ing, nil
}

// IngestFor creates a live_video on one target and splits its ingest into the
// URL and stream key polyemesis stores separately.
func (f *Facebook) IngestFor(ctx context.Context, clientID, accessToken, targetRef string, opts IngestOptions) (*Broadcast, error) {
	tgt, err := f.resolveTarget(ctx, accessToken, targetRef)
	if err != nil {
		return nil, err
	}

	// status=LIVE_NOW is what makes this a broadcast rather than a scheduled
	// post. The video only appears once bytes arrive, so creating it ahead of
	// the encoder is safe.
	//
	// overlay_url is deliberately absent: Graph removed it in v24.0 and sending
	// it now is an error rather than a no-op.
	// SCHEDULED_UNPUBLISHED is the opposite trade to LIVE_NOW, and that is the
	// point of it: a LIVE_NOW video is invisible until bytes arrive, whereas a
	// scheduled one is a PUBLIC event page from the moment it is created. That
	// visibility days in advance is the whole feature -- it is what lets people
	// be told about a show before it starts.
	//
	// event_params carries the start time. Facebook accepts at most seven days
	// ahead; the bound is enforced by the caller, which is the only layer that
	// knows the occurrence.
	//
	// A BARE UNIX SCALAR, and the alternative was rejected rather than
	// overlooked. Meta documents event_params two ways: the scheduling guide's
	// own copy-pasteable request sends the scalar
	// (...&event_params=1541539800), while the v26.0 edge reference types it as
	// a structured Live Video Event Parameter object ({start_time, cover})
	// scoped to Live Online Events. They cannot both be the wire format. The
	// literal sample in the guide that describes THIS call is the stronger
	// evidence, so the scalar is what goes out. If a live account ever answers
	// this with a refusal naming event_params, the object is the next thing to
	// try -- and the read-back,
	// GET /<ID>/live_videos?broadcast_status=["SCHEDULED_UNPUBLISHED"], is how
	// to tell which one Facebook actually took.
	params := url.Values{"status": {"LIVE_NOW"}}
	if !opts.ScheduledFor.IsZero() {
		params.Set("status", "SCHEDULED_UNPUBLISHED")
		params.Set("event_params", strconv.FormatInt(opts.ScheduledFor.Unix(), 10))
	}
	// Every field below is sent ONLY when the operator chose it. Facebook treats
	// a present-but-empty parameter as a value, so "leave it alone" has to mean
	// an absent key rather than an empty one.
	//
	// Privacy is applied HERE, at create time, because Facebook documents
	// LIVE_VIDEO__PRIVACY_REQUIRED -- "You need to set a privacy before going
	// live" -- and this is the surface Meta actually describes. It can also be
	// changed afterwards, through UpdateLiveVideoPrivacy, but that path exists
	// despite Graph rather than because of it: the reference documents no
	// Updating section for LiveVideo at all, so that method confirms its own
	// write by reading the value back instead of trusting the POST's status.
	//
	// tgt.kind != fbKindPage is a second, independent condition, not a repeat of
	// the first: a Page broadcast has no personal audience for a value like
	// SELF to apply to, so an operator's chosen privacy is suppressed for every
	// Page target regardless of what they picked. That suppression is silent —
	// nothing here or in internal/api refuses or warns on the combination.
	if opts.Privacy != db.FBPrivacyUnchanged && tgt.kind != fbKindPage {
		params.Set("privacy", fbPrivacyParam(opts.Privacy))
	}
	if len(opts.Crosspost) > 0 {
		enc, err := fbCrosspostParam(opts.Crosspost)
		if err != nil {
			return nil, err
		}
		params.Set("crossposting_actions", enc)
	}
	if opts.DonateCharityID != "" {
		params.Set("donate_button_charity_id", opts.DonateCharityID)
	}
	if opts.BackupIngest {
		params.Set("enable_backup_ingest", "true")
	}

	var created fbLiveVideo
	err = f.post(ctx, tgt.token, "/"+tgt.node+"/live_videos", params, &created)
	if err != nil {
		// fbCreateAdvice rather than fbAdvice: this is the call Meta's go-live
		// eligibility gate refuses, and it refuses it without saying so.
		return nil, fbCreateAdvice(err, "start a Facebook broadcast", f.publishScopes(tgt.kind))
	}
	if created.ID == "" {
		return nil, fmt.Errorf("Facebook accepted the broadcast but returned no live video id")
	}

	// The create response normally carries the ingest already. When it does not
	// — or carries no backups — one read fills the gaps, and its failure is not
	// fatal because whatever we already have may well be enough.
	if created.SecureStreamURL == "" || len(created.SecureStreamSecond) == 0 {
		var full fbLiveVideo
		if err := f.get(ctx, tgt.token, "/"+created.ID,
			url.Values{"fields": {fbLiveVideoFields}}, &full); err == nil {
			mergeLiveVideo(&created, full)
		}
	}

	// Prefer RTMPS. Facebook has required it for years, and the plain rtmp://
	// field is kept only as the fallback for a response that omits the secure
	// one rather than as a thing we would choose.
	primary := firstNonEmpty(created.SecureStreamURL, created.StreamURL)
	if primary == "" {
		return nil, fmt.Errorf("Facebook created live video %s but returned no ingest URL", created.ID)
	}
	server, key, err := splitIngestURL(primary)
	if err != nil {
		return nil, err
	}

	b := &Broadcast{ID: created.ID, Target: tgt.ref, Ingest: Ingest{URL: server, Key: key}}

	backups := created.SecureStreamSecond
	if len(backups) == 0 {
		backups = created.StreamSecondary
	}
	for _, raw := range backups {
		// A backup we cannot parse is dropped, not fatal: the primary is what
		// the stream actually needs.
		if u, k, err := splitIngestURL(raw); err == nil {
			b.Backups = append(b.Backups, Ingest{URL: u, Key: k})
		}
	}
	return b, nil
}

// fbPrivacyParam is Graph's privacy object, which is a JSON document in a query
// parameter rather than a bare value.
func fbPrivacyParam(p db.FacebookPrivacy) string {
	return `{"value":"` + string(p) + `"}`
}

// fbCrosspostParam encodes the crossposting changes Graph documents.
//
// The two actions differ by whether a post is published as the Page. Defaulting
// to the quieter one is deliberate: a share nobody notices is recoverable, and a
// post published as somebody else's Page is not.
func fbCrosspostParam(targets []db.CrosspostTarget) (string, error) {
	type action struct {
		PageID string `json:"page_id"`
		Action string `json:"action"`
	}
	out := make([]action, 0, len(targets))
	for _, t := range targets {
		a := "enable_crossposting"
		if t.CreatePost {
			a = "enable_crossposting_and_create_post"
		}
		out = append(out, action{PageID: t.PageID, Action: a})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// publishScopes names the permissions the failed call actually needed, so the
// advice is not a list of five when only one is missing.
func (f *Facebook) publishScopes(kind fbKind) []string {
	if kind == fbKindPage {
		return []string{"pages_manage_posts", "pages_read_engagement", "pages_show_list"}
	}
	return []string{"publish_video"}
}

func mergeLiveVideo(dst *fbLiveVideo, src fbLiveVideo) {
	dst.StreamURL = firstNonEmpty(dst.StreamURL, src.StreamURL)
	dst.SecureStreamURL = firstNonEmpty(dst.SecureStreamURL, src.SecureStreamURL)
	dst.Status = firstNonEmpty(dst.Status, src.Status)
	dst.Title = firstNonEmpty(dst.Title, src.Title)
	if len(dst.StreamSecondary) == 0 {
		dst.StreamSecondary = src.StreamSecondary
	}
	if len(dst.SecureStreamSecond) == 0 {
		dst.SecureStreamSecond = src.SecureStreamSecond
	}
}

// splitIngestURL splits a Facebook ingest into the two halves polyemesis stores
// separately: the server, which is safe to display, and the stream key, which
// the UI masks. db.Destination.Target() joins them back with a single slash, so
// the split has to be exactly reversible.
//
// The anchor is the last slash of the *path*, not of the whole string, because
// the key arrives with a query string attached (s_bl, s_psm, a signature…) and
// a base64 signature can contain a slash of its own. Splitting on the last
// slash overall would then cut the key in half and produce a server URL nobody
// can publish to.
//
// No error message here ever echoes the URL: everything after /rtmp/ is a
// credential.
func splitIngestURL(raw string) (server, key string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("Facebook returned an empty ingest URL")
	}
	scheme := strings.Index(raw, "://")
	if scheme < 0 {
		return "", "", fmt.Errorf("Facebook returned an ingest URL with no scheme; expected rtmps://…/rtmp/<key>")
	}

	path := raw
	if q := strings.IndexByte(raw, '?'); q >= 0 {
		path = raw[:q]
	}
	slash := strings.LastIndexByte(path, '/')
	// scheme+2 is the second slash of "://" — a URL with no path at all.
	if slash <= scheme+2 {
		return "", "", fmt.Errorf("Facebook returned an ingest URL with no path; expected rtmps://…/rtmp/<key>")
	}
	server, key = raw[:slash], raw[slash+1:]
	if key == "" {
		return "", "", fmt.Errorf("Facebook returned an ingest URL with no stream key on the end")
	}
	return server, key, nil
}

// FacebookLiveVideoID recovers the broadcast id from a stored stream key.
//
// Facebook's key is the live_video id followed by a query string, which means
// the id is already persisted in destinations.stream_key and needs no column of
// its own. Best-effort by design: anything that is not a bare numeric id
// returns empty, and a caller that needs certainty should keep Broadcast.ID
// from IngestFor instead of relying on this.
func FacebookLiveVideoID(streamKey string) string {
	id := strings.TrimSpace(streamKey)
	if q := strings.IndexByte(id, '?'); q >= 0 {
		id = id[:q]
	}
	if id == "" {
		return ""
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return id
}

// --------------------------------------------------------------- metadata

func (f *Facebook) MetadataCaps() MetadataCaps {
	return MetadataCaps{
		// No category: a Facebook live video has no field equivalent to a
		// YouTube category or a Twitch game. Saying so here is what keeps it out
		// of the failure list. Tags ARE accepted: content_tags takes Facebook's
		// own ad-interest ids, resolved from operator-typed words by resolveTags.
		//
		// FieldPrivacy is deliberately absent, unlike YouTube and Twitch's
		// compliance fields: this list describes what the composer's
		// PushMetadata call can accept, and oauth.Metadata carries no Privacy
		// field for it to accept. Advertising it here would claim a composer
		// control that does not exist.
		//
		// It is NOT absent because privacy cannot move. Privacy travels through
		// PushCompliance -- Facebook's own is below in this file -- which an
		// ordinary composer push now calls, so pressing Push re-applies the
		// privacy stored on the destination to a live broadcast. The separation
		// is about which surface the operator sets the field on: it is a
		// destination setting rather than a composer field, so it changes when
		// they change it there, and not by being typed into the composer.
		Fields: []MetadataField{FieldTitle, FieldDescription, FieldTags},
		// Both limits are left at zero — "no published limit". Meta documents no
		// maximum for either field, and inventing one would reject a title the
		// platform would have accepted, which is the restrictive-check mistake
		// this codebase keeps relearning.
		Scope: "publish_video",
	}
}

// PushMetadata edits the live video's title and description.
//
// accountRef is the target ref stored when the account was connected
// ("user:…"/"page:…"), not a live video id: the broadcast to edit is discovered
// from the target, because the operator may have started it from Live Producer
// rather than from here. A caller that recorded Broadcast.ID from IngestFor
// should call UpdateLiveVideo instead — it is one round trip shorter and it
// cannot pick the wrong broadcast.
func (f *Facebook) PushMetadata(ctx context.Context, clientID, accessToken, accountRef string, m Metadata) (*MetadataResult, error) {
	m = m.Trimmed()
	tgt, err := f.resolveTarget(ctx, accessToken, accountRef)
	if err != nil {
		return nil, err
	}

	res := &MetadataResult{}
	if m.Category != "" {
		res.Skipped = append(res.Skipped, FieldCategory)
		res.Warnings = append(res.Warnings,
			"Facebook Live has no category field; set the audience and content tags in Live Producer.")
	}
	// Tags alone are a reason to write. Without them in this condition a push
	// carrying only tags returns here having done nothing, and the composer
	// reports success for a request that never left.
	if m.Title == "" && m.Description == "" && len(m.Tags) == 0 {
		return res, nil
	}

	lv, err := f.currentLiveVideo(ctx, tgt)
	if err != nil {
		return nil, err
	}
	if err := f.writeLiveVideo(ctx, tgt, lv.ID, m, res); err != nil {
		return nil, err
	}

	res.Target = firstNonEmpty(lv.Title, lv.ID)
	if m.Title != "" {
		res.Applied = append(res.Applied, FieldTitle)
		res.Target = m.Title
	}
	if m.Description != "" {
		res.Applied = append(res.Applied, FieldDescription)
	}
	return res, nil
}

// UpdateLiveVideo edits one known broadcast. This is the reliable path for a
// caller holding a Broadcast.ID: it addresses the video directly instead of
// guessing which of the target's broadcasts was meant.
func (f *Facebook) UpdateLiveVideo(ctx context.Context, clientID, accessToken, targetRef, liveVideoID string, m Metadata) (*MetadataResult, error) {
	m = m.Trimmed()
	if strings.TrimSpace(liveVideoID) == "" {
		return nil, fmt.Errorf("no Facebook live video id was recorded for this destination")
	}
	tgt, err := f.resolveTarget(ctx, accessToken, targetRef)
	if err != nil {
		return nil, err
	}

	res := &MetadataResult{Target: liveVideoID}
	if m.Category != "" {
		res.Skipped = append(res.Skipped, FieldCategory)
		res.Warnings = append(res.Warnings,
			"Facebook Live has no category field; set the audience and content tags in Live Producer.")
	}
	// Tags alone are a reason to write. Without them in this condition a push
	// carrying only tags returns here having done nothing, and the composer
	// reports success for a request that never left.
	if m.Title == "" && m.Description == "" && len(m.Tags) == 0 {
		return res, nil
	}
	if err := f.writeLiveVideo(ctx, tgt, liveVideoID, m, res); err != nil {
		return nil, err
	}
	if m.Title != "" {
		res.Applied = append(res.Applied, FieldTitle)
		res.Target = m.Title
	}
	if m.Description != "" {
		res.Applied = append(res.Applied, FieldDescription)
	}
	return res, nil
}

// fbPrivacyReadFields is what the read-back after a privacy push asks for.
// Kept separate from fbLiveVideoFields, which serves the create/ingest path
// and has never needed privacy on it: extending that constant would put a
// field on every ingest read that only this confirmation step uses.
const fbPrivacyReadFields = "id,privacy"

// fbLiveVideoPrivacy is the shape of the read-back UpdateLiveVideoPrivacy
// confirms against. Privacy is a pointer because its ABSENCE has to be
// distinguishable from every named value: Graph documents no update surface
// for LiveVideo at all, so a response that omits the field must read as "not
// confirmed," never as silent agreement with whatever was asked for.
type fbLiveVideoPrivacy struct {
	ID      string `json:"id"`
	Privacy *struct {
		Value string `json:"value"`
	} `json:"privacy"`
}

// ------------------------------------------------- scheduled broadcasts

// Facebook's implementation of ScheduledBroadcaster. The interface moved to
// oauth.go with TargetedProvider -- see the note there.

// ScheduleHorizon is Facebook's own bound, and it is not ours to widen: Graph
// refuses a live_video whose event_params is more than seven days out, at
// create and at reschedule alike.
//
// It constrains far less than it looks like it does. The next occurrence of a
// daily schedule is at most a day away and of a weekly one at most seven days,
// by definition -- only a one-shot schedule can be set beyond this.
func (f *Facebook) ScheduleHorizon() time.Duration { return 7 * 24 * time.Hour }

// RescheduleBroadcast moves an already-created scheduled broadcast to a new
// start time.
//
// POSTs to the live video NODE, not to the /live_videos edge. The edge creates
// a broadcast; the node edits one. Getting that wrong leaves the original event
// page in place with people subscribed to a show that will not happen there --
// which is worse than not moving it, because nothing anywhere says the old page
// is dead.
//
// Facebook's seven-day bound applies here exactly as it does at create, and is
// the caller's to enforce for the same reason: only the caller knows the
// occurrence.
func (f *Facebook) RescheduleBroadcast(ctx context.Context, accessToken, liveVideoID string, at time.Time) error {
	// Refused before any call. An empty id would make this a POST to "/", which
	// Graph answers in a way that reads as success.
	if liveVideoID == "" {
		return fmt.Errorf("reschedule: no live video id")
	}
	// The same bare unix scalar the create sends, for the same reason and with
	// the same fallback if it is ever refused -- see IngestFor. The two
	// spellings must not diverge: a create and a move that disagreed about the
	// wire format would work until the day one of them stopped.
	params := url.Values{"event_params": {strconv.FormatInt(at.Unix(), 10)}}
	var out struct{}
	if err := f.post(ctx, accessToken, "/"+liveVideoID, params, &out); err != nil {
		return fbAdvice(err, "reschedule a Facebook broadcast", nil)
	}
	return nil
}

// ------------------------------------------------ ending and stream health

// fbStatusVOD is what a Facebook broadcast becomes when it ends. Broadcasting
// guide, read 2026-08-16: "This ends your broadcast and saves it as a video on
// demand (VOD)." It is the only value that confirms an end.
const fbStatusVOD = "VOD"

// fbEndReadFields is the read-back after an end. Deliberately not
// fbLiveVideoFields: that constant exists to fetch ingest URLs, and asking for
// a stream key to answer "did this stop" would pull a credential into a call
// that has no use for one.
const fbEndReadFields = "id,status"

// BroadcastEnd is what EndBroadcast observed after asking Facebook to stop.
type BroadcastEnd struct {
	// Status is the status Facebook reported on the read-back. Empty means the
	// read-back failed or carried no status, which is NOT the same as the
	// broadcast still being live and must never be rendered as though it were.
	Status string
	// Ended is true only when Facebook reported VOD. A false here with no error
	// means "Facebook accepted the end and has not yet said it took", which is
	// an ordinary outcome -- the POST succeeded.
	Ended bool
	// Warnings name what was actually seen when Ended is false, in the shape
	// MetadataResult uses, so a caller can render them the same way.
	Warnings []string
}

// EndBroadcast ends one live video and reports what Facebook says it became.
//
// ENDING ON FACEBOOK IS NOT ENDING ON YOUTUBE, and that difference is the whole
// design of this method. Meta's Broadcasting guide, read 2026-08-16, verbatim:
// "To end a broadcast, stop streaming live video data from your encoder to the
// stream URL or send a request to POST /<LIVE_VIDEO_ID>?end_live_video=true."
// Two mechanisms, and one of them is the ABSENCE OF BYTES -- so on Facebook an
// encoder that crashes has already ended the show. The policy YouTube needs,
// where a crash deliberately leaves the broadcast live so a reconnecting
// encoder can rejoin it, has nothing to preserve here and is not imported: by
// the time anything noticed the crash, Facebook had already ended it. This
// method is for a DELIBERATE end -- the operator stopped the show.
//
// A refused POST is an ERROR, unlike the refused privacy push below which is a
// Skipped. The asymmetry is the consequence: a privacy push that fails leaves
// the value chosen at create time already applied, while an end that fails
// leaves a broadcast ON AIR that the operator believes is over.
//
// The end is confirmed by reading the status back, and Ended is reported only
// on VOD. A different status is not an error and not a lie either -- Facebook
// documents that it saves the VOD, not how quickly the node settles, so
// PROCESSING or a stale LIVE is reported as "accepted, not yet confirmed" with
// the value that was seen. Inventing a retry loop around that would mean
// inventing the settling time nobody published.
func (f *Facebook) EndBroadcast(ctx context.Context, accessToken, targetRef, liveVideoID string) (*BroadcastEnd, error) {
	// Refused before any call, for the reason RescheduleBroadcast is: an empty
	// id makes this a POST to "/", which Graph answers in a way that reads as
	// success -- and here that would report a still-live broadcast as ended.
	if strings.TrimSpace(liveVideoID) == "" {
		return nil, fmt.Errorf("no Facebook live video id was recorded for this destination")
	}
	// Resolved rather than posted with the user token: a Page's live video is
	// addressable only with that Page's token, and the failure without it is a
	// permission error on a call the operator is watching for the end of.
	tgt, err := f.resolveTarget(ctx, accessToken, targetRef)
	if err != nil {
		return nil, err
	}

	// The node, not the /live_videos edge, and end_live_video=true is the whole
	// request. Nothing else is sent: this call is not a place to also fix a
	// title, and a second parameter Graph did not expect would fail the end.
	if err := f.post(ctx, tgt.token, "/"+liveVideoID,
		url.Values{"end_live_video": {"true"}}, nil); err != nil {
		return nil, fbAdvice(err, "end the Facebook broadcast", f.publishScopes(tgt.kind))
	}

	out := &BroadcastEnd{}
	var confirm struct {
		Status string `json:"status"`
	}
	getErr := f.get(ctx, tgt.token, "/"+liveVideoID,
		url.Values{"fields": {fbEndReadFields}}, &confirm)
	switch {
	case getErr != nil:
		out.Warnings = append(out.Warnings,
			"Facebook accepted the end but it could not be confirmed: "+getErr.Error())
	case strings.EqualFold(strings.TrimSpace(confirm.Status), fbStatusVOD):
		out.Status, out.Ended = confirm.Status, true
	case strings.TrimSpace(confirm.Status) == "":
		out.Warnings = append(out.Warnings,
			"Facebook accepted the end but the read-back carried no status to confirm it")
	default:
		out.Status = confirm.Status
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"Facebook accepted the end but still reports this broadcast as %s rather than %s",
			confirm.Status, fbStatusVOD))
	}
	return out, nil
}

// FacebookStreamHealthInterval is the floor on how often StreamHealth may be
// called. It is FACEBOOK'S number, not an estimate of one -- Broadcasting
// guide, read 2026-08-16, verbatim: "Stream health data refreshes every 2
// seconds, so limit queries to no more than once every 2 seconds. A stream
// timeout will be detected and reported after 4 seconds of no data being
// received."
//
// A published floor may be encoded; an unpublished ceiling may not, which is
// why YouTube's concurrency limit is nowhere in this tree and this is here.
//
// It is not enforced inside StreamHealth, and that is deliberate: enforcement
// means either sleeping inside a caller's call or keeping a last-polled clock
// per broadcast in a provider that is otherwise stateless. The poll loop owns
// its own pacing; this constant is what it paces against, so the number is
// written down once instead of at every call site.
const FacebookStreamHealthInterval = 2 * time.Second

// FacebookStreamTimeout is how long Facebook itself waits before it reports a
// stream as timed out -- the second sentence of the same quote. It is here so a
// caller deciding "how long may a health read stay empty before the encoder is
// gone" reads Facebook's four seconds instead of inventing a number, which is
// the mistake this codebase repeats most.
const FacebookStreamTimeout = 4 * time.Second

// IngestStreamHealth is one ingest stream's health as Facebook reports it.
type IngestStreamHealth struct {
	// ID identifies which ingest stream this describes. A broadcast created
	// with IngestOptions.BackupIngest has more than one.
	ID string
	// Health carries stream_health's numeric fields under FACEBOOK'S OWN names
	// rather than fields named here.
	//
	// A map because the evidence establishes that stream_health "carries
	// bitrates and frame rates" and does not name the keys: the node reference
	// that would is the one that returns a real 404. Named Go fields would be a
	// guess at spellings, and a guess that is wrong reads back as zero on a
	// healthy stream -- a dropped-frames pane showing 0 for a field we misspelt
	// is worse than one showing nothing.
	//
	// An absent measurement is an ABSENT KEY, never a zero: a map lookup's
	// second return is what tells those apart, the same reason stats carries a
	// viewer count as a pointer.
	Health map[string]float64
	// Unparsed names the stream_health fields that were not numbers, sorted.
	// They are recorded rather than dropped because a field polyemesis cannot
	// read looks exactly like a field Facebook did not send, and one of those
	// is a bug here.
	Unparsed []string
}

// fbIngestStream is one entry of the ingest_streams read.
type fbIngestStream struct {
	ID string `json:"id"`
	// map[string]any, not a typed struct, for the reason IngestStreamHealth.Health
	// is a map: the field names are not established.
	StreamHealth map[string]any `json:"stream_health"`
}

// fbIngestStreams tolerates both spellings of a list-valued Graph field: the
// {"data": [...]} envelope this file already decodes on /me/accounts and
// /live_videos, and a bare array.
//
// Tolerance rather than a choice because nothing reachable settles which one
// ingest_streams uses when it is read as a FIELD of the node -- the LiveVideo
// node reference 404s, which is the same absence that keeps the health field
// names out of this file. Guessing one spelling would turn a healthy stream
// into an empty pane, and the two shapes cannot be confused for each other.
type fbIngestStreams struct {
	Streams []fbIngestStream
}

func (s *fbIngestStreams) UnmarshalJSON(b []byte) error {
	var env struct {
		Data []fbIngestStream `json:"data"`
	}
	// An array does not unmarshal into a struct, so this branch cannot swallow
	// the bare form; null unmarshals into it as no streams, which is correct.
	if err := json.Unmarshal(b, &env); err == nil {
		s.Streams = env.Data
		return nil
	}
	var bare []fbIngestStream
	if err := json.Unmarshal(b, &bare); err != nil {
		return err
	}
	s.Streams = bare
	return nil
}

// StreamHealth reads what Facebook's ingest sees of the encoder feed.
//
// Facebook is the only platform here that publishes this: Twitch's Helix
// reference carries no bitrate, framerate or dropped-frame field at all, so
// this is not a gap in the other providers to be filled by symmetry.
//
// AN EMPTY RESULT IS NOT AN ERROR AND NOT A FAILURE. A scheduled broadcast has
// no ingest yet, an ended one has none any more, and a live one whose encoder
// went quiet reports nothing until Facebook's own four-second timeout fires --
// see FacebookStreamTimeout. All three are "no ingest stream to describe", and
// turning them into an error would make a health pane shout during the pause
// between clicking Go Live and the first byte arriving.
//
// Callers must respect FacebookStreamHealthInterval between calls.
func (f *Facebook) StreamHealth(ctx context.Context, accessToken, targetRef, liveVideoID string) ([]IngestStreamHealth, error) {
	if strings.TrimSpace(liveVideoID) == "" {
		return nil, fmt.Errorf("no Facebook live video id was recorded for this destination")
	}
	tgt, err := f.resolveTarget(ctx, accessToken, targetRef)
	if err != nil {
		return nil, err
	}

	var read struct {
		IngestStreams fbIngestStreams `json:"ingest_streams"`
	}
	if err := f.get(ctx, tgt.token, "/"+liveVideoID,
		url.Values{"fields": {"ingest_streams"}}, &read); err != nil {
		return nil, fbAdvice(err, "read the Facebook stream health", f.publishScopes(tgt.kind))
	}

	out := make([]IngestStreamHealth, 0, len(read.IngestStreams.Streams))
	for _, s := range read.IngestStreams.Streams {
		h := IngestStreamHealth{ID: s.ID}
		for name, v := range s.StreamHealth {
			n, ok := v.(float64)
			if !ok {
				h.Unparsed = append(h.Unparsed, name)
				continue
			}
			if h.Health == nil {
				h.Health = make(map[string]float64, len(s.StreamHealth))
			}
			h.Health[name] = n
		}
		// Sorted because map iteration order is random, and a warning list that
		// reorders itself between two identical reads reads as a change.
		sort.Strings(h.Unparsed)
		out = append(out, h)
	}
	return out, nil
}

// UpdateLiveVideoPrivacy changes a broadcast's audience after it is already
// live -- the convenience that avoids deleting the broadcast to redo the
// value IngestFor already applied at create time.
//
// Graph documents no update surface for LiveVideo at all, so a 200 from the
// POST proves Facebook accepted the request, not that the field changed.
// Reporting Applied on that basis would tell an operator their broadcast is
// friends-only while it is public, which on this field cannot be taken back
// once someone has seen it. So the write is confirmed by reading the value
// back, and Applied is reported ONLY when Facebook returns exactly what was
// asked for. A different value, an absent field, an unreadable response, or
// an outright refusal of the POST are all Skipped with a warning naming what
// was actually seen -- Skipped rather than an error, because the value
// stored at create time is already live and a failed push here costs a
// convenience, not the setting.
func (f *Facebook) UpdateLiveVideoPrivacy(ctx context.Context, clientID, accessToken, targetRef, liveVideoID string, p db.FacebookPrivacy) (*MetadataResult, error) {
	if !db.ValidFacebookPrivacy(p) {
		return nil, fmt.Errorf("unknown Facebook privacy %q", p)
	}
	if strings.TrimSpace(liveVideoID) == "" {
		return nil, fmt.Errorf("no Facebook live video id was recorded for this destination")
	}
	res := &MetadataResult{Target: liveVideoID}

	// Read from the ref alone, before any request: a Page broadcast is public
	// by nature and has no personal audience for a value like SELF to apply
	// to, the same reasoning IngestFor uses to suppress privacy at create
	// time. Resolving the full target here would spend a round trip on an
	// answer this branch never uses.
	if kind, _ := parseTargetRef(targetRef); kind == fbKindPage {
		res.Skipped = append(res.Skipped, FieldPrivacy)
		res.Warnings = append(res.Warnings,
			"Facebook Pages have no personal audience; this broadcast's privacy was not changed.")
		return res, nil
	}

	tgt, err := f.resolveTarget(ctx, accessToken, targetRef)
	if err != nil {
		return nil, err
	}

	if postErr := f.post(ctx, tgt.token, "/"+liveVideoID,
		url.Values{"privacy": {fbPrivacyParam(p)}}, nil); postErr != nil {
		res.Skipped = append(res.Skipped, FieldPrivacy)
		res.Warnings = append(res.Warnings, "Facebook refused the privacy change: "+postErr.Error())
		return res, nil
	}

	var confirm fbLiveVideoPrivacy
	getErr := f.get(ctx, tgt.token, "/"+liveVideoID,
		url.Values{"fields": {fbPrivacyReadFields}}, &confirm)
	switch {
	case getErr != nil:
		res.Skipped = append(res.Skipped, FieldPrivacy)
		res.Warnings = append(res.Warnings,
			"Facebook accepted the privacy change but it could not be confirmed: "+getErr.Error())
	case confirm.Privacy == nil:
		res.Skipped = append(res.Skipped, FieldPrivacy)
		res.Warnings = append(res.Warnings,
			"Facebook accepted the privacy change but the read-back carried no privacy value to confirm it")
	case confirm.Privacy.Value == string(p):
		res.Applied = append(res.Applied, FieldPrivacy)
	default:
		res.Skipped = append(res.Skipped, FieldPrivacy)
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"Facebook still reports this broadcast's privacy as %s, not the requested %s; the change was not confirmed",
			confirm.Privacy.Value, p))
	}
	return res, nil
}

// PushCompliance writes Facebook's privacy audience to a broadcast already
// live.
//
// This is the only compliance field Facebook has, and it goes through the
// confirmed path -- UpdateLiveVideoPrivacy -- rather than a second, unconfirmed
// one: Graph documents no update surface for LiveVideo at all, so a claim of
// Applied that was not read back would tell an operator their broadcast is
// friends-only while it is public.
func (f *Facebook) PushCompliance(ctx context.Context, clientID, accessToken string, tgt ComplianceTarget, c db.Compliance) (*MetadataResult, error) {
	if c.FacebookPrivacy == db.FBPrivacyUnchanged {
		return &MetadataResult{}, nil
	}
	id := FacebookLiveVideoID(tgt.StreamKey)
	if id == "" {
		// Never an error: a destination whose stream key was typed by hand, or
		// has not gone live since this existed, legitimately has no Facebook
		// broadcast id recorded.
		return &MetadataResult{
			Skipped:  []MetadataField{FieldPrivacy},
			Warnings: []string{"this destination has no Facebook broadcast recorded, so its privacy could not be changed"},
		}, nil
	}
	return f.UpdateLiveVideoPrivacy(ctx, clientID, accessToken, tgt.AccountRef, id, c.FacebookPrivacy)
}

func (f *Facebook) writeLiveVideo(ctx context.Context, tgt *fbTarget, id string, m Metadata, res *MetadataResult) error {
	params := url.Values{}
	// Only what the operator typed is sent. An empty field means "leave what is
	// there", and posting an empty title would blank a live broadcast's title.
	if m.Title != "" {
		params.Set("title", m.Title)
	}
	if m.Description != "" {
		params.Set("description", m.Description)
	}

	// Tag words become ids, and a failure here is REPORTED rather than fatal.
	// /search?type=adinterest is an ads-surface endpoint that may not be
	// reachable with publish_video -- unverified, because this repo has no live
	// Facebook account to check it against -- and a title change seconds before
	// air must not be lost to a tag lookup.
	if len(m.Tags) > 0 {
		ids, warns, err := f.resolveTags(ctx, tgt, m.Tags)
		switch {
		case err != nil:
			res.Skipped = append(res.Skipped, FieldTags)
			res.Warnings = append(res.Warnings,
				"Facebook would not search for tags, so none were set: "+err.Error())
		case len(ids) > 0:
			b, mErr := json.Marshal(ids)
			if mErr != nil {
				return mErr
			}
			params.Set("content_tags", string(b))
			res.Applied = append(res.Applied, FieldTags)
		}
		res.Warnings = append(res.Warnings, warns...)
	}

	err := f.post(ctx, tgt.token, "/"+id, params, nil)
	if err != nil {
		return fbAdvice(err, "edit the Facebook broadcast", f.publishScopes(tgt.kind))
	}
	return nil
}

// resolveTags turns operator words into Facebook's ad-interest ids, returning
// one warning per word that matched nothing.
//
// An unmatched word is a WARNING NAMING THE WORD, never a silent drop: a tag
// that disappears without comment looks exactly like one that worked.
func (f *Facebook) resolveTags(ctx context.Context, tgt *fbTarget, words []string) ([]string, []string, error) {
	var ids, warns []string
	for _, w := range words {
		var found struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		err := f.get(ctx, tgt.token, "/search",
			url.Values{"type": {"adinterest"}, "q": {w}, "limit": {"1"}}, &found)
		if err != nil {
			return nil, nil, err
		}
		if len(found.Data) == 0 {
			warns = append(warns, fmt.Sprintf("no Facebook interest matches %q, so it was not set as a tag", w))
			continue
		}
		ids = append(ids, found.Data[0].ID)
	}
	return ids, warns, nil
}

// fbLiveRank orders a target's broadcasts: whatever is on air, then whatever is
// staged, and never a finished VOD. Editing last week's broadcast because it
// sorted first is the most embarrassing outcome available here.
func fbLiveRank(status string) int {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "LIVE", "LIVE_NOW":
		return 0
	case "UNPUBLISHED", "SCHEDULED_UNPUBLISHED", "SCHEDULED_LIVE":
		return 1
	case "":
		// Graph omits status on some reads. Unknown is not the same as finished,
		// so it stays eligible and sorts last.
		return 2
	default: // VOD, PROCESSING, SCHEDULED_CANCELED, anything Meta adds later
		return -1
	}
}

// currentLiveVideo picks the ONE broadcast on this target that a caller holding
// no id can safely have meant, and REFUSES WHEN THERE IS MORE THAN ONE. #725.
//
// It used to take the newest at the best rank. That is right when the target
// carries a single broadcast and wrong in the way fbLiveRank's own comment
// worries about -- "editing last week's broadcast because it sorted first is
// the most embarrassing outcome available here" -- except the remaining case is
// worse than last week's: two broadcasts live at once, and a title written onto
// the wrong one is published to somebody's viewers under a heading meant for
// another stream.
//
// A tie at the best rank is not a preference to resolve. It is a question this
// process cannot answer: nothing on a live_video says polyemesis started it,
// and a target may perfectly well carry a broadcast some other tool is running.
// So the tie is reported, with both titles, and the caller is told to address
// the video directly -- which UpdateLiveVideo exists to do.
//
// THE SINGLE-BROADCAST CASE IS UNCHANGED, which is every ordinary install: one
// live video on the target, one answer, no new refusal.
func (f *Facebook) currentLiveVideo(ctx context.Context, tgt *fbTarget) (*fbLiveVideo, error) {
	var list struct {
		Data []fbLiveVideo `json:"data"`
	}
	err := f.get(ctx, tgt.token, "/"+tgt.node+"/live_videos",
		url.Values{"fields": {"id,status,title"}, "limit": {"25"}}, &list)
	if err != nil {
		return nil, fbAdvice(err, "list Facebook broadcasts", f.publishScopes(tgt.kind))
	}

	bestRank := -1
	var tied []fbLiveVideo
	for _, lv := range list.Data {
		r := fbLiveRank(lv.Status)
		if r < 0 || lv.ID == "" {
			continue
		}
		switch {
		case bestRank < 0 || r < bestRank:
			bestRank, tied = r, []fbLiveVideo{lv}
		case r == bestRank:
			tied = append(tied, lv)
		}
	}
	if len(tied) == 0 {
		return nil, fmt.Errorf("this Facebook target has no live or staged broadcast to update; " +
			"start the stream (polyemesis creates the broadcast when it fetches the ingest) and push again")
	}
	if len(tied) > 1 {
		return nil, fmt.Errorf("this Facebook target has %d broadcasts at the same status "+
			"(%s), and nothing on a live video says which one polyemesis is publishing to -- "+
			"another tool may be running one of them. Refusing rather than guessing: end the "+
			"one you are not using, or push to this destination while only one is live",
			len(tied), fbNameList(tied))
	}
	return &tied[0], nil
}

// fbNameList renders the tied broadcasts for the refusal above, so an operator
// can go and look at the two rather than being told only that there are two.
func fbNameList(vids []fbLiveVideo) string {
	names := make([]string, 0, len(vids))
	for _, v := range vids {
		names = append(names, firstNonEmpty(v.Title, v.ID))
	}
	return strings.Join(names, ", ")
}

// AdoptLiveVideo finds the broadcast a HAND-PASTED key is publishing to. #725.
//
// A destination whose key came from the Facebook API carries the live-video id
// IN THE KEY -- FacebookLiveVideoID reads it off -- so chat, metadata and End
// broadcast have an id without asking anybody. A key pasted from Live Producer
// does not: a persistent key is `FB-<numbers>-<n>-<random>`, which is not an
// id, so those three features had nothing to attach to and said so.
//
// They do not have to. Going live with a persistent key creates a live video on
// the same target the connected account can see, and currentLiveVideo already
// knows how to find it. What makes that safe here rather than a guess is
// Facebook's own limit, stated on the persistent key itself: ONE LIVE VIDEO AT
// A TIME. So while a persistent key is publishing, "the broadcast on this
// target" is unambiguous -- and on the occasions it is not, currentLiveVideo
// now refuses rather than picking.
//
// Adoption is therefore exactly as safe as that refusal, which is why the two
// changes belong in one place.
func (f *Facebook) AdoptLiveVideo(ctx context.Context, accessToken, targetRef string) (string, error) {
	tgt, err := f.resolveTarget(ctx, accessToken, targetRef)
	if err != nil {
		return "", err
	}
	lv, err := f.currentLiveVideo(ctx, tgt)
	if err != nil {
		return "", err
	}
	return lv.ID, nil
}

// --------------------------------------------------------------- transport

// get and post keep the access token in the Authorization header rather than in
// Graph's ?access_token= parameter. Graph accepts both, and the header is the
// one that keeps a token out of the endpoint string that statusError carries
// into every error message and log line.
//
// They are methods on *Facebook rather than free functions, and that is not a
// style preference. As free functions they read the fbGraphBase package var,
// which meant the provider's own graphBase field -- the thing that looked like
// its test seam -- redirected the credential check and nothing else. Every
// caller was already a *Facebook method, so the receiver costs nothing and
// makes it impossible to add a Graph call that ignores the instance's base.
func (f *Facebook) get(ctx context.Context, accessToken, path string, q url.Values, out any) error {
	endpoint := f.graphEndpoint() + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	return requestJSON(ctx, http.MethodGet, endpoint, accessToken, nil, nil, out)
}

// post sends its parameters in the query string, which is the form Meta's own
// documentation uses for these edges (POST /me/live_videos?status=LIVE_NOW),
// rather than as a JSON body.
func (f *Facebook) post(ctx context.Context, accessToken, path string, params url.Values, out any) error {
	endpoint := f.graphEndpoint() + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	return requestJSON(ctx, http.MethodPost, endpoint, accessToken, nil, nil, out)
}

// graphError is Meta's error envelope. Every field here is server-authored
// text; none of it carries credentials.
type graphError struct {
	Message   string `json:"message"`
	Type      string `json:"type"`
	Code      int    `json:"code"`
	Subcode   int    `json:"error_subcode"`
	UserTitle string `json:"error_user_title"`
	UserMsg   string `json:"error_user_msg"`
}

func decodeGraphError(body string) (graphError, bool) {
	var env struct {
		Error graphError `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return graphError{}, false
	}
	if env.Error.Message == "" && env.Error.Code == 0 {
		return graphError{}, false
	}
	return env.Error, true
}

// fbAdvice turns Meta's numbered errors into the one instruction that fixes
// them. Anything it does not recognise is returned untouched: a wrong guess
// about what an error means sends the operator somewhere useless, and Meta's
// own wording is at least accurate.
func fbAdvice(err error, what string, scopes []string) error {
	se, ok := err.(*statusError)
	if !ok {
		return err
	}
	ge, ok := decodeGraphError(se.payload())
	if !ok {
		return err
	}
	msg := strings.ToLower(ge.Message + " " + ge.UserMsg + " " + ge.UserTitle)

	// App Review first: it usually arrives as a permission error too, and
	// "grant the permission" is useless advice when the permission cannot be
	// granted until Meta approves the app.
	for _, marker := range []string{
		"app review", "advanced access", "standard access",
		"development mode", "not been approved", "must be approved",
		"not approved for",
	} {
		if strings.Contains(msg, marker) {
			return fmt.Errorf("Facebook would not let polyemesis %s because the Meta app has not been "+
				"approved for it. Live video publishing needs Advanced Access to %s, which means "+
				"submitting the app for App Review in the Meta App Dashboard. Facebook said: %s",
				what, strings.Join(scopes, ", "), metaMessage(ge))
		}
	}

	// 190 is the whole token family: expired, invalidated by a password change,
	// or de-authorised. All three have the same cure.
	if ge.Code == 190 {
		return fmt.Errorf("Facebook rejected the access token, so polyemesis could not %s. "+
			"Facebook tokens expire (about 60 days) and are invalidated by a password change or by "+
			"removing the app. Reconnect the Facebook account in Settings → Platforms. Facebook said: %s",
			what, metaMessage(ge))
	}

	// 200/10/3 are the permission codes; the OAuthException fallback catches the
	// ones Meta has not numbered consistently.
	if ge.Code == 200 || ge.Code == 10 || ge.Code == 3 ||
		(ge.Type == "OAuthException" && strings.Contains(msg, "permission")) {
		return fmt.Errorf("Facebook refused to let polyemesis %s for lack of a permission. "+
			"This needs %s — reconnect the account in Settings → Platforms and accept every permission "+
			"on the consent screen (Facebook lets you decline them one at a time). Facebook said: %s",
			what, strings.Join(scopes, ", "), metaMessage(ge))
	}

	return err
}

// fbEligibilityNote is the pair of account facts Meta requires of anybody going
// live, quoted from the Live Video API overview (read 2026-08-16; in force
// since 2024-06-10) and already recorded in this repository at
// docs/roadmap/DESTINATION-SETTINGS.md and in internal/api/preannounce.go.
//
// It is APPENDED to a refusal, never used to explain one, and that distinction
// is the entire point of it. Neither requirement is a permission or a scope, so
// an operator can hold every scope, a valid token and a correct stream key and
// still be refused -- and Graph names neither in the error it sends back. There
// is no code, subcode or message marker that identifies this cause. If there
// were, this would be a branch of fbAdvice beside the 190 one and it would say
// "this IS why"; because there is not, it says "check this" and says so out
// loud. Asserting the diagnosis would be the more useful message exactly as
// often as it would be a lie, and an operator sent to count followers over a
// crossposting typo loses more than the sentence saved.
//
// The two numbers are Meta's, not ours, and neither is a threshold this code
// checks: nothing here counts a follower or measures an account's age. Graph
// declines to report either on the surfaces we call, so there is nothing to
// compare against and no version of this that could refuse the create itself.
//
// Wording note: "60 days" appears twice in this file for two unrelated reasons
// -- an account's minimum age here, a token's approximate lifetime in the 190
// branch of fbAdvice. fbCreateAdvice keeps them out of the same message.
const fbEligibilityNote = "If the account, its permissions and the stream key all look correct, " +
	"check the two things Facebook requires of the ACCOUNT before it may go live at all -- " +
	"neither of them is a permission, and this error names neither: the Facebook account must " +
	"be at least 60 days old, and the Facebook Page or professional-mode profile must have at " +
	"least 100 followers. Meta has required both since 2024-06-10. This is a possibility to " +
	"rule out in thirty seconds, not a diagnosis: the refusal above may have nothing to do " +
	"with either."

// fbCreateAdvice is fbAdvice plus fbEligibilityNote, for the one call that
// creates a broadcast.
//
// ONLY the create gets it. Editing, listing and rescheduling all act on a
// live_video that already exists, and its existence is proof the account was
// eligible when it was made -- appending the note there would send an operator
// to count followers over a failure that cannot be about follower count.
//
// The note is withheld unless Facebook itself REFUSED, because "the platform
// said no" is the only premise the sentence rests on:
//
//   - a non-*statusError never reached Graph. A dialled-wrong host, a cut
//     connection or a cancelled context is a transport failure, and no account
//     property can be why.
//   - a 5xx is Meta FAILING, not Meta refusing. Envelope or not, an eligibility
//     gate does not answer 500, and the cure for one of these is to try again.
//   - a body that is not Meta's error envelope came from somebody else -- a
//     proxy, a load balancer, an HTML error page -- so we do not know the Live
//     Video API was reached at all.
//   - code 190 is the token, which fbAdvice has already diagnosed exactly and
//     which has a one-button cure. Appending a hedge to a message that is
//     certain would make the certain part read as a guess too.
//
// The note is APPENDED to fbAdvice's result rather than replacing it, so the
// permission or App Review instruction an operator can actually act on stays
// first and the hedge stays last. %w rather than %v because it costs nothing
// and keeps whatever chain fbAdvice left behind -- which is the original
// *statusError on the pass-through path, and only the formatted message on the
// mapped ones, since fbAdvice's own branches do not wrap.
func fbCreateAdvice(err error, what string, scopes []string) error {
	advised := fbAdvice(err, what, scopes)
	se, ok := err.(*statusError)
	if !ok || se.Status < 400 || se.Status >= 500 {
		return advised
	}
	// payload(), never Body. Body is truncated to 300 characters for display and
	// a realistic Meta refusal is longer, so parsing it fails, `ok` is false,
	// and this returns WITHOUT the note -- withholding it from the exact
	// refusal it was written for. This call site arrived on a branch that
	// forked before the fix in fbAdvice and reintroduced the bug on merge.
	ge, ok := decodeGraphError(se.payload())
	if !ok || ge.Code == 190 {
		return advised
	}
	return fmt.Errorf("%w\n\n%s", advised, fbEligibilityNote)
}

// metaMessage prefers the user-facing text when Meta supplies one, because it
// is the half of the envelope written for a human.
func metaMessage(ge graphError) string {
	return firstNonEmpty(strings.TrimSpace(ge.UserMsg), strings.TrimSpace(ge.Message))
}

// CheckCredentials proves the pair through Facebook's app-access-token
// endpoint. Note the shape: this one is a GET with query parameters, where
// Twitch and Kick both POST a form. Reusing postForm here would send a request
// Facebook answers with 400 regardless of whether the credentials are good,
// which would report every correct pair as rejected.
func (f *Facebook) CheckCredentials(ctx context.Context, clientID, clientSecret string) error {
	q := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"client_credentials"},
	}
	endpoint := f.graphEndpoint() + "/oauth/access_token?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return classifyCheckError(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// tokenStatusError, not a formatted error: classifyCheckError needs the
		// numeric code to tell a 5xx outage from a 4xx refusal, and Facebook
		// takes this GET path instead of postForm precisely because it cannot
		// share that function's body -- it must still share its error type so
		// all three providers classify identically.
		return classifyCheckError(&tokenStatusError{code: resp.StatusCode, body: snippet(body)})
	}
	return nil
}
