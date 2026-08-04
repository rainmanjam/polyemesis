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
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// fbGraphBase and fbDialogBase are vars so tests can point the provider at a
// stub. Nothing at runtime rewrites them.
//
// The version is pinned rather than left off: an unversioned Graph call follows
// whatever Meta has made current, and a broadcast that starts working
// differently on a Tuesday is not a failure mode worth having. v24.0 is also
// the first version that removed overlay_url, which we deliberately never send.
var (
	fbGraphBase  = "https://graph.facebook.com/v24.0"
	fbDialogBase = "https://www.facebook.com/v24.0/dialog/oauth"
)

// Facebook implements Facebook Login plus the Live Video API.
type Facebook struct {
	// graphBase overrides https://graph.facebook.com/v24.0. Empty in production.
	graphBase string
}

func (f *Facebook) graphEndpoint() string {
	if f.graphBase != "" {
		return f.graphBase
	}
	return fbGraphBase
}

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
	return fbDialogBase + "?" + q.Encode()
}

// fbTokenURL is where both the code exchange and the long-lived upgrade go.
func fbTokenURL() string { return fbGraphBase + "/oauth/access_token" }

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
	tok, err := postForm(ctx, fbTokenURL(), url.Values{
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
	tok, err := postForm(ctx, fbTokenURL(), url.Values{
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

// BroadcastTarget is one place a single connected account may publish to.
//
// This and TargetedProvider live in this file because Facebook is the only
// platform that has ever needed them; if a second one appears they belong in
// oauth.go next to Provider.
type BroadcastTarget struct {
	// Ref is what gets stored in PlatformAccount.AccountRef and handed back to
	// AccountFor/IngestFor. See parseTargetRef for the spellings.
	Ref string `json:"ref"`
	// Kind is "user" or "page", so the UI can group and badge them.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Category is the Page's own category, empty for a profile. It is the one
	// thing that distinguishes two Pages with similar names in a dropdown.
	Category string `json:"category,omitempty"`
}

// Broadcast is an ingest plus the platform's identifier for the broadcast
// object that issued it.
//
// The ingest fields carry a stream key, so they are json:"-": nothing should
// ever serialise this struct outward, and if something does, it must not be the
// key that leaks. ID and Target are safe to persist and to show.
type Broadcast struct {
	// ID is Facebook's live_video id. It is the handle for ending the
	// broadcast, editing its metadata and reading its comments, so a caller
	// that discards it has to create a new broadcast to get another one.
	ID string `json:"id"`
	// Target is the ref this was created on, which is what makes the id
	// addressable later: a Page's live video needs the Page's token.
	Target string `json:"target"`
	Ingest Ingest `json:"-"`
	// Backups are the platform's secondary ingest endpoints, for a redundant
	// encoder feed. Exposed even though nothing consumes them yet, because they
	// arrive in the same response and re-fetching them means creating a second
	// broadcast.
	Backups []Ingest `json:"-"`
}

// IngestOptions carries what a platform needs when the broadcast is CREATED,
// which is not the same set the composer pushes afterwards.
//
// A struct rather than more parameters because the create-time surface is going
// to grow: scheduling (event_params) and backup ingest both land here, and three
// signature changes to one interface is three chances to miss a call site. The
// zero value sends nothing, which is what every caller without a destination in
// hand passes.
type IngestOptions struct {
	Privacy         db.FacebookPrivacy
	Crosspost       []db.CrosspostTarget
	DonateCharityID string
	// BackupIngest asks Facebook to provision a secondary ingest endpoint, so
	// a redundant feed can be published alongside the primary.
	//
	// Whether the secondary URLs come back WITHOUT this is not established --
	// our own fixture returns them unconditionally, and a fixture is not
	// evidence about Meta. So a caller must handle an empty Backups even when
	// it asked, rather than treating the request as a guarantee.
	BackupIngest bool
	// ScheduledFor makes this a SCHEDULED_UNPUBLISHED broadcast at that
	// instant rather than a LIVE_NOW one, which is what gives a show a
	// Facebook event page before it starts.
	//
	// Zero means live now. That is what every existing caller passes and what
	// they keep doing -- turning those into scheduled creates would produce
	// broadcasts that never go live.
	//
	// Facebook accepts a start time at most SEVEN DAYS ahead and that bound is
	// not ours to widen. It is enforced by the caller, which is the only layer
	// that knows the occurrence; this struct carries whatever it is given.
	ScheduledFor time.Time
}

// TargetedProvider is the optional capability for a platform where one
// connected login can publish to more than one destination. Discover it with
// TargetsFor; never type-assert Provider at a call site, because "absent" is
// the answer for every other platform and has to be handled once.
type TargetedProvider interface {
	Provider
	// Targets lists everywhere this token may publish. It reports an error only
	// when the identity itself cannot be read: a token that cannot see Pages
	// returns the profile alone, because that is a legitimate connection rather
	// than a failure.
	Targets(ctx context.Context, clientID, accessToken string) ([]BroadcastTarget, error)
	// AccountFor identifies one chosen target, for storing as a connected
	// account. An empty targetRef means the default.
	AccountFor(ctx context.Context, clientID, accessToken, targetRef string) (*Account, error)
	// IngestFor creates (or fetches) the ingest for one target and returns the
	// broadcast object behind it. opts carries the create-time fields a stored
	// destination may have chosen; its zero value sends none of them.
	IngestFor(ctx context.Context, clientID, accessToken, targetRef string, opts IngestOptions) (*Broadcast, error)
}

// TargetsFor returns the multi-target capability for a platform, or false when
// that platform has none. Mirrors MetadataFor.
func TargetsFor(p db.Platform) (TargetedProvider, bool) {
	pr, ok := Providers()[p]
	if !ok {
		return nil, false
	}
	tp, ok := pr.(TargetedProvider)
	return tp, ok
}

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
	if err := fbGet(ctx, accessToken, "/me", url.Values{"fields": {"id,name"}}, &out); err != nil {
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
	err := fbGet(ctx, accessToken, "/me/accounts",
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
	err = fbPost(ctx, tgt.token, "/"+tgt.node+"/live_videos", params, &created)
	if err != nil {
		return nil, fbAdvice(err, "start a Facebook broadcast", f.publishScopes(tgt.kind))
	}
	if created.ID == "" {
		return nil, fmt.Errorf("Facebook accepted the broadcast but returned no live video id")
	}

	// The create response normally carries the ingest already. When it does not
	// — or carries no backups — one read fills the gaps, and its failure is not
	// fatal because whatever we already have may well be enough.
	if created.SecureStreamURL == "" || len(created.SecureStreamSecond) == 0 {
		var full fbLiveVideo
		if err := fbGet(ctx, tgt.token, "/"+created.ID,
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
	params := url.Values{"event_params": {strconv.FormatInt(at.Unix(), 10)}}
	var out struct{}
	if err := fbPost(ctx, accessToken, "/"+liveVideoID, params, &out); err != nil {
		return fbAdvice(err, "reschedule a Facebook broadcast", nil)
	}
	return nil
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

	if postErr := fbPost(ctx, tgt.token, "/"+liveVideoID,
		url.Values{"privacy": {fbPrivacyParam(p)}}, nil); postErr != nil {
		res.Skipped = append(res.Skipped, FieldPrivacy)
		res.Warnings = append(res.Warnings, "Facebook refused the privacy change: "+postErr.Error())
		return res, nil
	}

	var confirm fbLiveVideoPrivacy
	getErr := fbGet(ctx, tgt.token, "/"+liveVideoID,
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

	err := fbPost(ctx, tgt.token, "/"+id, params, nil)
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
		err := fbGet(ctx, tgt.token, "/search",
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

func (f *Facebook) currentLiveVideo(ctx context.Context, tgt *fbTarget) (*fbLiveVideo, error) {
	var list struct {
		Data []fbLiveVideo `json:"data"`
	}
	err := fbGet(ctx, tgt.token, "/"+tgt.node+"/live_videos",
		url.Values{"fields": {"id,status,title"}, "limit": {"25"}}, &list)
	if err != nil {
		return nil, fbAdvice(err, "list Facebook broadcasts", f.publishScopes(tgt.kind))
	}

	// Graph returns this edge newest-first, so the first acceptable entry at the
	// best rank is the right one.
	best := -1
	for i, lv := range list.Data {
		r := fbLiveRank(lv.Status)
		if r < 0 || lv.ID == "" {
			continue
		}
		if best < 0 || r < fbLiveRank(list.Data[best].Status) {
			best = i
		}
	}
	if best < 0 {
		return nil, fmt.Errorf("this Facebook target has no live or staged broadcast to update; " +
			"start the stream (polyemesis creates the broadcast when it fetches the ingest) and push again")
	}
	lv := list.Data[best]
	return &lv, nil
}

// --------------------------------------------------------------- transport

// fbGet and fbPost keep the access token in the Authorization header rather
// than in Graph's ?access_token= parameter. Graph accepts both, and the header
// is the one that keeps a token out of the endpoint string that statusError
// carries into every error message and log line.
func fbGet(ctx context.Context, accessToken, path string, q url.Values, out any) error {
	endpoint := fbGraphBase + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	return requestJSON(ctx, http.MethodGet, endpoint, accessToken, nil, nil, out)
}

// fbPost sends its parameters in the query string, which is the form Meta's own
// documentation uses for these edges (POST /me/live_videos?status=LIVE_NOW),
// rather than as a JSON body.
func fbPost(ctx context.Context, accessToken, path string, params url.Values, out any) error {
	endpoint := fbGraphBase + path
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
	ge, ok := decodeGraphError(se.Body)
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
