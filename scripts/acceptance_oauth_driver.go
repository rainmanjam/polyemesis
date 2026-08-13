//go:build ignore

// Driver for acceptance-oauth.sh.
//
// internal/oauth is the largest external surface in this repository -- 10,693
// lines across nineteen hosts -- and until this suite nothing here had ever
// opened a socket to any of them. Its failure mode is the quiet one: a token
// refresh is correct on the day it ships and broken at hour four, when the
// first access token expires. It also populates destination URLs and stream
// keys automatically, which is the same field whose hand-typed equivalent
// shipped a Kick preset that could not publish (#312).
//
// WHAT IS TESTABLE WITHOUT A CREDENTIAL, and it is most of the value here:
// every one of these providers publishes its OAuth surface to the world. The
// discovery documents, the authorization and token endpoints, the grant types
// and the PKCE methods are all public, so an endpoint that moved, a grant that
// stopped being advertised, or a Graph version that was retired can all be
// caught with nothing secret involved.
//
// DRIVEN THROUGH THE PACKAGE, NOT AROUND IT. Where a check can be made by
// calling the provider's own method it is, because the thing worth testing is
// what internal/oauth builds, not a second copy of the same constant written
// here. authurl reads Provider.AuthURL; token-refusal calls Provider.Refresh;
// api-refusal calls Provider.Account. A test that retyped the URLs would go on
// passing after somebody changed them.
//
// SECRETS COME FROM THE ENVIRONMENT AND ARE NEVER PRINTED. Nothing here reads
// a credential from argv -- argv is world-readable in ps -- and every emitted
// string goes through redact(), which scrubs the literal value of every secret
// this process can see. See the live command.
//
// Proven able to fail against the committed tree: with a fake secret generated
// at runtime in POLY_OAUTH_KICK_CLIENT_SECRET, an added emit of that value came
// out as "[redacted POLY_OAUTH_KICK_CLIENT_SECRET]"; with redact() neutered to
// `return s`, the same emit printed the value. The suite's clean output is
// therefore a substitution having happened, not a platform having declined to
// echo the secret back -- which is what a run without the control would have
// shown either way.
//
// Every command here was proven able to fail; the exact change is recorded in
// acceptance-oauth.sh beside the step that consumes it.
//
//	go run scripts/acceptance_oauth_driver.go <cmd> <platform>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: acceptance_oauth_driver.go <cmd> [platform]")
	}
	arg := func(i int) string {
		if len(os.Args) > i {
			return os.Args[i]
		}
		return ""
	}
	switch os.Args[1] {
	case "discovery":
		discovery(arg(2))
	case "authurl":
		authURL(arg(2))
	case "token-refusal":
		tokenRefusal(arg(2))
	case "grant-differential":
		grantDifferential(arg(2))
	case "api-refusal":
		apiRefusal(arg(2))
	case "fb-version":
		fbVersion()
	case "live":
		live(arg(2))
	default:
		fail("unknown command %q", os.Args[1])
	}
}

func fail(f string, a ...any) {
	fmt.Printf("ERR "+f+"\n", a...)
	os.Exit(1)
}

// emit prints one key=value result line. The suite greps these, so a command
// that cannot answer prints nothing rather than a plausible zero -- a missing
// key reads as empty in the shell and fails its check, where a fabricated
// false would have read as a verdict.
//
// Every value passes through redact() on the way out. Most of what is printed
// here is server-authored refusal text with nothing secret in it, but the live
// command handles real tokens and one shared exit is easier to keep right than
// four careful call sites.
func emit(k string, v any) { fmt.Printf("%s=%s\n", k, redact(fmt.Sprint(v))) }

// ------------------------------------------------------------- redaction

// secretEnv names every variable this driver reads a credential from. It is
// the redactor's input as well as the live command's, so a variable added to
// one is automatically covered by the other.
var secretEnv = []string{
	"POLY_OAUTH_YOUTUBE_CLIENT_SECRET", "POLY_OAUTH_YOUTUBE_REFRESH_TOKEN",
	"POLY_OAUTH_TWITCH_CLIENT_SECRET", "POLY_OAUTH_TWITCH_REFRESH_TOKEN",
	"POLY_OAUTH_FACEBOOK_CLIENT_SECRET", "POLY_OAUTH_FACEBOOK_REFRESH_TOKEN",
	"POLY_OAUTH_KICK_CLIENT_SECRET", "POLY_OAUTH_KICK_REFRESH_TOKEN",
}

// redact removes the literal value of every secret this process can see, and
// flattens the result to one line so a multi-line platform error cannot forge
// extra key=value records in the output the suite parses.
//
// A SUBSTITUTION RATHER THAN A LENGTH CHECK. The rule that matters is that a
// secret's exact spelling never reaches a log, and #306 is the reminder of what
// happens when a value is trusted to be shaped as expected: the stored spelling
// and the spelling that reached the wire were allowed to differ. Short values
// are skipped because a two-character secret would blank out half the output
// and tell an operator nothing; a credential that short has bigger problems.
func redact(s string) string {
	for _, k := range secretEnv {
		if v := os.Getenv(k); len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "[redacted "+k+"]")
		}
	}
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}

// ------------------------------------------------------------- providers

func providerFor(name string) oauth.Provider {
	p, ok := map[string]db.Platform{
		"youtube":  db.PlatformYouTube,
		"twitch":   db.PlatformTwitch,
		"facebook": db.PlatformFacebook,
		"kick":     db.PlatformKick,
	}[name]
	if !ok {
		fail("unknown platform %q", name)
	}
	pr, err := oauth.Get(p)
	if err != nil {
		fail("no provider for %q: %v", name, err)
	}
	return pr
}

func ctx20() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 20*time.Second)
}

// ------------------------------------------------------------- discovery

// discoveryURL is where each platform publishes, or does not publish, its
// authorization-server metadata.
//
// Kick's two entries are not a mistake and not a placeholder. Kick speaks
// OAuth 2.1 and publishes nothing at either well-known path, which is a fact
// worth pinning: it is the reason Kick is checked by probe below rather than
// by comparison, and the day Kick starts publishing one is the day this suite
// can start comparing Kick's endpoints the way it compares Google's.
var discoveryURL = map[string]string{
	"google":    "https://accounts.google.com/.well-known/openid-configuration",
	"twitch":    "https://id.twitch.tv/oauth2/.well-known/openid-configuration",
	"facebook":  "https://www.facebook.com/.well-known/openid-configuration",
	"kick":      "https://id.kick.com/.well-known/oauth-authorization-server",
	"kick-oidc": "https://id.kick.com/.well-known/openid-configuration",
}

// discoveryDoc is the subset of RFC 8414 / OpenID Discovery that internal/oauth
// actually depends on. Fields nothing here relies on are deliberately not
// decoded: a check on a field polyemesis never reads would fail for a reason
// nobody could act on.
type discoveryDoc struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	GrantTypes            []string `json:"grant_types_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	ResponseTypes         []string `json:"response_types_supported"`
}

// discovery fetches one platform's metadata document and reports what it says.
//
// It reports rather than judges: every comparison against what internal/oauth
// hardcodes happens in the suite, where the reason for each comparison can be
// written next to it.
func discovery(name string) {
	u, ok := discoveryURL[name]
	if !ok {
		fail("no discovery URL for %q", name)
	}
	emit("url", u)

	ctx, cancel := ctx20()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		fail("building the request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		emit("reached", false)
		emit("error", err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	emit("reached", true)
	emit("status", resp.StatusCode)

	// A 404 is a RESULT, not an error. Kick answers 404 at both well-known
	// paths and that is the finding; treating it as a failure to fetch would
	// hide the difference between "Kick publishes nothing" and "the network is
	// down", which are the two things this pair of keys keeps apart.
	if resp.StatusCode != http.StatusOK {
		emit("published", false)
		return
	}
	var d discoveryDoc
	if err := json.Unmarshal(body, &d); err != nil {
		emit("published", false)
		emit("error", "the document did not parse as JSON: "+err.Error())
		return
	}
	emit("published", true)
	emit("issuer", d.Issuer)
	emit("authorizationEndpoint", d.AuthorizationEndpoint)
	emit("tokenEndpoint", d.TokenEndpoint)
	emit("grantTypes", strings.Join(d.GrantTypes, " "))
	emit("codeChallengeMethods", strings.Join(d.CodeChallengeMethods, " "))
	emit("tokenAuthMethods", strings.Join(d.TokenAuthMethods, " "))
	// COMMA-DELIMITED, unlike the three above, because response types are the
	// one field here whose entries contain spaces of their own -- "code token
	// id_token" is a single value. Space-joined, a reader looking for the plain
	// "code" response type polyemesis uses would match the "code" inside
	// "code id_token" and conclude something false.
	emit("responseTypes", ","+strings.Join(d.ResponseTypes, ",")+",")
}

// ---------------------------------------------------------------- authurl

// authURL reports the consent URL the provider builds, split into the part a
// discovery document can be compared against and the part it cannot.
//
// THE POINT IS THAT THIS COMES FROM THE PACKAGE. Provider.AuthURL is what
// production calls, so the origin and path emitted here are whatever
// ytConsentBase, twitchIDBase, kickIDBase and fbDialogBase currently say. A
// driver that wrote the URLs out again would keep passing after one of those
// constants changed, which is the entire class of bug this suite exists to
// catch.
//
// The arguments are obvious placeholders. None of them is a credential: a
// client ID is not secret, and this one is not even real.
func authURL(name string) {
	p := providerFor(name)
	raw := p.AuthURL("polyemesis-acceptance-client", "http://localhost/oauth/callback", "acceptance-state", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
	u, err := url.Parse(raw)
	if err != nil {
		fail("the provider built an unparseable AuthURL: %v", err)
	}
	emit("endpoint", u.Scheme+"://"+u.Host+u.Path)
	emit("host", u.Host)
	q := u.Query()
	emit("scope", q.Get("scope"))
	emit("responseType", q.Get("response_type"))
	emit("codeChallengeMethod", q.Get("code_challenge_method"))
	emit("pkce", p.PKCE())
	emit("scopeVersion", p.ScopeVersion())
	emit("scopeCount", len(p.Scopes()))
}

// ---------------------------------------------------------- token refusal

// tokenRefusal drives the real Refresh, against the real authorization server,
// with credentials that cannot work -- and reports how the server said no.
//
// THIS IS THE CHECK THAT FINDS A MOVED TOKEN ENDPOINT, and it needs nothing
// secret. A 4xx carrying the platform's own OAuth error vocabulary means the
// URL internal/oauth built is being served by an authorization server that
// parsed our grant and rejected our credentials. A 404 means the path is gone.
// A transport error means the host is gone. Those are three different repairs
// and the suite reports them as three different things.
//
// WHAT IT DOES NOT PROVE, and the suite says so in its own message: that a
// REAL refresh works. A 401 from a token endpoint proves the endpoint exists
// and is asking for credentials, and nothing more. Only the live command,
// which skips without a token, can speak to whether the grant succeeds.
//
// The junk credentials are well-formed nonsense rather than empty strings.
// An empty one risks being rejected by a local length check before it ever
// reaches the wire, which would make this pass without the platform having
// been consulted -- the shape of failure acceptance-chat.sh's refusal check
// was originally written with and had to be rewritten to avoid.
func tokenRefusal(name string) {
	p := providerFor(name)
	ctx, cancel := ctx20()
	defer cancel()

	start := time.Now()
	tok, err := p.Refresh(ctx, "polyemesis-acceptance-client", "polyemesis-acceptance-secret", "polyemesis-acceptance-refresh-token")
	emit("elapsedMs", time.Since(start).Milliseconds())

	// A SUCCESS HERE IS A CATASTROPHE, not a pass. If a token endpoint minted
	// a token for that triple, the platform is handing tokens to anybody.
	if err == nil {
		emit("refused", false)
		emit("mintedAToken", tok != nil && tok.AccessToken != "")
		return
	}
	emit("refused", true)
	msg := err.Error()

	// tokenStatusError and statusError are both unexported, so their status
	// has to come back out of the formatted message. Both types carry a doc
	// comment recording that their wording is deliberately preserved for
	// exactly this kind of reader; if that ever stops being true, status comes
	// back -1 and every check below reports "no status could be read" rather
	// than inventing one.
	emit("status", statusFrom(msg))
	emit("transportError", isTransportError(msg))
	emit("error", msg)
	// The platform's own error token, which is what distinguishes "we do not
	// know that grant" from "we know that grant and your token is wrong".
	emit("oauthError", oauthErrorIn(msg))
}

var statusRe = regexp.MustCompile(`returned (\d{3})[:\s]`)

// statusFrom pulls an HTTP status out of an error message built by
// tokenStatusError ("token endpoint returned 401: ...") or statusError
// ("<url> returned 404: ..."). -1 means no status was present, which is a
// different answer from 0 and must not be confused with one.
func statusFrom(msg string) int {
	if m := statusRe.FindStringSubmatch(msg); m != nil {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			return n
		}
	}
	// getJSON writes its two classified branches differently, and those are the
	// package's own reading of the response rather than a raw code -- which is
	// the more interesting fact when it is available.
	if strings.Contains(msg, "rejected the access token (401)") {
		return 401
	}
	if strings.Contains(msg, "refused the request (403)") {
		return 403
	}
	return -1
}

// isTransportError reports whether the request never got an HTTP answer at
// all. A DNS failure and a 404 are both "the endpoint is not there" to a
// careless reader and completely different repairs to an operator.
func isTransportError(msg string) bool {
	for _, s := range []string{"no such host", "connection refused", "context deadline exceeded",
		"i/o timeout", "certificate", "TLS handshake", "EOF"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// oauthErrorIn names the platform's own refusal vocabulary. Each of these is a
// string observed coming back from that platform, not one from the RFC: Twitch
// answers "invalid client" with a space, Kick answers the RFC's invalid_grant,
// Facebook answers an OAuthException with a numeric code.
func oauthErrorIn(msg string) string {
	for _, s := range []string{"invalid_grant", "invalid_client", "unsupported_grant_type",
		"invalid client", "invalid_request", "OAuthException"} {
		if strings.Contains(msg, s) {
			return s
		}
	}
	return ""
}

// ----------------------------------------------------- grant differential

// grantDifferential asks the same token endpoint two questions and reports both
// answers: one with the grant polyemesis depends on, one with a grant that
// cannot exist.
//
// WITHOUT THE SECOND ANSWER THE FIRST MEANS NOTHING. "The token endpoint
// refused our refresh_token" is satisfied by a server that refuses everything,
// including a server that has quietly stopped serving refresh_token at all. It
// is only when a nonsense grant is refused DIFFERENTLY that the first refusal
// becomes evidence that refresh_token was recognised, routed, and rejected on
// its merits.
//
// This is the one credential-free way to say anything at all about the grant
// that produces the hour-four failure, and it works on two of the four
// platforms. Twitch and Facebook validate the client before they look at the
// grant, so both questions come back "invalid client" and the differential is
// unreachable without credentials. The suite skips them by name and says why
// rather than reporting a comparison it did not make.
//
// It talks to the URL directly rather than through the provider because
// Provider.Refresh has no way to send a grant_type polyemesis would never
// send. The URL is not retyped: it is read out of the provider's own AuthURL
// origin, so a moved authorization server moves this probe with it.
func grantDifferential(name string) {
	tokenPath := map[string]string{
		"youtube": "/token",
		"kick":    "/oauth/token",
	}[name]
	if tokenPath == "" {
		fail("no grant differential is available for %q", name)
	}
	// Google grants consent on accounts.google.com and mints on
	// oauth2.googleapis.com, so the token host cannot be taken from AuthURL
	// the way Kick's can. It is taken from Google's discovery document, which
	// is the same document the suite checks internal/oauth against -- so if
	// that comparison passes, this probe is aimed where internal/oauth aims.
	base := ""
	switch name {
	case "youtube":
		base = googleTokenBase()
	case "kick":
		u, err := url.Parse(providerFor("kick").AuthURL("c", "http://localhost/cb", "s", "ch"))
		if err != nil {
			fail("parsing Kick's AuthURL: %v", err)
		}
		base = u.Scheme + "://" + u.Host
	}
	if base == "" {
		emit("probed", false)
		return
	}
	endpoint := base + tokenPath
	emit("endpoint", endpoint)

	real := postGrant(endpoint, "refresh_token")
	bogus := postGrant(endpoint, "polyemesis_not_a_grant_type")
	emit("probed", true)
	emit("realStatus", real.status)
	emit("realError", orNone(real.oauthError))
	emit("bogusStatus", bogus.status)
	// "no error token at all" is Kick's answer to a grant it does not serve --
	// an empty 400 body -- and it has to read as an answer rather than as a
	// missing field, because it is half of the differential below.
	emit("bogusError", orNone(bogus.oauthError))
	// The verdict the suite asserts on: the two answers are not the same one.
	emit("differs", real.status != bogus.status || real.oauthError != bogus.oauthError)
}

type grantAnswer struct {
	status     int
	oauthError string
}

// orNone keeps an empty answer legible in the suite's output. The comparison
// that decides `differs` runs on the raw values, so this is presentation only.
func orNone(s string) string {
	if s == "" {
		return "(no OAuth error)"
	}
	return s
}

func postGrant(endpoint, grant string) grantAnswer {
	ctx, cancel := ctx20()
	defer cancel()
	form := url.Values{
		"grant_type":    {grant},
		"client_id":     {"polyemesis-acceptance-client"},
		"client_secret": {"polyemesis-acceptance-secret"},
		"refresh_token": {"polyemesis-acceptance-refresh-token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return grantAnswer{status: -1}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return grantAnswer{status: -1}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// An empty body is itself an answer -- it is how Kick refuses a grant it
	// does not serve -- so it is reported as the absence of an error token
	// rather than folded into one of the named ones.
	return grantAnswer{status: resp.StatusCode, oauthError: oauthErrorIn(string(body))}
}

// googleTokenBase reads Google's minting host out of its own discovery
// document. Nothing is hardcoded here: if Google moves it, the suite's
// endpoint comparison fails first and says so.
func googleTokenBase() string {
	ctx, cancel := ctx20()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL["google"], nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var d discoveryDoc
	if json.Unmarshal(body, &d) != nil || d.TokenEndpoint == "" {
		return ""
	}
	u, err := url.Parse(d.TokenEndpoint)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// ------------------------------------------------------------ api refusal

// apiRefusal drives Provider.Account against the real data API with a token
// that cannot work, and reports how polyemesis classified the refusal.
//
// THE ASSERTION IS ON POLYEMESIS'S READING, NOT ON THE RAW STATUS. getJSON has
// a branch that turns a 401 into "the platform rejected the access token
// (401); reconnect the account" -- the sentence an operator sees. A check that
// only looked at the wire would pass while that branch was broken, and the
// branch is the part a human acts on.
//
// A 401 here is decisive about the base URL because the alternative is
// observably different: every one of these hosts answers 404 to a path that is
// close but wrong (api.twitch.tv/helixXX, api.kick.com/publicXX,
// googleapis.com/youtubeXX all do), and getJSON reports a 404 as
// "<endpoint> returned 404", not as a rejected token. Facebook is the
// exception and the suite says so where it checks Facebook.
func apiRefusal(name string) {
	p := providerFor(name)
	ctx, cancel := ctx20()
	defer cancel()

	acct, err := p.Account(ctx, "polyemesis-acceptance-client", "polyemesis-acceptance-access-token")
	if err == nil {
		// Same reasoning as tokenRefusal: an identity returned for a junk
		// token is not a pass.
		emit("refused", false)
		emit("returnedAccount", acct != nil && acct.Ref != "")
		return
	}
	msg := err.Error()
	emit("refused", true)
	emit("status", statusFrom(msg))
	emit("transportError", isTransportError(msg))
	// The classified sentence, which is the thing the operator reads.
	emit("classifiedAsBadToken", classifiedAsBadToken(msg))
	emit("error", msg)
}

// classifiedAsBadToken reports whether polyemesis reached its own "this token
// is no good, reconnect the account" conclusion.
//
// TWO SENTENCES, because there are two classifiers. getJSON writes the first
// for the three providers that get a 401. Facebook never reaches it: Meta
// answers a malformed token with its own numbered envelope, requestJSON hands
// that to fbAdvice, and fbAdvice's code-190 branch writes the second sentence
// -- which says more, because it also tells the operator that a password change
// invalidates a token. Matching only the first would report Facebook's
// classifier as absent while it was working perfectly.
func classifiedAsBadToken(msg string) bool {
	return strings.Contains(msg, "rejected the access token (401)") ||
		strings.Contains(msg, "rejected the access token, so")
}

// ------------------------------------------------------------- fb version

// fbVersion checks that the Graph API version facebook.go pins is still the
// version Facebook serves.
//
// THIS IS THE SILENT BREAK IN THIS PACKAGE THAT NOTHING ELSE COULD SEE. Meta
// retires Graph versions on a schedule, and it does not answer a retired one
// with an error: a request to v3.0 is served, successfully, by v20.0, and the
// only place the substitution is visible is the facebook-api-version response
// header. facebook.go pins v24.0 with a comment saying "a broadcast that starts
// working differently on a Tuesday is not a failure mode worth having" -- and
// that pin stops being a pin, silently, on the day v24.0 is retired.
//
// THE CONTROL IS WHAT MAKES IT NON-VACUOUS. A header check alone would pass
// against a server that echoed whatever was asked for. So a version that
// cannot exist is requested too, and the header must come back DIFFERENT. If
// both answers match what was asked, the header is an echo and proves nothing.
//
// The pinned version is read out of the package rather than typed here: the
// Graph base comes back inside statusError's message from a real Account call,
// which is the same string production logs.
func fbVersion() {
	ctx, cancel := ctx20()
	defer cancel()

	// AN EMPTY ACCESS TOKEN, DELIBERATELY, and it is the opposite of the choice
	// tokenRefusal makes -- for a reason worth writing down.
	//
	// What is needed here is the URL the provider built, and the only place it
	// survives is inside statusError's message. A malformed token gets Meta's
	// code 190, fbAdvice recognises 190 and replaces the error with operator
	// advice that does not quote the URL. An empty one gets code 2500, which
	// fbAdvice does not recognise, so it returns the statusError untouched and
	// the Graph base -- pinned version and all -- comes back with it.
	//
	// That makes this check depend on fbAdvice NOT growing a 2500 branch. If it
	// ever does, baseFound comes back false and the suite says the base could
	// not be read, rather than silently checking nothing.
	_, err := providerFor("facebook").Account(ctx, "polyemesis-acceptance-client", "")
	if err == nil {
		emit("baseFound", false)
		emit("error", "Facebook accepted a junk access token")
		return
	}
	base := graphBaseIn(err.Error())
	if base == "" {
		emit("baseFound", false)
		emit("error", err.Error())
		return
	}
	emit("baseFound", true)
	emit("graphBase", base)
	pinned := versionIn(base)
	emit("pinnedVersion", pinned)

	emit("servedVersion", servedGraphVersion(base+"/me"))
	// A version that cannot exist, to prove the header is a real answer rather
	// than an echo of the request.
	emit("controlVersion", servedGraphVersion(strings.Replace(base, pinned, "v99.0", 1)+"/me"))
	emit("controlRequested", "v99.0")
}

var graphBaseRe = regexp.MustCompile(`(https://[^/\s]*facebook[^/\s]*/v\d+\.\d+)`)
var versionRe = regexp.MustCompile(`/(v\d+\.\d+)`)

func graphBaseIn(msg string) string {
	if m := graphBaseRe.FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	return ""
}

func versionIn(base string) string {
	if m := versionRe.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	return ""
}

// servedGraphVersion returns the version Facebook says it actually served,
// which is not necessarily the one that was asked for.
func servedGraphVersion(endpoint string) string {
	ctx, cancel := ctx20()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) //nolint:errcheck // draining for reuse
	return resp.Header.Get("facebook-api-version")
}

// ------------------------------------------------------------------ live

// live is the credentialed half, and it skips rather than fails without a
// credential -- everything above is this suite's floor and runs everywhere.
//
// IT IS THE ONLY THING THAT CAN SPEAK TO THE HOUR-FOUR FAILURE. Every check
// above establishes that a token endpoint is present and refuses a bad grant.
// None of them establishes that a good one succeeds, and the gap between those
// two is exactly where a refresh that has quietly stopped working lives.
//
// Four facts are reported, and the last two are the ones #312 argues for:
//
//   - the refresh returned a token at all
//   - its expiry is far enough ahead to be a real expiry rather than a
//     already-stale one
//   - the refreshed token is accepted by the data API, so it is a working
//     credential and not merely a well-formed string
//   - Ingest yields a publish URL and a key, which is the field polyemesis
//     fills in automatically and the one whose hand-typed equivalent shipped
//     a preset that could not publish
//
// NOTHING SECRET IS PRINTED. The stream key is reported as a length and a
// character-class verdict; the ingest URL is reported as its scheme and host
// with the path dropped, because some platforms put the key in the path. The
// access token is never emitted in any form. redact() is the backstop.
func live(name string) {
	up := strings.ToUpper(name)
	clientID := os.Getenv("POLY_OAUTH_" + up + "_CLIENT_ID")
	clientSecret := os.Getenv("POLY_OAUTH_" + up + "_CLIENT_SECRET")
	refresh := os.Getenv("POLY_OAUTH_" + up + "_REFRESH_TOKEN")
	if clientID == "" || clientSecret == "" || refresh == "" {
		emit("skipped", true)
		return
	}
	emit("skipped", false)
	p := providerFor(name)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tok, err := p.Refresh(ctx, clientID, clientSecret, refresh)
	if err != nil {
		emit("refreshed", false)
		emit("error", err.Error())
		return
	}
	emit("refreshed", true)
	emit("accessTokenLen", len(tok.AccessToken))
	emit("gotRefreshToken", tok.RefreshToken != "")
	// SECONDS OF LIFE, not a boolean. A refresh that hands back a token
	// expiring in ninety seconds is technically a success and practically the
	// hour-four failure arriving early, so the number is reported and the
	// suite decides.
	if tok.ExpiresAt.IsZero() {
		emit("expiresInSec", -1)
	} else {
		emit("expiresInSec", int(time.Until(tok.ExpiresAt).Seconds()))
	}
	// The scopes the platform says the refreshed token carries, compared
	// against what this build asks for. A platform that quietly dropped one is
	// the AccountNeedsReconnect case arriving from the other direction.
	granted := strings.Fields(strings.ReplaceAll(tok.Scopes, ",", " "))
	var missing []string
	for _, want := range p.Scopes() {
		if !contains(granted, want) {
			missing = append(missing, want)
		}
	}
	emit("scopesReported", len(granted))
	emit("scopesMissing", strings.Join(missing, " "))

	acct, err := p.Account(ctx, clientID, tok.AccessToken)
	if err != nil {
		emit("accountOK", false)
		emit("accountError", err.Error())
	} else {
		emit("accountOK", acct != nil && acct.Ref != "")
		emit("accountNameLen", len(acct.Name))
	}

	ing, err := p.Ingest(ctx, clientID, tok.AccessToken)
	if err != nil {
		emit("ingestOK", false)
		emit("ingestError", err.Error())
		return
	}
	emit("ingestOK", true)
	// Scheme and host only. The path is dropped because more than one platform
	// puts the stream key in it, and a suite that leaked a key while checking
	// that keys are handled correctly would be its own bug report.
	if u, perr := url.Parse(ing.URL); perr == nil {
		emit("ingestScheme", u.Scheme)
		emit("ingestHost", u.Host)
	}
	emit("ingestKeyLen", len(ing.Key))
	// A key is opaque and platform-specific, so the only universal statement
	// worth making is that it is printable and has no whitespace -- which is
	// what #306 turned out to be about, a stored spelling and a wire spelling
	// that were allowed to differ.
	emit("ingestKeyClean", ing.Key != "" && ing.Key == strings.TrimSpace(ing.Key) && !strings.ContainsAny(ing.Key, " \t\r\n"))
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
