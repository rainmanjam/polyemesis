package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// YouTube implements Google OAuth 2.0 plus the Live Streaming API lookup that
// turns a connected account into an ingest URL and stream key.
type YouTube struct {
	// endpoints carries the base URLs; zero value is production. See
	// endpoints.go.
	endpoints
	// vodRetry replaces the archive-availability wait in youtube_vod.go. Nil
	// means ytArchiveRetryDelays, which is what production uses -- nothing at
	// runtime assigns this, for the same reason nothing at runtime calls
	// WithBaseURL. A test sets it to zero delays so the retry path can be
	// driven without a real minute of waiting, which is the difference between
	// that path being covered and being hoped about.
	vodRetry []time.Duration
}

// Google splits what other platforms combine: consent is granted on
// accounts.google.com, tokens are minted on oauth2.googleapis.com, and the data
// API is a third host. All three are the authorization/data split endpoints.go
// describes, so WithBaseURL moves all three together.
const (
	ytConsentBase = "https://accounts.google.com"
	ytTokenBase   = "https://oauth2.googleapis.com"
)

// apiEndpoint is the YouTube Data API base for THIS provider. Account and
// Ingest previously wrote https://www.googleapis.com/youtube/v3 inline while
// the rest of the package went through the ytAPIBase var, so a test that
// redirected the var still sent those two calls to Google.
func (y *YouTube) apiEndpoint() string { return y.apiBase(ytAPIBase) }

func (y *YouTube) Platform() db.Platform { return db.PlatformYouTube }

// Scopes: youtube.readonly is not enough, because creating a liveStream (which
// we do when the channel has none) is a write.
func (y *YouTube) Scopes() []string {
	return []string{"https://www.googleapis.com/auth/youtube"}
}

// PKCE is on: Google documents code_challenge/code_challenge_method for web
// server applications, i.e. exactly this flow, and enforces the verifier at the
// token endpoint. It costs nothing and it means a leaked authorization code —
// through a proxy log, a referrer header, a shared machine's history — is
// useless to anyone but this process.
// ScopeVersion 1 is the single youtube scope above. Bump whenever Scopes
// changes; see the Provider interface for why this is a hand-bumped integer
// rather than a diff of what the platform granted.
func (y *YouTube) ScopeVersion() int { return 1 }

func (y *YouTube) PKCE() bool { return true }

func (y *YouTube) AuthURL(clientID, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(y.Scopes(), " "))
	q.Set("state", state)
	// access_type=offline is what gets us a refresh token at all; without it
	// the connection dies an hour later and the user has to reconnect.
	q.Set("access_type", "offline")
	// Google only re-issues a refresh token when consent is re-granted, so a
	// user reconnecting an account would otherwise end up with an access token
	// and no way to renew it.
	q.Set("prompt", "consent")
	q.Set("include_granted_scopes", "true")
	if challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	return y.authBase(ytConsentBase) + "/o/oauth2/v2/auth?" + q.Encode()
}

func (y *YouTube) Exchange(ctx context.Context, clientID, clientSecret, redirectURI, code, verifier string) (*Token, error) {
	form := url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	return postForm(ctx, y.authBase(ytTokenBase)+"/token", form, nil)
}

func (y *YouTube) Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*Token, error) {
	t, err := postForm(ctx, y.authBase(ytTokenBase)+"/token", url.Values{
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"refresh_token"},
	}, nil)
	if err != nil {
		return nil, err
	}
	// A refresh response omits refresh_token; carrying the old one forward is
	// what keeps the connection alive indefinitely.
	if t.RefreshToken == "" {
		t.RefreshToken = refreshToken
	}
	return t, nil
}

func (y *YouTube) Account(ctx context.Context, clientID, accessToken string) (*Account, error) {
	var out struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	err := getJSON(ctx,
		y.apiEndpoint()+"/channels?part=snippet&mine=true",
		accessToken, nil, &out)
	if err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, fmt.Errorf("this Google account has no YouTube channel; create one and reconnect")
	}
	return &Account{Name: out.Items[0].Snippet.Title, Ref: out.Items[0].ID}, nil
}

type ytLiveStream struct {
	ID      string `json:"id"`
	Snippet struct {
		Title string `json:"title"`
	} `json:"snippet"`
	CDN struct {
		IngestionType string `json:"ingestionType"`
		Resolution    string `json:"resolution"`
		FrameRate     string `json:"frameRate"`
		IngestionInfo struct {
			StreamName          string `json:"streamName"`
			IngestionAddress    string `json:"ingestionAddress"`
			BackupIngestionAddr string `json:"backupIngestionAddress"`
		} `json:"ingestionInfo"`
	} `json:"cdn"`
}

// Ingest returns the channel's reusable RTMP ingest, creating one if the
// channel has never streamed.
//
// This is the payoff of the OAuth flow: the user never sees, copies or
// mistypes a stream key.
func (y *YouTube) Ingest(ctx context.Context, clientID, accessToken string) (*Ingest, error) {
	s, err := y.reusableStream(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	return &Ingest{
		URL: s.CDN.IngestionInfo.IngestionAddress,
		Key: s.CDN.IngestionInfo.StreamName,
	}, nil
}

// reusableStream finds or creates the channel's reusable RTMP stream and
// returns the WHOLE resource, id included.
//
// The zero IngestOptions is what makes this the SAME function it always was:
// no held key to match, no dedicated stream asked for, so streamFor takes the
// first reusable RTMP stream on the channel exactly as this did. Provider.Ingest
// has nowhere to carry an option, so it stays on this path for good.
//
// Split out of Ingest rather than copied beside it because the scheduled-broadcast
// path needs the stream's ID as well as its key: liveBroadcasts.bind takes a
// streamId, and Ingest discarded it. Written twice, the two would be two chances
// to pick different streams -- the encoder publishing to one and the broadcast
// bound to another -- and nothing would say so, because both calls succeed and
// the failure only shows up as a scheduled show that stays dark. Binding to the
// SAME reusable stream is documented as allowed: "A broadcast can only be bound
// to one video stream, though a video stream may be bound to more than one
// broadcast" (liveBroadcasts/bind, read 2026-08-16), and it is what keeps a
// destination's stream key stable across a pre-announce.
func (y *YouTube) reusableStream(ctx context.Context, accessToken string) (*ytLiveStream, error) {
	return y.streamFor(ctx, accessToken, IngestOptions{})
}

// streamFor picks the liveStream ONE DESTINATION should publish to, which is
// not always the channel's reusable one.
//
// THE THREE ANSWERS, IN THE ORDER THEY ARE TRIED, AND THE ORDER IS THE WHOLE
// DESIGN:
//
//  1. The stream this destination is ALREADY publishing with, matched on
//     opts.HeldKey. It wins over everything below, including
//     opts.DedicatedIngest, because a key that changes under a running
//     configuration is the one outcome this change is not allowed to have:
//     somebody has that key pasted into OBS right now. It also means the
//     destination holding the channel's shared stream keeps holding it however
//     the caller's first/not-first arithmetic comes out later -- deleting a
//     neighbour cannot re-point an established destination.
//  2. The channel's existing reusable RTMP stream, when nothing dedicated was
//     asked for. Today's behaviour, unchanged, for the destination the caller
//     nominated as the account's first.
//  3. A NEW stream, titled for the destination. This is the fix: its key is
//     its own, so the broadcast it feeds is its own ingestion source rather
//     than the fourth tenant of somebody else's.
//
// A HELD KEY THE CHANNEL NO LONGER LISTS FALLS THROUGH, and that is deliberate
// rather than an oversight to fix later. The stream was deleted in Studio, or
// the account was reconnected to a different channel; either way the key in the
// encoder is already dead, so re-provisioning is the only outcome available and
// arriving at it silently beats failing a refresh the operator pressed exactly
// because something was wrong.
//
// NOTHING HERE COUNTS ANYTHING. It does not ask how many streams the channel
// has, or how many broadcasts are live, and it must not learn to: YouTube
// publishes neither ceiling, so any pre-flight check would be enforcing an
// invented number. The refusal is handled where it arrives -- see
// ytBroadcastCreateAdvice and RefusalSharedIngestionFull.
func (y *YouTube) streamFor(ctx context.Context, accessToken string, opts IngestOptions) (*ytLiveStream, error) {
	streams, err := y.listStreams(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	// Byte for byte, with no trimming and no case folding. This is the
	// destination's stored key being compared against the platform's spelling
	// of the same key, and #306 is what a "helpful" normalisation between
	// those two costs.
	// The held key wins UNLESS the operator asked for a new one and this
	// destination is meant to have a stream of its own. That combination is
	// the only way an established key ever moves -- see IngestOptions.RotateKey
	// for why it has to exist at all.
	if opts.HeldKey != "" && !(opts.RotateKey && opts.DedicatedIngest) {
		for i := range streams {
			if streams[i].CDN.IngestionInfo.StreamName == opts.HeldKey {
				return &streams[i], nil
			}
		}
	}

	// TAKING "THE FIRST RTMP STREAM ON THE CHANNEL" USED TO BE SAFE AND IS NOT
	// ANY MORE, AND THIS GUARD IS THE DIFFERENCE.
	//
	// Before destinations had streams of their own, a channel held at most one
	// polyemesis stream, so "the first one" and "ours" were the same object.
	// Now the channel fills up with streams belonging to OTHER destinations,
	// and the first listed is very likely one of them.
	//
	// The case that bites is an error path, which is why it was missed: a
	// destination holding a key whose stream has since been deleted in Studio.
	// Its HeldKey matches nothing, it falls through here, and it adopts a
	// sibling's stream -- rotating its key onto a stream another destination is
	// already publishing to. Two destinations, one ingestion source: the exact
	// defect this whole change exists to remove, recreated by the recovery
	// path. It was measured, not theorised.
	//
	// So the shared stream is only for a destination that has never held a key.
	// One that held a key and cannot find it gets a new stream, which is the
	// honest reading of its situation: whatever it was bound to is gone.
	if !opts.DedicatedIngest && opts.HeldKey == "" {
		// Prefer an existing reusable RTMP stream; that is the one the creator's
		// scheduled broadcasts are already bound to.
		for i := range streams {
			if strings.EqualFold(streams[i].CDN.IngestionType, "rtmp") &&
				streams[i].CDN.IngestionInfo.StreamName != "" {
				return &streams[i], nil
			}
		}
	}

	return y.createStream(ctx, accessToken, opts.IngestLabel)
}

// listStreams reads the channel's liveStreams.
func (y *YouTube) listStreams(ctx context.Context, accessToken string) ([]ytLiveStream, error) {
	var list struct {
		Items []ytLiveStream `json:"items"`
	}
	err := getJSON(ctx,
		// part=id was ADDED to a shipping path, and it is worth naming why the
		// risk is asymmetric. The scheduled path needs the stream's id to bind
		// a broadcast to it; without id in the part list there is no documented
		// guarantee the field comes back. "id" is not in the evidence file for
		// THIS resource, but it is evidenced for a sibling
		// (part=id,snippet,status on liveBroadcasts.list), and an extra part is
		// additive -- it cannot remove snippet or cdn, which is what this call
		// already depended on. Resolve by: one live liveStreams.list without
		// part=id, recording whether id is present.
		y.apiEndpoint()+ytStreamsPath+"?part=id,snippet,cdn&mine=true&maxResults=50",
		accessToken, nil, &list)
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

const (
	// ytStreamTitleBase is what every stream polyemesis creates is called, so an
	// operator scanning YouTube Studio can see at a glance which streams are
	// ours.
	ytStreamTitleBase = "polyemesis"
	// ytStreamTitleMax is DOCUMENTED rather than chosen, which is the only
	// reason a number appears in this file at all. The liveStreams create
	// caveats in docs/evidence/platform-lifecycle-apis-2026-08-16.md, read
	// 2026-08-16: "Title 1-128 chars, description <= 10000 chars." Contrast the
	// concurrency ceilings in the same file, which are a support transcript and
	// are deliberately enforced nowhere.
	//
	// It is CHARACTERS, so the cut is by rune. Cutting 128 bytes out of a
	// destination named in Japanese would send a title ending in half a
	// character.
	ytStreamTitleMax = 128
)

// ytStreamTitle names a created stream after the destination that will publish
// to it.
//
// THE NAME IS FOR A HUMAN IN YOUTUBE STUDIO, and that is the whole requirement:
// a channel with five streams on it all called "polyemesis" is unmanageable,
// and the destination's name is the only string polyemesis holds that the
// operator chose themselves. It is display metadata, never an identifier --
// nothing matches on it, so a rename in either place breaks nothing.
//
// NO SECRET GOES IN IT. The caller passes a destination NAME; it must never
// pass a key, and streamFor never passes opts.HeldKey here.
//
// THE LABEL IS WHAT GETS CUT, never the base, so an over-long name still
// produces a title that reads as polyemesis's. A cut title is worth more than a
// refused create: the alternative to trimming is a required field YouTube
// rejects, and the operator would be told their destination's NAME broke a
// stream key fetch.
// YouTubeStreamTitle is ytStreamTitle for callers outside this package.
//
// EXPORTED FOR ONE READER, NOT FOR A WRITER: internal/api names the stream a
// deleted destination leaves behind so an operator can find it in Studio, and
// the name it prints has to be the name that was actually sent. Two spellings of
// this rule would mean telling somebody to look for a title that does not exist.
// It computes a display string and touches nothing.
func YouTubeStreamTitle(label string) string { return ytStreamTitle(label) }

func ytStreamTitle(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ytStreamTitleBase
	}
	title := ytStreamTitleBase + " - " + label
	if r := []rune(title); len(r) > ytStreamTitleMax {
		title = string(r[:ytStreamTitleMax])
	}
	return title
}

// createStream provisions a new reusable RTMP stream, and therefore a NEW
// STREAM KEY.
//
// Every caller has to have decided it wants one. Called where a destination
// already has a working key, this is a rotation an operator did not ask for --
// which is why streamFor tries opts.HeldKey before it gets here.
func (y *YouTube) createStream(ctx context.Context, accessToken, label string) (*ytLiveStream, error) {
	// Create a reusable variable-resolution stream. variable/variable
	// is what lets polyemesis pass through whatever OBS is sending without
	// YouTube rejecting a resolution mismatch.
	created := ytLiveStream{}
	payload := map[string]any{
		"snippet": map[string]any{"title": ytStreamTitle(label)},
		"cdn": map[string]any{
			"ingestionType": "rtmp",
			"resolution":    "variable",
			"frameRate":     "variable",
		},
	}
	err := postJSON(ctx,
		y.apiEndpoint()+ytStreamsPath+"?part=id,snippet,cdn",
		accessToken, payload, nil, &created)
	if err != nil {
		return nil, fmt.Errorf("could not create a YouTube ingest stream: %w", err)
	}
	if created.CDN.IngestionInfo.StreamName == "" {
		return nil, fmt.Errorf("YouTube created a stream but returned no stream key")
	}
	return &created, nil
}

// --------------------------------------------------------------------- stats

// The capability is resolved by type assertion in StatsFor, so a drifting
// signature would make YouTube silently stop reporting viewers rather than fail
// to build. See twitch.go for the same guard and the drift test that catches
// the other direction.
var _ LiveStatter = (*YouTube)(nil)

const (
	ytBroadcastsPath = "/liveBroadcasts"
	ytVideosPath     = "/videos"
	ytStreamsPath    = "/liveStreams"
	// ytBindPath is liveBroadcasts.bind, which is a SEPARATE method rather than
	// a parameter on the create: a broadcast and the stream that feeds it are
	// two resources and joining them is a third call.
	ytBindPath = ytBroadcastsPath + "/bind"
)

// ytConcurrentViewers decodes a field whose documented type and whose wire type
// disagree, and neither spelling may be assumed.
//
// The videos resource representation says `concurrentViewers: unsigned long`.
// Google's JSON convention serialises 64-bit values as QUOTED STRINGS, because
// a uint64 does not survive a JavaScript number -- so the byte on the wire is
// widely `"concurrentViewers": "1312"` rather than `1312`. The reference states
// the logical type; it does not state the encoding, and nothing read on
// 2026-08-16 settles which arrives.
//
// So this accepts both rather than betting on one. Betting wrong does not
// degrade gracefully: json.Unmarshal fails the WHOLE videos response on a type
// mismatch, so a viewer count would take the title, the start time and the
// liveness answer down with it. Accepting both costs a type and cannot fail.
//
// Absent stays absent. This is only reached when the key is present, and the
// caller distinguishes that -- see LiveStats.ViewerCount.
type ytConcurrentViewers struct {
	value   int
	present bool
}

func (c *ytConcurrentViewers) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		// A count we cannot parse is a count we were not given. Failing here
		// would fail the entire read for one advisory number.
		return nil
	}
	c.value, c.present = n, true
	return nil
}

// Stats reads the channel's live broadcast and how many people are watching it.
//
// TWO CALLS, AND THE FIRST ONE EXISTS BECAUSE POLYEMESIS STORES NO VIDEO ID.
// The viewer count lives on the video resource, keyed by video id, and nothing
// in the database holds one. liveBroadcasts.list is what turns a token into
// that id. The join is documented and is the load-bearing fact of this method,
// verbatim from the Live Streaming API overview (read 2026-08-16): "In fact,
// the liveBroadcast resource and the video resource share the same ID." Cite
// that page rather than the liveBroadcasts resource, whose own gloss is weaker
// and does not state the identity.
//
// broadcastType=all IS SENT DELIBERATELY. Verbatim: "The broadcastType
// parameter filters the API response to only include broadcasts with the
// specified type. The parameter should be used in requests that set the mine
// parameter to true or that use the broadcastStatus parameter. The default
// value is event." The default therefore returns only scheduled event
// broadcasts and would hide a persistent one, reporting a live channel as dark.
//
// broadcastStatus and mine are MUTUALLY EXCLUSIVE -- "Filters (specify exactly
// one of the following parameters)" -- so ownership cannot be asked for in the
// same breath as liveness. "owned by the authenticated user" appears exactly
// once on that page, in the mine row, and nothing scopes active to the caller.
// Whether broadcastStatus=active can return somebody else's broadcast is
// therefore UNVERIFIED, and the channelId filter below is the defence: it costs
// one comparison and the alternative is reporting a stranger's audience as
// yours.
//
// NO QUOTA NUMBER APPEARS HERE ON PURPOSE. The string "quota" occurs zero times
// in the liveBroadcasts.list reference, so its cost is undocumented; videos.list
// documents 1 unit. The project-wide ceiling is 10,000 units per day shared
// across every YouTube feature polyemesis has -- metadata push and compliance
// included -- so a caller polling this hard does not merely slow the viewer
// count down, it takes title push down with it. The refusal to handle is
// quotaExceeded; a hardcoded interval derived from a guessed cost would be the
// invented number this whole evidence pass exists to prevent.
func (y *YouTube) Stats(ctx context.Context, clientID, accessToken string) (*LiveStats, error) {
	acct, err := y.Account(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}

	var broadcasts struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				ChannelID       string `json:"channelId"`
				Title           string `json:"title"`
				ActualStartTime string `json:"actualStartTime"`
			} `json:"snippet"`
		} `json:"items"`
	}
	err = getJSON(ctx,
		y.apiEndpoint()+ytBroadcastsPath+"?part=id,snippet&broadcastStatus=active&broadcastType=all",
		accessToken, nil, &broadcasts)
	if err != nil {
		return nil, err
	}

	stats := &LiveStats{Source: ytBroadcastsPath}

	// len first, never items[0] blind. Nothing in the reference states what an
	// idle channel returns -- neither the Errors table nor the Response section
	// says so -- and an empty items array is the structurally natural reading
	// rather than a documented one. Indexing on that reading would panic on the
	// day it is wrong.
	var id, title, startedAt string
	for _, b := range broadcasts.Items {
		if b.Snippet.ChannelID != "" && b.Snippet.ChannelID != acct.Ref {
			continue
		}
		id, title, startedAt = b.ID, b.Snippet.Title, b.Snippet.ActualStartTime
		break
	}
	if id == "" {
		// Not live. An answer, not an error -- the same contract every other
		// provider here holds.
		return stats, nil
	}

	stats.Live = true
	stats.Title = title
	stats.StartedAt = parseYouTubeTime(startedAt)

	var videos struct {
		Items []struct {
			LiveStreamingDetails struct {
				ConcurrentViewers ytConcurrentViewers `json:"concurrentViewers"`
				ActualStartTime   string              `json:"actualStartTime"`
			} `json:"liveStreamingDetails"`
		} `json:"items"`
	}
	err = getJSON(ctx,
		y.apiEndpoint()+ytVideosPath+"?part=liveStreamingDetails&id="+url.QueryEscape(id),
		accessToken, nil, &videos)
	if err != nil {
		// THE BROADCAST IS STILL LIVE AND WE STILL KNOW IT. The first call
		// already answered the question that matters; losing the second costs a
		// number, and returning an error here would throw away a correct
		// liveness answer to report the failure of an advisory one. Kick makes
		// the same trade for the same reason.
		return stats, nil
	}
	if len(videos.Items) == 0 {
		return stats, nil
	}

	d := videos.Items[0].LiveStreamingDetails
	// A MISSING KEY IS "UNKNOWN" AND NEVER ZERO, and YouTube omits it under
	// three conditions that are indistinguishable from each other: no current
	// viewers, the owner has HIDDEN the viewcount, and after the broadcast
	// ends. An operator who hid their count would otherwise be shown an
	// audience of none on a stream people are watching.
	if d.ConcurrentViewers.present {
		v := d.ConcurrentViewers.value
		stats.ViewerCount = &v
	}
	// The video resource's actualStartTime is the better source when both
	// answer: it is the broadcast's own record rather than the listing's.
	if at := parseYouTubeTime(d.ActualStartTime); at != nil {
		stats.StartedAt = at
	}
	stats.Source = ytVideosPath
	return stats, nil
}

// parseYouTubeTime reads the one format YouTube commits to. liveStreamingDetails
// timestamps are documented as ISO 8601, which RFC 3339 parses for the subset
// Google emits; an unreadable stamp costs the timestamp rather than the read,
// and nil keeps it out of the payload entirely rather than sending the year 1.
func parseYouTubeTime(s string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return &parsed
}
