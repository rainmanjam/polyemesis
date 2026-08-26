// Package oauth implements per-platform sign-in and stream-key retrieval.
//
// polyemesis cannot ship client secrets, so the operator registers their own
// developer app and pastes the client ID/secret into Settings. Everything here
// runs the authorization-code flow against those credentials, stores the
// resulting tokens encrypted (see internal/secrets), and refreshes them
// automatically.
//
// On Kick: its stream key WAS recorded here as unavailable — "confirmed absent
// from Channels, Livestreams and Users" — and that was wrong. There is no
// /streamkey endpoint to find, so reading the endpoint list suggests the
// capability does not exist; the key in fact rides as stream.key on the same
// GET /public/v1/channels response the adapter already fetches, withheld unless
// streamkey:read was granted. Kick.Ingest reads it like any other provider.
//
// ErrNoStreamKeyAPI survives for exactly one case: a token minted before that
// scope was requested, which reads as an empty key and is fixed by reconnecting
// once. Granting a scope never upgrades a token already issued.
//
// The stale claim outlived the fix in three other places — the capability
// matrix, the setup guide, and this comment. guide_drift_test.go now pins the
// guide against the matrix; this paragraph is pinned by nothing, so re-read it
// against kick.go before trusting it.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Token is the result of an authorization-code exchange or a refresh.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       string
}

// String redacts AccessToken and RefreshToken so a stray %v/%+v -- in a log
// line, a wrapped error, a test failure message -- cannot print a live token.
// Defined on the value receiver: Go always includes value-receiver methods in
// the pointer's method set (never the other way round), so this single
// definition is also what fmt reaches for a *Token. #16.
func (t Token) String() string {
	return fmt.Sprintf("Token{AccessToken:%s, RefreshToken:%s, ExpiresAt:%s, Scopes:%q}",
		redactSecret(t.AccessToken), redactSecret(t.RefreshToken), t.ExpiresAt, t.Scopes)
}

// LogValue redacts the same fields for slog, so slog.Any("token", tok) is as
// safe as fmt-printing it.
func (t Token) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("access_token", redactSecret(t.AccessToken)),
		slog.String("refresh_token", redactSecret(t.RefreshToken)),
		slog.Time("expires_at", t.ExpiresAt),
		slog.String("scopes", t.Scopes),
	)
}

// redactSecret stands in for a secret value in a String()/LogValue(); it
// leaves an unset secret visibly empty rather than claiming one is present.
func redactSecret(s string) string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

// Account identifies the connected channel.
type Account struct {
	Name string
	Ref  string
}

// Ingest is where to publish for this account.
type Ingest struct {
	URL string
	Key string
}

// Provider is one platform integration.
type Provider interface {
	Platform() db.Platform
	// Scopes are requested at authorization time and shown in the UI so the
	// user knows what they are granting.
	Scopes() []string
	// ScopeVersion is bumped BY HAND whenever Scopes changes, and is stored
	// alongside the account it was granted under.
	//
	// This exists because an OAuth token carries exactly the scopes it was
	// issued with, and adding a scope to the application does NOT upgrade a
	// token somebody already holds. Without this, an operator who connected
	// before a feature shipped silently lacks permission for it and finds out
	// from a 401 during a broadcast. polyemesis previously handled that with a
	// line in the documentation, twice.
	//
	// A developer-bumped integer rather than a diff of the granted scope
	// strings, and that is deliberate. Diffing means comparing against what
	// the PLATFORM returned, and platforms rename scopes, grant supersets and
	// reorder them -- which produces spurious "reconnect" prompts, and a
	// prompt that cries wolf is a prompt operators learn to dismiss. The
	// version is cruder and always right about the only question that matters:
	// did WE change what we ask for.
	//
	// Keep the constant next to Scopes in each provider. The pairing is the
	// only thing that makes forgetting to bump it visible in review.
	ScopeVersion() int
	// PKCE reports whether this platform accepts RFC 7636 parameters. It is
	// opt-in per provider rather than on by default: a platform whose
	// /authorize endpoint validates its query string strictly rejects an
	// unknown code_challenge outright, which locks every user out of sign-in.
	// That is a far worse outcome than doing without a defence-in-depth
	// measure on a confidential client that never exposes the code.
	PKCE() bool
	// AuthURL builds the consent URL. challenge is the S256 code_challenge,
	// and is empty whenever PKCE reports false.
	AuthURL(clientID, redirectURI, state, challenge string) string
	// Exchange trades the authorization code for tokens. verifier is the
	// code_verifier matching the challenge given to AuthURL, empty when the
	// provider does not do PKCE.
	Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error)
	Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error)
	Account(ctx context.Context, clientID, accessToken string) (*Account, error)
	// Ingest fetches the ingest endpoint and stream key, so the user never
	// copy-pastes a key.
	Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error)
}

// Providers returns every implemented provider, keyed by platform. This is
// what production uses: no options, so every provider talks to its real host.
func Providers() map[db.Platform]Provider { return ProvidersWith() }

// ProvidersWith returns the same set, with every provider built from opts.
//
// This is the seam that lets a caller outside this package -- internal/api,
// specifically -- exercise a handler end to end against a stub platform:
//
//	srv := httptest.NewServer(stub)
//	provs := oauth.ProvidersWith(oauth.WithBaseURL(srv.URL))
//
// It exists because internal/api could not reach the base URL any other way,
// and had grown five function-pointer fields on Server (ingestForFn,
// pushMetadataFn, pushComplianceFn, pushBroadcastFn, rescheduleFn) to work
// around it -- up from zero at v0.1.0, with each new platform call adding
// another. A test that replaces the provider set replaces the thing that makes
// the HTTP call, which is what those fields were imitating.
func ProvidersWith(opts ...ProviderOption) map[db.Platform]Provider {
	return map[db.Platform]Provider{
		db.PlatformYouTube:  NewYouTube(opts...),
		db.PlatformTwitch:   NewTwitch(opts...),
		db.PlatformFacebook: NewFacebook(opts...),
		db.PlatformKick:     NewKick(opts...),
		// Vimeo signs in on any plan and can do nothing live without an
		// Enterprise contract. It is registered anyway, and the reason is the
		// gate rather than in spite of it: a connected account is what lets
		// polyemesis ASK Vimeo whether this operator reaches the live API, at
		// connect time, instead of leaving them to discover it from a refusal
		// mid-broadcast. See vimeo.go and entitlement.go.
		db.PlatformVimeo: NewVimeo(opts...),
	}
}

// Get returns a provider, or an error naming the platform.
func Get(p db.Platform) (Provider, error) {
	if pr, ok := Providers()[p]; ok {
		return pr, nil
	}
	return nil, fmt.Errorf("no OAuth provider for platform %q", p)
}

// ------------------------------------------ optional platform capabilities

// TargetedProvider is the optional capability for a platform where one
// connected login can publish to more than one destination. Discover it with
// TargetsFor; never type-assert Provider at a call site, because "absent" is
// the answer for every other platform and has to be handled once.
//
// FACEBOOK IS STILL THE ONLY IMPLEMENTATION AND THIS LIVES HERE ANYWAY, which
// is deliberate. An interface that moves out of a platform file in the same
// commit that adds its second implementer arrives together with that
// implementer, and nothing afterwards can say which was shaped to fit the
// other. Sited first and alone, the next platform has to fit what is already
// written down.
//
// It is not a general rule that one implementation is too few. It is a rule
// about the ones a caller DISCOVERS rather than calls: the value of these is
// that ABSENT is a supported answer handled once, and a capability sitting in a
// platform's own file reads as that platform's private business until somebody
// checks.
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

// BroadcastTarget is one place a single connected account may publish to.
type BroadcastTarget struct {
	// Ref is what gets stored in PlatformAccount.AccountRef and handed back to
	// AccountFor/IngestFor. See parseTargetRef for the spellings.
	Ref string `json:"ref"`
	// Kind is the platform's own word for what this target is, so the UI can
	// group and badge them: "user" or "page" on Facebook, "channel" on YouTube.
	// Not normalised to a shared vocabulary, because a Page is not a channel and
	// an operator comparing this against the platform's own UI has to see the
	// term that platform uses.
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
	// ID is the platform's id for the broadcast object -- Facebook's live_video
	// id today. It is the handle for ending the broadcast, editing its metadata
	// and reading its comments, so a caller that discards it has to create a new
	// broadcast to get another one.
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
//
// THE FIELD NAMES ARE STILL FACEBOOK'S, and moving the type here did not change
// that or intend to. Privacy, BackupIngest and the seven-day bound on
// ScheduledFor are facts about one platform's API, and renaming them to
// something platform-neutral is a design question -- what is the union of what
// two platforms accept at create time -- not a question a file move gets to
// answer in passing.
type IngestOptions struct {
	Privacy         db.FacebookPrivacy
	Crosspost       []db.CrosspostTarget
	DonateCharityID string
	// BackupIngest asks Facebook to provision a secondary ingest endpoint, so
	// a redundant feed can be published alongside the primary.
	//
	// This flag is what PROVISIONS the backup url; without it the secondary
	// lists come back empty. Meta's live_videos edge reference:
	//
	//	enable_backup_ingest  boolean
	//	Set this to true to enable a backup ingest url.
	//	stop_on_delete_stream defaults to false when set
	//
	// and the getting-started response shows "secure_stream_secondary_urls":
	// [] on a create without it. An earlier version of this comment said the
	// relationship was "not established" -- it is, and the source is the EDGE
	// reference rather than the LiveVideo node reference, which 404s.
	//
	// A caller must still handle an empty Backups even when it asked: eligibility
	// and account state can refuse what the flag requests.
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
	// DedicatedIngest asks the platform for an ingest stream that belongs to
	// THIS destination rather than the one the account already shares.
	//
	// It exists because YouTube counts concurrency two ways at once -- per
	// stream key and per channel -- and the per-key ceiling is the smaller of
	// the two, so every destination handed the same key counts as one
	// ingestion source and the channel ceiling is unreachable. A refused
	// broadcast comes back as sharedIngestionBroadcastsExceedLimit, which
	// youtube_lifecycle.go classifies as RefusalSharedIngestionFull precisely
	// because it is polyemesis's doing and not the operator's. Neither number
	// is published by YouTube and neither is written down here: this is the
	// shape of the fix, not a count.
	//
	// IT IS AN OPTION RATHER THAN A PROVIDER DECISION BECAUSE A PROVIDER
	// CANNOT MAKE IT. Answering "does this destination need its own stream"
	// means knowing whether some OTHER destination already holds the account's
	// shared one, and a provider is handed a token and a target ref -- it has
	// never heard of a destination. The caller owns the destination table, so
	// the caller decides; see internal/api's ingestOptions.
	//
	// False keeps today's behaviour EXACTLY: the account's existing reusable
	// stream, created only if there is none. That is what protects the common
	// case -- one destination, whose key an operator's Studio-scheduled events
	// are already bound to and which must not change under them.
	//
	// HeldKey outranks it. A destination that already holds a key keeps that
	// stream whatever this says, so flipping the flag on an established
	// destination does not mint a second stream for it.
	DedicatedIngest bool
	// RotateKey says the OPERATOR ASKED FOR A NEW KEY, which is the one
	// circumstance in which the key on a destination may change.
	//
	// WITHOUT IT THE DEDICATED-STREAM FEATURE IS INERT ON EVERY INSTALL THAT
	// NEEDS IT, and that was measured rather than argued. HeldKey is matched
	// first so an automatic sweep can never move a key out from under somebody
	// mid-broadcast. But in production EVERY YouTube destination already holds
	// the same shared key -- that is precisely the defect -- so the match
	// always succeeds, the dedicated branch is never reached, and nothing is
	// created. Three destinations were driven through the real refresh handler
	// and produced zero new streams.
	//
	// The two requirements are the same line of code: "never move a key" and
	// "move this destination to its own stream" cannot both hold for a
	// destination that already holds the shared one. What separates them is not
	// a rule, it is WHO ASKED. A five-minute sweep has no right to rotate a key
	// an encoder is publishing with; an operator pressing Refresh stream key
	// has asked for exactly that and would be puzzled to get the old one back.
	//
	// So this is set on the explicit path and nowhere else.
	RotateKey bool
	// HeldKey is the stream key this destination is ALREADY publishing with,
	// empty for one that has never fetched an ingest.
	//
	// It is an input to a create so that a re-fetch is not a rotation. Refresh
	// key is a button an operator presses when they think something is stale,
	// and a platform whose streams are addressable by key can answer it by
	// handing back the same stream instead of provisioning another one --
	// which would leave the key in their encoder pointing at a stream nothing
	// publishes to. It is the mechanism behind DedicatedIngest's last
	// paragraph, and it is the only thing that keeps the destination holding
	// the shared stream on the shared stream once its neighbours have moved
	// off it.
	//
	// A KEY IN, NEVER A KEY OUT. This is matched byte for byte against what
	// the platform lists and is never logged, echoed in an error or sent as a
	// request parameter; #306 is what happens when a stored key and the key on
	// the wire are allowed to differ, so nothing here trims or normalises it.
	HeldKey string
	// IngestLabel is the destination's name, for platforms that let a
	// provisioned stream be titled.
	//
	// A channel with five polyemesis streams on it, all called "polyemesis",
	// is a channel whose operator cannot tell which one their second show
	// publishes to -- and these are visible in YouTube Studio, which is where
	// they will go looking. The destination's own name is the only material
	// polyemesis has that means anything to them.
	//
	// NOT A SECRET AND NOT A KEY. It is a display string that reaches the
	// platform's public-ish metadata, so a caller must never put a key, a
	// token or an ingest URL in it.
	IngestLabel string
}

// ScheduledBroadcaster is the optional capability for a platform that can
// create a broadcast BEFORE the show and move it afterwards -- what gives a
// scheduled show an event page people can subscribe to. Discover it with
// ScheduledBroadcastsFor; never type-assert Provider at a call site, because
// "absent" is the answer for every other platform and has to be handled once.
//
// It exists because that rule was being broken in the plainest possible way.
// internal/api reached RescheduleBroadcast with a CONCRETE-type assertion --
// `fb, ok := p.(*oauth.Facebook)` -- on a method that was on no interface at
// all, so the one place that knew a platform could not do this was an `ok`
// check against a struct pointer. A second platform with a schedulable
// broadcast would have had to be added to that assertion by hand, and the
// compiler would not have said a word.
//
// It came here together with TargetedProvider and not one at a time, because
// half of this capability is over there: CREATING the scheduled broadcast is
// TargetedProvider.IngestFor with IngestOptions.ScheduledFor set, deliberately
// not a method here -- a Create on this interface would be a second mechanism
// for one concept, which is exactly how endpoints.go records the graphBase seam
// growing up beside WithBaseURL and covering one endpoint out of thirteen. A
// platform holding one half of the pair can pre-announce nothing, so leaving
// either half behind would have been a move that looked done and was not.
//
// Nothing here is stubbed onto YouTube, Twitch or Kick. The value of the
// interface is that ABSENT is a supported answer, handled once by the caller,
// not that every provider grows a method that returns an error.
type ScheduledBroadcaster interface {
	Provider
	// ScheduleHorizon is how far ahead of now this platform will accept a
	// start time. A caller must refuse an occurrence beyond it rather than
	// send it, because the platform's refusal arrives as a generic API error
	// that reads like every other one.
	//
	// A method rather than a constant at the call site because it is a fact
	// about the PLATFORM. internal/api USED to spell Facebook's seven days out
	// as a facebookScheduleHorizon constant, inside a loop already gated on
	// `d.Platform != db.PlatformFacebook` -- the same defect as a type
	// assertion wearing a different hat. Both are gone: preannounce.go:116 and
	// automation.go:288 now read this method, so a caller enforces "this
	// platform's bound" without knowing which platform it is holding, and the
	// two paths cannot disagree about the bound the way two constants could.
	ScheduleHorizon() time.Duration
	// RescheduleBroadcast moves an already-created broadcast to a new start
	// time. broadcastID is the id the create returned and the caller stored;
	// an empty one is an error rather than a no-op, because a platform can
	// answer a write to no object in a way that reads as success.
	RescheduleBroadcast(ctx context.Context, accessToken, broadcastID string, at time.Time) error
}

// ScheduleHorizonUnbounded is what ScheduleHorizon returns for a platform that
// PUBLISHES NO BOUND on how far ahead a broadcast may be scheduled.
//
// It exists so that "the docs state no limit" and "the limit happens to be some
// number I chose" cannot be spelled the same way. Facebook's seven days is a
// documented sentence -- Graph refuses an event_params further out -- and is a
// real number for that reason. YouTube's liveBroadcasts.insert reference names
// snippet.scheduledStartTime as required and says nothing whatever about how far
// ahead it may point; the live errors page lists invalidScheduledStartTime with
// no bound attached (both read 2026-08-16). Returning 30 days, or 90, or a year
// would be this repository's most-repeated defect -- a guessed number encoded as
// though it were documented -- and it would silently drop every occurrence past
// the guess, with no error anywhere, because the caller SKIPS rather than fails
// when an occurrence is out of range.
//
// The maximum Duration and not zero, because of how a caller reads this: both
// gates -- preannounce.go's `at.Sub(now) > sb.ScheduleHorizon()` and
// automation.go's `at.Sub(now) <= sb.ScheduleHorizon()` -- treat a SMALLER
// horizon as a tighter refusal, so zero would mean "refuse everything" rather
// than "no bound to enforce". time.Time.Sub saturates at this value instead of
// overflowing, so the comparison is well defined for any two instants.
//
// It asserts nothing about what YouTube will accept. A start time YouTube
// refuses still comes back as invalidScheduledStartTime from the create, which
// is the right place for a bound only the platform knows to be enforced.
const ScheduleHorizonUnbounded = time.Duration(math.MaxInt64)

// ScheduledBroadcastsFor returns the pre-announce capability for a platform, or
// false when that platform has none. Mirrors TargetsFor and MetadataFor, both
// in shape and in what false means: it covers "this platform cannot schedule"
// and "there is no provider for this platform at all", because neither caller
// does anything different about them.
//
// Named for the thing rather than shortened to SchedulesFor because the only
// caller holds a scheduler.Schedule in the same function, and two unrelated
// senses of "schedule" one line apart is how a reader loses the thread.
func ScheduledBroadcastsFor(p db.Platform) (ScheduledBroadcaster, bool) {
	pr, ok := Providers()[p]
	if !ok {
		return nil, false
	}
	sb, ok := pr.(ScheduledBroadcaster)
	return sb, ok
}

// httpClient is shared; the timeout keeps a hung platform API from wedging a
// request handler.
var httpClient = &http.Client{Timeout: 20 * time.Second}

// tokenStatusError is a non-2xx response from a platform token endpoint. It
// carries the numeric status so a caller -- classifyCheckError, specifically
// -- can distinguish "the platform refused this credential" from "the
// platform could not answer" by comparing an int, rather than by parsing a
// status code back out of a formatted string.
//
// This is a different type from statusError in metadata.go on purpose, not
// an oversight: that one carries the request URL and already means something
// specific to scopeAdvice and fbAdvice, which switch on its Status field for
// Graph/Helix metadata writes. Reusing it here would make a metadata
// permission error and a token-endpoint failure look like the same kind of
// thing to any future `case *statusError` that assumes one meaning.
type tokenStatusError struct {
	code int
	body string
}

// Error's wording is unchanged from the fmt.Errorf this type replaced, so
// nothing that previously read the message -- logs, other error text built
// with %w -- changes behaviour.
func (e *tokenStatusError) Error() string {
	return fmt.Sprintf("token endpoint returned %d: %s", e.code, e.body)
}

// postForm performs an OAuth token request and decodes the standard response.
func postForm(ctx context.Context, endpoint string, form url.Values, headers map[string]string) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &tokenStatusError{code: resp.StatusCode, body: snippet(body)}
	}

	var out struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		ExpiresIn    int             `json:"expires_in"`
		Scope        json.RawMessage `json:"scope"`
		Error        string          `json:"error"`
		ErrorDesc    string          `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s: %s", out.Error, out.ErrorDesc)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("token response contained no access_token")
	}

	t := &Token{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		Scopes:       decodeScope(out.Scope),
	}
	if out.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return t, nil
}

// decodeScope handles both spellings: Google returns a space-delimited string,
// Twitch returns an array.
func decodeScope(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return strings.Join(arr, " ")
	}
	return ""
}

// getJSON performs an authenticated GET and decodes into out.
func getJSON(ctx context.Context, endpoint, accessToken string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("the platform rejected the access token (401); reconnect the account")
	}
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("the platform refused the request (403): %s", snippet(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d: %s", endpoint, resp.StatusCode, snippet(body))
	}
	return json.Unmarshal(body, out)
}

func postJSON(ctx context.Context, endpoint, accessToken string, payload any, headers map[string]string, out any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d: %s", endpoint, resp.StatusCode, snippet(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// SetupGuide is the in-UI instructions for registering a developer app.
type SetupGuide struct {
	Platform db.Platform `json:"platform"`
	Name     string      `json:"name"`
	// ConsoleURL is where the user registers the app.
	ConsoleURL string `json:"consoleUrl"`
	// RedirectPath is appended to the server's origin to form the redirect URI
	// the user must whitelist.
	RedirectPath string   `json:"redirectPath"`
	Steps        []string `json:"steps"`
	Scopes       []string `json:"scopes"`
	// Supported reports whether polyemesis can sign in to this platform at all.
	Supported bool `json:"supported"`
	// ManualStreamKey reports that the account connects, but the key is pasted
	// rather than fetched — the two are separate answers, and Kick is the reason
	// they had to be. Read it from ManualKeyFor rather than hard-coding it.
	ManualStreamKey bool `json:"manualStreamKey,omitempty"`
	// DeviceFlow reports that this platform can connect an account WITHOUT a
	// redirect URI -- the operator types a code at the platform's own site and
	// the box polls for the token. Read from DeviceFor for the same reason
	// ManualStreamKey is read from ManualKeyFor: it is exactly one platform
	// today (device.go says why the other three are absent, and the reasons
	// differ), and a UI that hard-coded the name would go on offering the
	// button for a platform that lost the capability and would never offer it
	// for one that gained it.
	DeviceFlow bool   `json:"deviceFlow,omitempty"`
	Note       string `json:"note,omitempty"`
	// RedirectWarnings are computed per request by the API layer, which is
	// where the configuration and the inbound Host live. Empty here; filled in
	// by handlePlatformGuides.
	RedirectWarnings []string `json:"redirectWarnings,omitempty"`
}

// Guides returns the setup instructions rendered on the credentials page.
//
// ManualStreamKey and DeviceFlow are filled in from ManualKeyFor and DeviceFor
// rather than written out per entry, so a provider that gains a key endpoint
// stops advertising the paste step the moment it drops the ManualKey interface,
// and a platform that gains or loses a device flow changes what the UI offers
// without anybody editing a list of platform names.
func Guides() []SetupGuide {
	guides := guides()
	for i := range guides {
		if _, manual := ManualKeyFor(guides[i].Platform); manual {
			guides[i].ManualStreamKey = true
		}
		if _, device := DeviceFor(guides[i].Platform); device {
			guides[i].DeviceFlow = true
		}
	}
	return guides
}

func guides() []SetupGuide {
	return []SetupGuide{
		{
			Platform:     db.PlatformYouTube,
			Name:         "YouTube (Google)",
			ConsoleURL:   "https://console.cloud.google.com/apis/credentials",
			RedirectPath: "/api/v1/oauth/youtube/callback",
			Supported:    true,
			Scopes:       (&YouTube{}).Scopes(),
			Steps: []string{
				"Open the Google Cloud Console and create a project (or pick an existing one).",
				"In APIs & Services → Library, enable the “YouTube Data API v3”.",
				"In APIs & Services → OAuth consent screen, choose External, fill in the app name and your email, and add your own Google account under Test users. You do not need to publish the app.",
				"In APIs & Services → Credentials, click Create Credentials → OAuth client ID → Web application.",
				"Under “Authorised redirect URIs”, add exactly the redirect URI shown below.",
				"Copy the Client ID and Client secret into the fields on this page and save.",
				"Go to a destination and click Connect account. YouTube will ask you to grant access, then polyemesis fetches your ingest URL and stream key automatically.",
			},
		},
		{
			Platform:     db.PlatformTwitch,
			Name:         "Twitch",
			ConsoleURL:   "https://dev.twitch.tv/console/apps",
			RedirectPath: "/api/v1/oauth/twitch/callback",
			Supported:    true,
			Scopes:       (&Twitch{}).Scopes(),
			Steps: []string{
				"Open the Twitch Developer Console and click Register Your Application.",
				"Give it any name, set the OAuth Redirect URL to exactly the URI shown below, and pick category “Broadcasting Suite”.",
				"Set Client Type to Confidential — polyemesis exchanges the code server-side and needs a client secret.",
				"Click Manage on the new app, then New Secret, and copy both the Client ID and the secret into the fields on this page.",
				"Go to a destination and click Connect account. polyemesis reads your stream key via the Helix API.",
			},
		},
		{
			Platform:     db.PlatformFacebook,
			Name:         "Facebook Live (Meta)",
			ConsoleURL:   "https://developers.facebook.com/apps",
			RedirectPath: "/api/v1/oauth/facebook/callback",
			Supported:    true,
			Scopes:       (&Facebook{}).Scopes(),
			Note: "Read this first: Meta will not let anyone but you use this app until it passes App Review. " +
				"Your own Facebook account works immediately as a developer/tester of the app, which is all a " +
				"self-hosted setup normally needs — but if anyone else is going to connect their account here, " +
				"the app has to be submitted for Advanced Access to publish_video (profiles) or " +
				"pages_manage_posts (Pages) first. Budget days, not minutes, and start it before you need it. " +
				"Facebook also issues a new ingest URL and key for every broadcast, so connecting the account " +
				"creates the broadcast: there is no permanent key to reuse.",
			Steps: []string{
				"Before anything else: decide who will connect accounts. Only people listed under Roles → Roles " +
					"(admins, developers, testers) can use an app in development mode. Anyone else requires App Review, " +
					"which is Meta's process, not something polyemesis can shortcut.",
				"Open the Meta app dashboard and click Create app. Pick the “Other” use case and the “Business” type — " +
					"that is the one that offers Facebook Login and the Live Video permissions.",
				"Add the “Facebook Login” product. Under Facebook Login → Settings, switch on “Client OAuth login” and " +
					"“Web OAuth login”, and add exactly the redirect URI shown below under “Valid OAuth Redirect URIs”.",
				"In App settings → Basic, copy the App ID and App Secret into the fields on this page. The App ID is " +
					"the client ID and the App Secret is the client secret.",
				"Decide the target. Streaming to your own profile needs publish_video. Streaming to a Page needs " +
					"pages_manage_posts and pages_read_engagement, plus pages_show_list so polyemesis can offer you " +
					"the Page to pick. polyemesis asks for all of them; you can decline the ones you do not want on " +
					"the consent screen.",
				"In App Review → Permissions and Features, request Advanced Access for the permissions you need. " +
					"Skip this only while you are the sole person connecting an account.",
				"Go to a destination and click Connect account, then choose your profile or one of your Pages. " +
					"polyemesis creates the broadcast and fills in the RTMPS URL and stream key for you.",
			},
		},
		{
			Platform:     db.PlatformKick,
			Name:         "Kick",
			ConsoleURL:   "https://kick.com/settings/developer",
			RedirectPath: "/api/v1/oauth/kick/callback",
			Supported:    true,
			Scopes:       (&Kick{}).Scopes(),
			Note: "Kick uses OAuth 2.1, so the consent step sends a PKCE challenge automatically — " +
				"there is nothing extra to configure for it. Grant every scope on the consent screen: " +
				"the stream key is withheld unless streamkey:read is among them, and an account " +
				"connected before that scope was requested has to be disconnected and reconnected once " +
				"before the key appears.",
			Steps: []string{
				"Open Kick → Settings → Developer and create an OAuth application.",
				"Set the Redirect URI to exactly the URI shown below.",
				"Copy the Client ID and Client Secret into the fields on this page and save.",
				"Click Connect account. Kick uses OAuth 2.1, so polyemesis sends a PKCE challenge automatically.",
				"Nothing to paste: polyemesis reads the ingest URL and stream key from the channels " +
					"resource over the streamkey:read scope, the same way it does for the other platforms.",
			},
		},
		{
			Platform:     db.PlatformVimeo,
			Name:         "Vimeo",
			ConsoleURL:   "https://developer.vimeo.com/apps",
			RedirectPath: "/api/v1/oauth/vimeo/callback",
			Supported:    true,
			Scopes:       (&Vimeo{}).Scopes(),
			// FIRST, IN THE PLATFORM'S OWN WORDS, BECAUSE IT DECIDES WHETHER
			// ANY OF THE STEPS BELOW ARE WORTH DOING. This is the same job
			// Facebook's App Review note does -- an obstacle that costs the
			// operator their evening if they meet it at step six instead of
			// before step one -- except that this one has no process at the end
			// of it, only a price.
			Note: "Read this first: Vimeo's live API is available only to Vimeo Enterprise customers. " +
				"That is Vimeo's own sentence, not polyemesis's reading of it, and it is a commercial " +
				"gate rather than a permission — no scope, no reconnection and no app setting lifts it. " +
				"Signing in works on any Vimeo plan and is still worth doing: polyemesis asks the live " +
				"API whether your account reaches it the moment you connect, and tells you then rather " +
				"than letting the refusal arrive during a broadcast. What sign-in does NOT do on any " +
				"plan is fetch a stream key — Vimeo issues the ingest URL and key with a live event, so " +
				"both must be pasted from the event's setup panel. Vimeo is also deprecating one-time " +
				"live events and recommends avoiding them, so create a RECURRING event.",
			Steps: []string{
				"Open developer.vimeo.com → My Apps and click Create an app. Any name and description will do.",
				"On the app's page, under OAuth redirect authentication, add exactly the redirect URI shown below. " +
					"Leave implicit authentication switched off — polyemesis exchanges the code server-side.",
				"Copy the Client Identifier and Client Secret into the fields on this page and save. Vimeo can " +
					"verify this pair immediately, so a typo is caught here rather than at consent time.",
				"Go to a destination and click Connect account. Vimeo asks you to approve the public and private " +
					"scopes, which is what lets polyemesis read which member the token belongs to.",
				"Watch the message that comes back from connecting. It says whether your account reaches Vimeo's " +
					"live API. If it does not, everything below still works — you are just pasting the key.",
				"In Vimeo, create a recurring live event and open its setup panel. Copy the RTMPS server URL and " +
					"the stream key into this destination. Streaming to it then works exactly as well as to any " +
					"other destination.",
			},
		},
	}
}
