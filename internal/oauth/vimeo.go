package oauth

// Vimeo: the sign-in works for everybody and the live API works for almost
// nobody, and the whole shape of this file follows from that one sentence.
//
// From Vimeo's own live reference (https://developer.vimeo.com/api/reference/live,
// read 2026-08-26, rendered in a browser -- see below):
//
//	"Please note that our live API is available only to Vimeo Enterprise
//	 customers."
//
// Vimeo's OAuth is open to any registered app on any plan. Its live API -- the
// whole of it: create an event, activate, end, ingest status, RTMP
// destinations, M3U8 playback, thumbnails -- is not. Every live method on that
// page carries a CAPABILITY badge, and the one polyemesis probes says so
// explicitly: "This method requires an app with the capability
// CAPABILITY_RECURRING_LIVE_EVENTS."
//
// So an operator can register an app, paste correct credentials, get a green
// credential check, complete consent, and see "Connected vimeo as ..." -- and
// still be unable to do a single live thing. Without something built to say so,
// the first evidence they get is a refusal in the middle of a broadcast, from
// an API that never uses the word Enterprise.
//
// WHAT THIS FILE THEREFORE IS. Sign-in, identity, a credential check, and a
// probe of the gated API that runs the moment a token exists. It implements
// EntitlementGated (entitlement.go) so the connect handler can report the gate
// in Vimeo's own words at connect time, and ManualKey so the stream-key field
// carries the same sentence rather than an empty box.
//
// WHAT THIS FILE IS NOT, on purpose: there is no event lifecycle here. Create,
// activate and end are documented and unreachable for the median operator, and
// unreachable for this author -- there is no Enterprise contract to test a
// single call against. Writing them would mean shipping untested code for a
// capability the matrix could not honestly claim. The gate is the finding; the
// lifecycle is what somebody with a contract builds on top of it.
//
// TWO TRAPS, BOTH OF WHICH PRODUCE CODE THAT LOOKS RIGHT:
//
//  1. developer.vimeo.com IS CLIENT-SIDE RENDERED. A plain fetch answers HTTP
//     200 with a body containing only the word "Vimeo". Every fact in this file
//     was read from a rendered page in a real browser; a fetcher that trusted
//     the status code would have concluded the docs were empty, and one that
//     trusted its own memory would have filled the gap from training data.
//     docs/evidence/vimeo-trovo-oauth-2026-08-26.md records this trap because
//     it was hit during research.
//
//  2. VIMEO'S TOKEN ENDPOINTS TAKE JSON AND HTTP BASIC, NOT A FORM. Every other
//     provider in this package posts application/x-www-form-urlencoded with
//     client_id and client_secret as fields, which is what postForm does.
//     Vimeo's Table 8 is explicit: Authorization is
//     `basic base64_encode(x:y)`, Content-Type is application/json, and the
//     body carries grant_type/code/redirect_uri as JSON. A form post here is
//     refused, and the refusal reads like a bad credential.
//
// AND ONE THING VIMEO SIMPLY DOES NOT ISSUE: a refresh token. Table 10 lists
// the authorization-code response fields as access_token, token_type, scope and
// user -- no refresh_token and no expires_in. The token "remains active as long
// as we perceive that you're using it". Refresh below says that rather than
// inventing a call; see the method.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Vimeo implements Vimeo's authorization-code flow, the identity read behind
// it, and the live-API entitlement probe.
type Vimeo struct {
	// endpoints carries the base URLs; zero value is production. Consent,
	// tokens and data all live on one host here, as they do for X and unlike
	// YouTube, Twitch and Kick -- so a reader looking for a second constant
	// should stop looking. See endpoints.go.
	endpoints
}

// vimeoAPIBase is the one production host. Both accessors resolve to it, which
// is what keeps a provider built with WithBaseURL from reaching the real Vimeo
// for any call.
const vimeoAPIBase = "https://api.vimeo.com"

// vimeoAccept pins the API version, and it is not optional decoration. Vimeo's
// authentication guide, verbatim: "Don't omit the Accept header in this request
// (and all subsequent requests in this guide), and always be sure to set it to
// the given value. This ensures that the API handles your response according to
// version 3.4."
//
// A request without it gets whatever version Vimeo currently defaults to, which
// is a response shape that can change under a running install without anything
// here changing. Sent on every call this file makes.
const vimeoAccept = "application/vnd.vimeo.*+json;version=3.4"

// vimeoLiveEventsPath is the probe target, and it was chosen because it is the
// cheapest READ on the gated surface: it lists events and creates nothing.
//
// Vimeo documents three spellings of the same method -- /users/{user_id}/live_events,
// /live_events and /me/live_events. The /me form is used because the probe runs
// with a token whose user id has not necessarily been read yet, and because a
// path with no interpolated id cannot be built wrong.
const vimeoLiveEventsPath = "/me/live_events"

func (v *Vimeo) apiEndpoint() string  { return v.apiBase(vimeoAPIBase) }
func (v *Vimeo) authEndpoint() string { return v.authBase(vimeoAPIBase) }

// NewVimeo lives in endpoints.go beside NewYouTube and friends, because Vimeo
// IS in the production set -- unlike NewX, which sits in x.go for the opposite
// reason.
//
// The interface assertions, so a signature drifting out from under one of these
// capabilities is a compile error rather than a lookup that silently starts
// answering false.
var (
	_ Provider          = (*Vimeo)(nil)
	_ ManualKey         = (*Vimeo)(nil)
	_ EntitlementGated  = (*Vimeo)(nil)
	_ CredentialChecker = (*Vimeo)(nil)
)

func (v *Vimeo) Platform() db.Platform { return db.PlatformVimeo }

// Scopes asks for exactly what polyemesis uses today, and that is a departure
// from the rule kick.go states.
//
// Kick's list is settled in one go, on the grounds that "adding a scope later
// does not upgrade an existing connection, it forces every operator to
// disconnect and reconnect". That reasoning is right, and it applies when the
// wider surface is landing alongside -- Kick's chat and moderation were.
// Vimeo's is not, and cannot be: everything create/edit would authorise sits
// behind a commercial gate nobody here can open or test against.
//
// So asking for `create` and `edit` would mean requesting the power to make and
// modify resources across an operator's whole Vimeo library, on the consent
// screen, for features that do not exist and that most of the people granting
// them could not use. That is a worse trade than a future reconnect, which
// ScopeVersion exists to make visible and survivable.
//
//	public   Table 1: "Access public member data." Its footnote is the reason
//	         it cannot be dropped -- an authenticated token with public scope is
//	         what makes the /me endpoint refer to the logged-in user at all.
//	private  Table 1: "Access private member data", and footnoted "Required for
//	         any scope other than public". The live-events listing is private
//	         member data, so the probe needs it.
//
// Both are what Vimeo itself defaults to when scope is omitted ("its default
// value is public private"), which is a useful corroboration that this is the
// ordinary ask rather than an unusually small one.
func (v *Vimeo) Scopes() []string {
	return []string{"public", "private"}
}

// ScopeVersion 1: the first list. Bump it BY HAND if `create`, `edit` or
// `upload` are ever added for event lifecycle -- an account connected now holds
// a token that cannot create an event no matter what Vimeo's plan says, and the
// account list is where the operator has to be told so.
func (v *Vimeo) ScopeVersion() int { return 1 }

// PKCE is off. Vimeo's authentication guide describes four grant types --
// client credentials, authorization code, implicit and device code -- and
// mentions no code_challenge, code_challenge_method or code_verifier anywhere
// in the authorization-code workflow; the flow is a confidential client using
// client_secret over HTTP Basic.
//
// Recorded as NOT DOCUMENTED rather than as absent, which for Provider.PKCE is
// the same answer: its comment says a platform whose /authorize validates its
// query string strictly rejects an unknown code_challenge outright and locks
// every user out of sign-in. Vimeo's own words for a bad authorize request are
// "the request fails, and the standard Vimeo 404 page loads", which is exactly
// that failure wearing its least diagnosable face.
func (v *Vimeo) PKCE() bool { return false }

// AuthURL builds the consent URL from Table 6.
func (v *Vimeo) AuthURL(clientID, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	// "The space-separated list of scopes that you want to be able to access."
	// Table 6, verbatim. NOT comma-separated and NOT plus-separated in the
	// source string -- url.Values.Encode percent-escapes the separator on the
	// wire, which is the form RFC 6749 servers decode back to a space.
	q.Set("scope", strings.Join(v.Scopes(), " "))
	// challenge is deliberately ignored: PKCE reports false, and the shared
	// pkce tests feed every provider a challenge to prove none of the opted-out
	// ones leak it.
	_ = challenge
	return v.authEndpoint() + "/oauth/authorize?" + q.Encode()
}

// Exchange trades the authorization code for a token, per Tables 8 and 10.
//
// verifier is ignored rather than sent. Vimeo has not documented PKCE, and
// oauth.go's Provider.PKCE comment is explicit that sending an undocumented
// parameter to an authorization server is the change that locks everyone out.
func (v *Vimeo) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error) {
	_ = verifier
	return v.token(ctx, clientID, clientSecret, v.authEndpoint()+"/oauth/access_token", map[string]string{
		"grant_type":   "authorization_code",
		"code":         code,
		"redirect_uri": redirectURI,
	})
}

// Refresh reports, in one sentence, that Vimeo does not do this.
//
// THIS IS NOT A STUB AND MUST NOT BECOME ONE. Table 10 lists every field of the
// authorization-code response -- access_token, token_type, scope, user -- and
// there is no refresh_token and no expires_in among them. The guide's own
// account of the lifetime is that the token "remains active as long as we
// perceive that you're using it", with inactive tokens deleted. There is no
// refresh grant documented for this flow, so there is no endpoint to call.
//
// The practical consequence is nil: Exchange leaves Token.ExpiresAt zero, so
// db.PlatformAccount.Expired() is false and internal/api's tokenFor returns the
// stored token without ever reaching here. This method exists because Provider
// requires it, and it says the true thing rather than posting a guessed
// grant_type at an endpoint that would answer 400 and read like a broken
// credential.
func (v *Vimeo) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	return nil, errors.New("Vimeo issues no refresh token for the authorization-code grant " +
		"and documents no refresh endpoint: an access token stays valid while it is used, " +
		"and a token Vimeo has retired is recovered by connecting the account again")
}

// vimeoUser is the subset of GET /me this provider reads. Both fields are on
// Vimeo's own example response for that method.
type vimeoUser struct {
	// URI is the resource path, "/users/152184". Vimeo publishes no bare
	// numeric id field on this resource, so the ref is derived from it.
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// Account identifies the connected member from GET /me.
//
// clientID is unused: Vimeo authenticates data requests with the bearer token
// alone. It stays in the signature because Twitch's Helix needs it and Provider
// is one interface.
func (v *Vimeo) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	var u vimeoUser
	if err := getJSON(ctx, v.apiEndpoint()+"/me", accessToken, vimeoHeaders(), &u); err != nil {
		return nil, err
	}
	ref := strings.TrimPrefix(strings.TrimSpace(u.URI), "/users/")
	name := strings.TrimSpace(u.Name)
	if name == "" {
		name = ref
	}
	if ref == "" && name == "" {
		return nil, fmt.Errorf("Vimeo returned no identity for this token; make sure the app was granted the public scope, which is what lets /me refer to the signed-in member")
	}
	return &Account{Name: name, Ref: ref}, nil
}

// Ingest asks the gate rather than guessing, and both answers are true
// statements an operator can act on.
//
// Vimeo has no permanent stream key. The ingest URL and key belong to a live
// event's RTMP destination, so obtaining one means creating or reading a live
// event -- which is the gated surface. That makes the honest answer depend on
// the account, and the only way to know is to ask.
//
// It returns ErrNoStreamKeyAPI in BOTH branches, which looks odd and is
// deliberate: internal/api treats that sentinel as "send the operator to the
// paste field, with this reason, and do not offer a retry that cannot work"
// (a 400 rather than a 502). That is the correct handling for a gated account
// and for an entitled one alike, because polyemesis does not create Vimeo
// events yet either way. The two branches differ in what they SAY, which is the
// part the operator reads.
func (v *Vimeo) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	switch err := v.CheckEntitlement(ctx, clientID, accessToken); {
	case errors.Is(err, ErrNotEntitled):
		return nil, fmt.Errorf("%w. %s", ErrNoStreamKeyAPI, v.ManualKeyReason())
	case err != nil:
		// The probe did not complete, so the gate is unknown. Saying "your plan
		// is too small" here would be a claim about somebody's contract made on
		// the strength of a DNS failure.
		return nil, fmt.Errorf("%w. polyemesis could not check whether this Vimeo account "+
			"reaches the live API (%s), so it cannot say why no key is available. Open the "+
			"live event in Vimeo and copy the RTMPS server URL and stream key from its setup "+
			"panel", ErrNoStreamKeyAPI, err)
	default:
		// The gate is open and there is still no key to fetch, because event
		// creation is not built. Said plainly rather than dressed up as the
		// Enterprise message, which would be false for this operator.
		return nil, fmt.Errorf("%w. This Vimeo account CAN reach the live API, but polyemesis "+
			"does not create Vimeo live events yet, and a stream key belongs to an event. "+
			"Create a recurring event in Vimeo and copy the RTMPS server URL and stream key "+
			"from its setup panel", ErrNoStreamKeyAPI)
	}
}

// ManualKeyReason is what an operator reads next to the stream-key field, and
// it is written for the account that cannot pass the gate -- because that is
// the overwhelming majority of them and the one that otherwise gets no
// explanation at all.
//
// It quotes Vimeo rather than paraphrasing. "Your plan does not support this"
// is something an operator can argue with; the platform's own published
// sentence is something they can go and check, and it names the thing to
// search for if they want to know what it would cost.
//
// It carries no ingest hostname, for the reason kick.go states: an ingest URL
// invented to make the message look complete publishes to nowhere and reads as
// polyemesis's bug. Vimeo issues the server URL with the event.
func (v *Vimeo) ManualKeyReason() string {
	return "Vimeo's live API is Enterprise-only — in Vimeo's own words, \"our live API is " +
		"available only to Vimeo Enterprise customers\" — so polyemesis cannot create an event " +
		"or read a stream key for this account, and no scope or reconnection changes that. " +
		"Signing in still works and is worth having: it is how polyemesis checks the gate for " +
		"you instead of letting a refusal arrive mid-broadcast. Create the live event in Vimeo " +
		"and paste the RTMPS server URL and stream key from its setup panel; streaming to it " +
		"works exactly as well as to any other destination."
}

// EntitlementReason is the same fact stated for somebody who has not connected
// anything yet, which is where it does the most good.
func (v *Vimeo) EntitlementReason() string {
	return "Vimeo's live API is available only to Vimeo Enterprise customers. Sign-in works on " +
		"any plan; creating a live event, activating it, ending it and reading its ingest do not."
}

// CheckEntitlement asks the gated API itself whether this token reaches it.
//
// GET /me/live_events is a read that creates nothing and is documented to
// answer 200 OK ("The events were returned") when it works, above a line
// stating "This method requires an app with the capability
// CAPABILITY_RECURRING_LIVE_EVENTS". An empty list is a perfectly good pass:
// the question is whether the endpoint ANSWERS, not whether the operator has
// scheduled anything.
//
// THE REFUSAL STATUS IS NOT PINNED, AND THAT IS THE FINDING. Vimeo's reference
// for this method publishes exactly one response row, 200, and no error table
// at all -- so there is no documented status or error code for the capability
// refusal to match on. Matching 403 alone would silently pass a 401-shaped
// refusal; matching a body string would break the first time Vimeo rewords it.
// So any non-2xx is treated as "this token does not reach the live API", and
// what Vimeo actually replied is carried in the message so an operator (and the
// next person reading a bug report) can see the platform's own answer rather
// than polyemesis's interpretation of it.
//
// A transport failure is NOT folded into that. It returns unwrapped, so a
// caller's errors.Is against ErrNotEntitled is false and nobody is told about
// their contract on the strength of a timeout. See entitlement.go's three
// outcomes.
func (v *Vimeo) CheckEntitlement(ctx context.Context, clientID, accessToken string) error {
	_ = clientID
	status, body, err := v.get(ctx, v.apiEndpoint()+vimeoLiveEventsPath, accessToken)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	return fmt.Errorf("%w. %s Vimeo answered %d: %s",
		ErrNotEntitled, v.EntitlementReason(), status, snippet(body))
}

// CheckCredentials proves the client ID and secret through Vimeo's
// client-credentials grant, which needs no user consent.
//
// POST /oauth/authorize/client with grant_type client_credentials, per Table 3.
// Vimeo documents the refusal for an unrecognised app as 401 with error code
// 8001, "We don't recognize your API app" -- so a wrong pair comes back as a
// considered answer rather than as silence, which is what makes this worth
// doing at all.
//
// The scope is public and only public: Vimeo states that a client-credentials
// token cannot get anything but public data "even if you specify additional
// scopes". Asking for more here would be requesting something the grant cannot
// issue, on a call whose entire purpose is a yes/no about the credential pair.
//
// IT PROVES NOTHING ABOUT THE LIVE GATE, deliberately. A green tick here means
// the credentials are real; CheckEntitlement is the separate question, and it
// cannot be asked until a member has consented.
func (v *Vimeo) CheckCredentials(ctx context.Context, clientID, clientSecret string) error {
	_, err := v.token(ctx, clientID, clientSecret, v.authEndpoint()+"/oauth/authorize/client", map[string]string{
		"grant_type": "client_credentials",
		"scope":      "public",
	})
	return classifyCheckError(err)
}

// ---------------------------------------------------------------- transport

// vimeoHeaders is the version pin, applied to every data request. Separate from
// the token requests' headers because those also carry HTTP Basic client
// authentication and these must never.
func vimeoHeaders() map[string]string {
	return map[string]string{"Accept": vimeoAccept}
}

// token posts one of Vimeo's OAuth requests and decodes the token out of it.
//
// It exists instead of postForm because Vimeo's token endpoints take a JSON
// body and HTTP Basic client authentication (trap 2 in the file header), and
// instead of postJSON because the credential check classifies its failure by
// STATUS CODE -- classifyCheckError recovers an int from *tokenStatusError
// rather than string-matching a formatted message, and postJSON returns a
// plain fmt.Errorf.
//
// NOTHING HERE PUTS A CREDENTIAL WHERE IT CAN BE READ BACK. The client id and
// secret go in an Authorization header, never in the URL or the body, so an
// http.Client error -- which carries the full request URL -- cannot contain
// them. That is the defect credcheck.go records for Facebook's query-string
// check, avoided by construction rather than by sanitising afterwards.
func (v *Vimeo) token(ctx context.Context, clientID, clientSecret, endpoint string, payload map[string]string) (*Token, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", vimeoBasic(clientID, clientSecret))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", vimeoAccept)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &tokenStatusError{code: resp.StatusCode, body: snippet(raw)}
	}

	var out struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode Vimeo token response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s: %s", out.Error, out.ErrorDesc)
	}
	if out.AccessToken == "" {
		return nil, errors.New("Vimeo's token response contained no access_token")
	}
	// ExpiresAt stays zero and RefreshToken stays empty because Vimeo sends
	// neither for this grant. See Refresh.
	return &Token{AccessToken: out.AccessToken, Scopes: out.Scope}, nil
}

// get performs an authenticated GET and hands back the status and body without
// interpreting either.
//
// getJSON is the right helper for a request whose only interesting outcome is
// the decoded body; this one is for the probe, which is ABOUT the status. Their
// difference matters: getJSON collapses 401 and 403 into fixed sentences, and
// the entitlement probe must report what Vimeo actually said, because Vimeo
// publishes no error table for the endpoint being probed.
func (v *Vimeo) get(ctx context.Context, endpoint, accessToken string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	// Vimeo spells the scheme lowercase ("set the value of this header to
	// bearer {token}"); HTTP auth schemes are case-insensitive, and the
	// capitalised form is what every other provider here sends.
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", vimeoAccept)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}

// vimeoBasic builds the Authorization header Vimeo's Tables 3, 8 and 13 all
// specify: "basic base64_encode(x:y), where x is the client identifier and y is
// the client secret".
//
// The returned string is a live credential. It is set on a header and never
// logged, formatted into an error, or returned to a caller.
func vimeoBasic(clientID, clientSecret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret))
}
