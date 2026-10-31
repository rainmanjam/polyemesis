// Package oauthlive is the credentialed step of scripts/acceptance-oauth.sh:
// refresh a real token, and ask the platform what that token can do.
//
// WHY IT IS A PACKAGE AND NOT PART OF THE DRIVER. The driver is a
// `//go:build ignore` file, so nothing can link it and no test can call it.
// That mattered here because the step had a side effect its own header denied:
// it ended by calling Provider.Ingest, and Facebook's Ingest is
// POST /{node}/live_videos with status=LIVE_NOW. Every run against a real
// Facebook account left a real live video on it. Moved here, the rule "the
// default run creates nothing" is something a test can hold the code to.
//
// WHAT EACH CALL DOES TO THE ACCOUNT, read from internal/oauth:
//
//   - Refresh: a token grant. It writes no account content, but KICK ROTATES
//     REFRESH TOKENS, and the rotated one is never printed, so one run spends
//     the POLY_OAUTH_KICK_REFRESH_TOKEN it was given. Refreshing is what this
//     step exists to measure, so it is not gated.
//   - Account, Stats, StreamKeys: GETs.
//   - Ingest is the one that differs per platform -- see policyFor.
//
// Nothing here prints a credential. The caller's Emit is expected to redact,
// and nothing secret is handed to it: the stream key is reported as a length
// and a character-class verdict, the ingest URL as its scheme and host.
package oauthlive

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
)

// AllowBroadcastFlag is the driver flag that opts in to the one step that
// creates something. Named here so the skip message and the driver cannot
// disagree about its spelling.
const AllowBroadcastFlag = "allow-broadcast"

// AllowBroadcastUsage is the flag's help text. It has to say what is created,
// where, that other people can see it, and what is done about it afterwards,
// because the person reading it is deciding whether to run this against their
// own account.
const AllowBroadcastUsage = "create a REAL Facebook live video (POST /me/live_videos, status=LIVE_NOW) " +
	"on the token owner's own profile, to read the ingest URL and stream key Facebook issues for it. " +
	"It is a public broadcast object on that profile, visible to its audience once video arrives. " +
	"The driver ends it (end_live_video=true) before exiting and reports the id; ending is not deleting, " +
	"so an ended, empty live video may remain on the profile. " +
	"Without this flag no platform is asked to create anything. Affects Facebook only."

// Ingest modes, as emitted in ingestMode.
const (
	// ModeRead: Provider.Ingest is a pure read on this platform.
	ModeRead = "read"
	// ModeKeys: the stream keys are listed instead of calling Ingest, because
	// Ingest would create a stream when the channel has none.
	ModeKeys = "keys"
	// ModeBroadcast: the ingest only exists by creating a live video, and the
	// operator opted in to that.
	ModeBroadcast = "broadcast"
	// ModeSkipped: nothing was asked for.
	ModeSkipped = "skipped"
)

// Creds is one platform's credential set, read from the environment by the
// caller.
type Creds struct {
	ClientID, ClientSecret, RefreshToken string
}

func (c Creds) complete() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.RefreshToken != ""
}

// Options are the operator's choices for one run.
type Options struct {
	// AllowBroadcast permits creating a Facebook live video. See
	// AllowBroadcastUsage.
	AllowBroadcast bool
}

// Emit prints one key=value result line. The driver's implementation redacts.
type Emit func(k string, v any)

// policyFor says how the ingest may be read on a platform when nothing has
// been allowed. AN ALLOWLIST: a platform not named here was never audited, so
// it gets no Ingest call at all rather than inheriting one.
//
//   - Twitch: Account (GET /users) then GET /streams/key. A read.
//   - Kick: GET /public/v1/channels; the key is a field on it. A read.
//   - YouTube: liveStreams.list, then liveStreams.insert when the channel has
//     no reusable RTMP stream -- a new stream and a new key the operator did
//     not ask for. StreamKeys is the list without the insert.
//   - Facebook: POST /{node}/live_videos on every call. There is no read.
func policyFor(p db.Platform) string {
	switch p {
	case db.PlatformTwitch, db.PlatformKick:
		return ModeRead
	case db.PlatformYouTube:
		return ModeKeys
	case db.PlatformFacebook:
		return ModeBroadcast
	default:
		return ModeSkipped
	}
}

// facebookBroadcaster is the create/end pair, asserted rather than imported as
// *oauth.Facebook so a test can stand in for it.
type facebookBroadcaster interface {
	IngestFor(ctx context.Context, clientID, accessToken, targetRef string, opts oauth.IngestOptions) (*oauth.Broadcast, error)
	EndBroadcast(ctx context.Context, accessToken, targetRef, liveVideoID string) (*oauth.BroadcastEnd, error)
}

// cleanupTimeout bounds the end call. It runs on its own context, not the
// run's: the run's deadline may be spent by the time the create returns, and
// a cleanup that failed instantly on a dead context would leave the video up.
const cleanupTimeout = 30 * time.Second

// Run is the live step. It skips, emitting skipped=true, without a complete
// credential set.
func Run(ctx context.Context, p oauth.Provider, c Creds, opt Options, emit Emit) {
	if !c.complete() {
		emit("skipped", true)
		return
	}
	emit("skipped", false)

	tok, err := p.Refresh(ctx, c.ClientID, c.ClientSecret, c.RefreshToken)
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

	acct, err := p.Account(ctx, c.ClientID, tok.AccessToken)
	if err != nil {
		emit("accountOK", false)
		emit("accountError", err.Error())
	} else {
		emit("accountOK", acct != nil && acct.Ref != "")
		emit("accountNameLen", len(acct.Name))
	}

	stats(ctx, p, c.ClientID, tok.AccessToken, emit)
	ingest(ctx, p, c.ClientID, tok.AccessToken, opt, emit)
}

// stats is the one place a real stats body is read. Every stats test in
// internal/oauth answers from a stub whose body this repo wrote, so all of them
// prove the decoder matches the fixture and none prove the fixture matches the
// platform. Liveness is reported, not asserted; the shape is.
//
// Asserted on p rather than resolved through oauth.StatsFor, which would look
// up the PACKAGE singleton and ignore the provider this run was given.
func stats(ctx context.Context, p oauth.Provider, clientID, accessToken string, emit Emit) {
	st, ok := p.(oauth.LiveStatter)
	if !ok {
		emit("statsOK", "unsupported")
		return
	}
	s, err := st.Stats(ctx, clientID, accessToken)
	if err != nil {
		emit("statsOK", false)
		emit("statsError", err.Error())
		return
	}
	emit("statsOK", true)
	emit("statsLive", s.Live)
	// Absent and zero are DIFFERENT, so absent is a word rather than a 0.
	if s.ViewerCount == nil {
		emit("statsViewers", "not-reported")
	} else {
		emit("statsViewers", strconv.Itoa(*s.ViewerCount))
	}
	emit("statsSource", s.Source)
}

// ingest reads the #312 field -- the URL and key polyemesis fills in for the
// operator -- in whichever way policyFor allows.
func ingest(ctx context.Context, p oauth.Provider, clientID, accessToken string, opt Options, emit Emit) {
	mode := policyFor(p.Platform())
	switch mode {
	case ModeRead:
		emit("ingestMode", ModeRead)
		ing, err := p.Ingest(ctx, clientID, accessToken)
		if err != nil {
			emit("ingestOK", false)
			emit("ingestError", err.Error())
			return
		}
		emit("ingestOK", true)
		emitIngest(*ing, emit)

	case ModeKeys:
		r, ok := p.(oauth.StreamKeyReader)
		if !ok {
			skip(emit, "this provider has no read-only stream key list")
			return
		}
		emit("ingestMode", ModeKeys)
		keys, err := r.StreamKeys(ctx, clientID, accessToken)
		if err != nil {
			emit("ingestOK", false)
			emit("ingestError", err.Error())
			return
		}
		if len(keys) == 0 {
			// Ingest would insert a stream here. That is a new key on the
			// operator's channel, so it is not done, with or without the flag.
			skip(emit, "the channel has no live stream yet, and fetching an ingest would create one")
			return
		}
		emit("ingestOK", true)
		emit("ingestKeyCount", len(keys))
		emit("ingestKeyLen", len(keys[0]))
		clean := true
		for _, k := range keys {
			clean = clean && keyClean(k)
		}
		emit("ingestKeyClean", clean)

	case ModeBroadcast:
		b, ok := p.(facebookBroadcaster)
		if !opt.AllowBroadcast || !ok {
			skip(emit, "reading a Facebook ingest creates a live video on the account; pass -"+
				AllowBroadcastFlag+" to create one and end it afterwards")
			return
		}
		emit("ingestMode", ModeBroadcast)
		broadcast(ctx, b, clientID, accessToken, emit)

	default:
		skip(emit, "this platform's ingest call has not been audited for side effects")
	}
}

func skip(emit Emit, reason string) {
	emit("ingestMode", ModeSkipped)
	emit("ingestOK", "skipped")
	emit("ingestSkipReason", reason)
}

// createdIDRe pulls a live video id out of IngestFor's error text. IngestFor
// can fail AFTER its POST succeeded -- "created live video <id> but returned no
// ingest URL" -- and then returns no Broadcast, so the id exists only there.
var createdIDRe = regexp.MustCompile(`live video (\d+)`)

// broadcast creates one Facebook live video, reads its ingest, and ends it.
//
// IngestFor, not Ingest: Ingest discards the live video id, and without the id
// there is nothing to end. The end is deferred, so a failure anywhere after the
// create still reaches it.
func broadcast(ctx context.Context, b facebookBroadcaster, clientID, accessToken string, emit Emit) {
	br, err := b.IngestFor(ctx, clientID, accessToken, "", oauth.IngestOptions{})
	id, target := "", ""
	if br != nil {
		id, target = br.ID, br.Target
	} else if err != nil {
		if m := createdIDRe.FindStringSubmatch(err.Error()); m != nil {
			id = m[1]
		}
	}
	if id != "" {
		emit("createdLiveVideo", id)
		defer end(b, accessToken, target, id, emit)
	}

	if err != nil {
		emit("ingestOK", false)
		emit("ingestError", err.Error())
		if id == "" {
			// The refusal may have come before the create or after it, and
			// nothing in the error says which. A person has to look.
			emit("possibleStray", true)
			emit("cleanupOK", false)
			emit("cleanupError", "no live video id was returned; check the profile's live videos and delete any left by this run")
		}
		return
	}
	emit("ingestOK", true)
	emitIngest(br.Ingest, emit)
}

func end(b facebookBroadcaster, accessToken, target, id string, emit Emit) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	res, err := b.EndBroadcast(ctx, accessToken, target, id)
	if err != nil {
		emit("cleanupOK", false)
		emit("cleanupError", "could not end live video "+id+"; end or delete it by hand: "+err.Error())
		return
	}
	emit("cleanupOK", true)
	emit("cleanupEnded", res.Ended)
	emit("cleanupStatus", res.Status)
	emit("cleanupWarning", strings.Join(res.Warnings, "; "))
}

// emitIngest reports an ingest without its secret parts. Scheme and host only:
// more than one platform puts the stream key in the path.
func emitIngest(ing oauth.Ingest, emit Emit) {
	if u, err := url.Parse(ing.URL); err == nil {
		emit("ingestScheme", u.Scheme)
		emit("ingestHost", u.Host)
	}
	emit("ingestKeyLen", len(ing.Key))
	emit("ingestKeyClean", keyClean(ing.Key))
}

// keyClean is the only universal statement worth making about an opaque key:
// it is present and carries no whitespace -- which is what #306 was about, a
// stored spelling and a wire spelling that were allowed to differ.
func keyClean(k string) bool {
	return k != "" && k == strings.TrimSpace(k) && !strings.ContainsAny(k, " \t\r\n")
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
