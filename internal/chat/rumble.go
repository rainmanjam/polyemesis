package chat

// Rumble live chat by polling the live-stream API.
//
// Rumble is the fifth platform here and the first that is neither OAuth nor a
// webhook. Its live-stream API is a single unauthenticated GET whose entire
// credential is a key in the query string, issued from the operator's own
// account settings at rumble.com/account/livestream-api rather than from a
// partner programme. That is what made it reachable where TikTok, Instagram and
// LinkedIn are not: there is nobody to be approved by.
//
// WHAT WAS MEASURED, and what was not. The endpoint is real and key-gated: an
// invalid key answers 403 with a structured body, and no key at all answers 400
// with "No access token found in the request" -- a live service refusing a
// caller, not a dead URL. The payload shape below is Rumble's own published
// example (rumble.support, "Rumble's Live Stream API", last updated 20 Nov
// 2025). Nothing here has been run against a real key on a live broadcast,
// because obtaining one requires a Rumble account login. So the transport and
// the refusal path are verified; the field-by-field fidelity of a live payload
// is documented rather than observed. The capability row says exactly that.
//
// THREE PROPERTIES OF THIS API DRIVE THE WHOLE DESIGN:
//
//  1. It is POLL-ONLY. There is no socket, no webhook, no long poll. Every
//     response is a snapshot of "the most recent 50" -- so consecutive polls
//     overlap heavily and the Hub's dedupe is load-bearing, not a nicety.
//
//  2. Messages carry NO ID. Rumble sends username, badges, text and
//     created_on, and nothing that identifies the message. Message.Normalise
//     synthesises a content-derived id, which is what makes the overlap in (1)
//     collapse correctly. The cost is recorded honestly at deliver below: two
//     identical messages from one person inside the same second are
//     indistinguishable to us and the second is dropped.
//
//  3. The response body CONTAINS THE STREAM KEY. livestreams[].stream_key
//     rides in the same JSON as the chat. That is the #310 shape exactly -- a
//     secret arriving somewhere nobody expected one -- so this file never
//     decodes that field at all, and scrub below covers the API key on every
//     path out of the adapter.
//
// The rate limit is UNDOCUMENTED. Rumble publishes none, and five requests in
// close succession drew no 429 and no Retry-After. Absence of a published limit
// is not absence of a limit, so the poll interval is deliberately conservative
// and floored: see rumbleMinPoll, which exists so that a bad config cannot turn
// this adapter into the thing that gets an operator's IP blocked.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// RumbleAPIBase is Rumble's live-stream API. Overridden in tests.
const RumbleAPIBase = "https://rumble.com/-livestream-api"

// RumbleChatKeyEnv is the environment variable the live-stream API key is read
// from, and the ONLY supported way to supply it.
//
// An environment variable rather than a flag, a config field or a database
// column, and the reason is worth stating: a flag is argv, argv is world
// readable in ps on every machine this runs on, and a key every local user can
// read is a key that has leaked. It is not a stored credential either -- there
// is no OAuth account row for Rumble to hang it off, because this API has no
// sign-in to hang one off of.
//
// Named here rather than in internal/api because the adapter's own "not
// configured" message has to name it, and internal/chat cannot import the
// package that wires it up.
const RumbleChatKeyEnv = "RUMBLE_CHAT_API_KEY"

const (
	// rumbleDefaultPoll is how often a live chat is read. Rumble publishes no
	// rate limit, so this is chosen rather than derived: fast enough that chat
	// feels live, slow enough that a day-long broadcast is six requests a
	// minute against an endpoint whose tolerance nobody has told us.
	rumbleDefaultPoll = 10 * time.Second
	// rumbleMinPoll is the floor no configuration can go under.
	//
	// This is the whole reason the interval is clamped rather than trusted. A
	// misconfigured poll of 100ms is not a slow-chat bug, it is ten requests a
	// second at an endpoint with an unknown and unpublished limit, from an
	// operator's home IP, for hours. The clamp is cheap; finding out where the
	// limit was by crossing it is not.
	rumbleMinPoll = 5 * time.Second
	// rumbleMaxPoll is where the idle backoff stops. A chat nobody is talking
	// in still has to notice the next message within a minute.
	rumbleMaxPoll = 60 * time.Second
	// rumbleOfflinePoll is the cadence while nothing is live. Chat cannot exist
	// without a broadcast, so this is only watching for one to start.
	rumbleOfflinePoll = 30 * time.Second
	// rumbleDefaultBackfill bounds what the FIRST poll delivers.
	//
	// Every response carries the most recent 50 messages, so without this the
	// pane opens by replaying however long those 50 span -- possibly an hour --
	// as if it had all just arrived. Five minutes is context; the rest is a
	// flood. This mirrors ytDefaultBackfill, and for the same reason.
	rumbleDefaultBackfill = 5 * time.Minute
)

// rumbleKeyPlaceholder is what a scrubbed key is replaced with. It is visibly
// not a key, so a log line containing it reads as "a secret was removed here"
// rather than as a corrupted value.
const rumbleKeyPlaceholder = "[rumble-api-key-redacted]"

// RumbleConfig configures the polling adapter.
type RumbleConfig struct {
	AccountRef string
	// Channel is the display name for the tab header.
	Channel string
	// Key is the live-stream API key.
	//
	// It MUST reach this struct from an environment variable and never from a
	// command line: argv is world-readable in ps on every machine this runs on,
	// and a key visible to every local user is a key that has leaked. See
	// RumbleChatKeyEnv, which is the only name anything reads it from.
	Key string
	// Poll overrides the base interval. Clamped to [rumbleMinPoll, rumbleMaxPoll].
	Poll time.Duration
	// Backfill bounds the first poll's history.
	Backfill time.Duration

	APIBase string
	HTTP    *http.Client
	Now     func() time.Time
	Sleep   func(context.Context, time.Duration) bool
}

// RumbleAdapter reads one Rumble live chat.
//
// Read-only, deliberately. It implements Adapter and Healther and nothing else:
// Rumble's live-stream API is a single GET that returns data, and no endpoint
// for posting a message or removing one has been verified to exist. Declaring a
// Sender or a Deleter that returned "not supported" would put a reply box and a
// delete button in front of an operator that cannot work -- the compile-time
// assertions in chat.go exist precisely so a capability claim and its
// implementation cannot drift apart, and the honest answer here is to claim
// neither.
type RumbleAdapter struct {
	cfg RumbleConfig

	mu     sync.Mutex
	health Health
}

// NewRumble builds the adapter. A missing key is a named "not configured"
// state rather than a crash, as everywhere else in this package.
func NewRumble(cfg RumbleConfig) (*RumbleAdapter, error) {
	if strings.TrimSpace(cfg.Key) == "" {
		return nil, fmt.Errorf("rumble chat is not configured: no live-stream API key, so set %s "+
			"to the key from rumble.com/account/livestream-api", RumbleChatKeyEnv)
	}
	cfg.Key = strings.TrimSpace(cfg.Key)
	if cfg.APIBase == "" {
		cfg.APIBase = RumbleAPIBase
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.Backfill <= 0 {
		cfg.Backfill = rumbleDefaultBackfill
	}
	return &RumbleAdapter{
		cfg:    cfg,
		health: Health{State: StateConnecting},
	}, nil
}

func (r *RumbleAdapter) Platform() db.Platform { return db.PlatformRumble }
func (r *RumbleAdapter) Account() string       { return r.cfg.AccountRef }

func (r *RumbleAdapter) Health() Health {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.health
}

// setHealth records the adapter's own view of itself.
//
// detail is scrubbed on the way in rather than at each call site. Every string
// that reaches it today is built from our own words, but Health.Detail is
// rendered in the UI and copied into logs, and "no caller will ever pass an API
// response through here" is the assumption that put a stream key in server.log
// in #310. One chokepoint is cheaper than trusting every future caller.
func (r *RumbleAdapter) setHealth(state State, detail string) {
	r.mu.Lock()
	r.health = Health{State: state, Detail: r.scrub(detail)}
	r.mu.Unlock()
}

// pollInterval is the configured base, clamped. See rumbleMinPoll for why the
// clamp is not negotiable.
func (r *RumbleAdapter) pollInterval() time.Duration {
	d := r.cfg.Poll
	if d <= 0 {
		d = rumbleDefaultPoll
	}
	if d < rumbleMinPoll {
		return rumbleMinPoll
	}
	if d > rumbleMaxPoll {
		return rumbleMaxPoll
	}
	return d
}

// ------------------------------------------------------------- the payload

// rumbleChatEntry is one chat message or one rant.
//
// Rumble sends the same four fields for both, plus an amount on a rant, so one
// struct reads both lists. There is NO id field here because Rumble sends none;
// see the note at the top of this file about what that costs.
type rumbleChatEntry struct {
	Username  string   `json:"username"`
	Badges    []string `json:"badges"`
	Text      string   `json:"text"`
	CreatedOn string   `json:"created_on"`
	// AmountCents is set on a rant and zero on an ordinary message. Cents
	// rather than dollars because the dollars field is a rounded-down integer
	// in Rumble's own example, and $1.50 reported as $1 is worse than no
	// figure at all.
	AmountCents int `json:"amount_cents"`
}

// rumbleLivestream is one broadcast.
//
// Deliberately partial. Rumble's response also carries stream_key, likes,
// dislikes, watching_now, categories and an id; none of them are decoded. The
// stream_key omission is the important one and is not an oversight: a field
// that is never unmarshalled cannot be accidentally logged, put in an error, or
// serialised into a health detail by some later change. See #310.
type rumbleLivestream struct {
	Title  string `json:"title"`
	IsLive bool   `json:"is_live"`
	Chat   struct {
		RecentMessages []rumbleChatEntry `json:"recent_messages"`
		RecentRants    []rumbleChatEntry `json:"recent_rants"`
	} `json:"chat"`
}

// rumbleResponse is the subset of get-data this adapter reads.
//
// The follower, subscriber and gifted-sub blocks are ignored: they are
// alert-overlay material rather than chat, and decoding them would mean
// modelling five more shapes for data nothing here renders.
type rumbleResponse struct {
	Livestreams []rumbleLivestream `json:"livestreams"`
}

// ------------------------------------------------------------- the loop

// Run polls the live chat until ctx ends.
//
// Like the YouTube adapter, it stays inside this loop across "nothing is live
// yet" rather than returning an error and letting the Hub reconnect. Waiting
// for a broadcast is a normal state on a platform whose chat cannot exist
// without one, and reporting it as a failure would make the account list read
// as broken every morning before the operator goes live.
func (r *RumbleAdapter) Run(ctx context.Context, sink Sink) error {
	var (
		first = true
		wait  = r.pollInterval()
	)

	for ctx.Err() == nil {
		resp, err := r.fetch(ctx)
		if err != nil {
			return r.classify(err)
		}

		live, ok := liveStream(resp)
		if !ok {
			r.setHealth(StateDegraded,
				"waiting for a Rumble live stream to start; chat begins when it does")
			if !r.cfg.Sleep(ctx, rumbleOfflinePoll) {
				return nil
			}
			// Re-arm the backfill window, so a SECOND broadcast is bounded the
			// same way the first was.
			//
			// Without this the window is a once-per-process thing: an operator
			// who ends a stream and starts another an hour later would have the
			// new stream's first poll arrive with no cutoff at all, and the
			// pane would open by replaying that whole recent-50 window as if it
			// had just been said. The overnight case -- attached at 9pm, live at
			// 8am -- does not need this line, because a poll that finds nothing
			// live never consumed the window in the first place.
			first = true
			continue
		}

		cutoff := time.Time{}
		if first {
			cutoff = r.cfg.Now().Add(-r.cfg.Backfill)
			first = false
		}

		delivered := r.deliver(live, cutoff, sink)
		r.setHealth(StateLive, "")

		// Back off while nobody is talking and snap back the moment they are.
		// A silent chat polled every ten seconds for eight hours is 2,880
		// requests that told us nothing.
		if delivered == 0 {
			if wait *= 2; wait > rumbleMaxPoll {
				wait = rumbleMaxPoll
			}
		} else {
			wait = r.pollInterval()
		}
		if !r.cfg.Sleep(ctx, wait) {
			return nil
		}
	}
	return nil
}

// fetch performs one poll.
//
// The key rides in the query string because that is the only authentication
// this API has -- there is no header form. doJSON's apiError records
// stripQuery(endpoint) rather than the endpoint, which is what keeps the key
// out of the error text; scrub in classify is the second layer, so that a
// change to either one alone cannot re-open the leak.
func (r *RumbleAdapter) fetch(ctx context.Context) (rumbleResponse, error) {
	endpoint := r.cfg.APIBase + "/get-data?key=" + url.QueryEscape(r.cfg.Key)
	var out rumbleResponse
	// No TokenFunc: this API takes no Authorization header, and passing one
	// would send the key twice rather than once.
	err := doJSON(ctx, r.cfg.HTTP, http.MethodGet, endpoint, nil, nil, &out)
	return out, err
}

// liveStream picks the broadcast whose chat to read.
//
// Rumble returns an ARRAY, and the array is empty or the entries are not live
// when nothing is on air. Only a live entry is used: the chat block on a
// finished stream is unpopulated, and treating an is_live:false entry as a live
// one would report the pane as connected while it silently received nothing.
func liveStream(resp rumbleResponse) (rumbleLivestream, bool) {
	for _, ls := range resp.Livestreams {
		if ls.IsLive {
			return ls, true
		}
	}
	return rumbleLivestream{}, false
}

// deliver hands one poll's chat to the sink and reports how many were new
// enough to show. cutoff is zero on every poll after the first.
//
// DEDUPLICATION IS THE HUB'S, AND IT IS NOT OPTIONAL HERE. Every response
// repeats the last 50 messages, so at a ten-second poll the overlap is nearly
// total. The Hub keys on Message.Key, and because Rumble sends no message id
// that key comes from Message.Normalise's content hash over
// platform+account+author+text+timestamp.
//
// The cost of that, stated plainly rather than discovered later: Rumble's
// created_on has one-second resolution, so if one person sends the same text
// twice inside the same second the two messages hash identically and the pane
// shows one. That is a real if rare loss. The alternative -- keying on the
// message's index within the array -- is worse in a way that is not rare at
// all: the index of a given message shifts on every poll as newer ones arrive,
// so every message would get a fresh id each time and the pane would repeat the
// entire chat every ten seconds.
func (r *RumbleAdapter) deliver(ls rumbleLivestream, cutoff time.Time, sink Sink) int {
	delivered := 0
	// Rants first is deliberate: they arrive in a separate list, and a paid
	// message is the one an operator most wants to see. The Hub sorts by At.
	for _, e := range ls.Chat.RecentRants {
		if m, ok := r.messageFrom(e, ls); ok && !before(m.At, cutoff) {
			delivered++
			sink.Deliver(m)
		}
	}
	for _, e := range ls.Chat.RecentMessages {
		if m, ok := r.messageFrom(e, ls); ok && !before(m.At, cutoff) {
			delivered++
			sink.Deliver(m)
		}
	}
	return delivered
}

// before reports whether t is inside a non-zero cutoff. A zero cutoff means
// "no window", which is every poll after the first.
func before(t, cutoff time.Time) bool {
	return !cutoff.IsZero() && t.Before(cutoff)
}

// messageFrom normalises one Rumble chat entry.
//
// ID is left EMPTY on purpose so Message.Normalise synthesises the content
// hash. Inventing an id here would put the same decision in two places and
// guarantee they eventually disagree.
func (r *RumbleAdapter) messageFrom(e rumbleChatEntry, ls rumbleLivestream) (Message, bool) {
	if strings.TrimSpace(e.Text) == "" {
		return Message{}, false
	}

	at, err := time.Parse(time.RFC3339, e.CreatedOn)
	if err != nil {
		// An unparseable timestamp becomes now rather than dropping the
		// message. A line rendered a few seconds late is better than a line the
		// operator never sees because Rumble changed a date format.
		at = r.cfg.Now()
	}

	badges := make([]Badge, 0, len(e.Badges)+1)
	for _, b := range e.Badges {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		// ID is Rumble's own token, so applyBadgeRoles in message.go can map
		// the ones it recognises -- "admin" to moderator, "premium" to
		// subscriber -- without this file duplicating that table.
		badges = append(badges, Badge{ID: b, Label: rumbleBadgeLabel(b)})
	}
	if e.AmountCents > 0 {
		// A rant is Rumble's paid message. Marking it as a badge is how the
		// pane can render it differently, and the amount is in the label
		// because a paid message with no figure on it is just a message.
		badges = append(badges, Badge{
			ID:    "rant",
			Label: fmt.Sprintf("Rant $%d.%02d", e.AmountCents/100, e.AmountCents%100),
		})
	}
	if len(badges) == 0 {
		badges = nil
	}

	return Message{
		Platform: db.PlatformRumble,
		Account:  r.cfg.AccountRef,
		Channel:  firstNonEmpty(r.cfg.Channel, ls.Title),
		Text:     e.Text,
		At:       at,
		Author: Author{
			// Rumble identifies a chatter by username and sends no numeric id,
			// so the username IS the identity available. Recorded because it is
			// weaker than every other adapter here: a renamed account is a new
			// author as far as polyemesis can tell.
			ID:   e.Username,
			Name: e.Username,
			// Badges carry the roles. applyBadgeRoles fills Moderator and
			// Subscriber from them during Normalise, so setting the flags here
			// as well would mean two sources for one answer.
			Badges: badges,
		},
	}, true
}

// rumbleBadgeLabel gives Rumble's badge tokens a human name where one is
// obvious, and falls back to the token itself.
//
// The fallback matters more than the table: Rumble ships badges this list has
// never seen (its own example includes "whale-blue"), and an unrecognised badge
// should render as itself rather than vanish.
func rumbleBadgeLabel(id string) string {
	switch strings.ToLower(id) {
	case "admin":
		return "Admin"
	case "moderator":
		return "Moderator"
	case "premium":
		return "Premium"
	case "locals", "locals_supporter":
		return "Locals supporter"
	case "recurring_subscription":
		return "Subscriber"
	case "verified":
		return "Verified"
	default:
		return id
	}
}

// ------------------------------------------------------------- failure

// classify turns a failed poll into either a fatal error or a retryable one.
//
// THE FATAL SET IS THE POINT OF THIS FUNCTION. A key the operator revoked, or
// mistyped, answers 403 forever. Retrying it on the Hub's schedule means a
// request every thirty seconds, from a home IP, at an endpoint whose rate limit
// is unpublished -- which is the twitch.go fatalNotice lesson in a different
// spelling, and how an IP gets banned for a mistake that a sentence in the UI
// would have fixed in ten seconds.
//
// Everything else falls through as retryable, including 429 and 5xx. That is
// the fail-open direction and it is chosen: a status this function has never
// seen is far more likely to be a bad afternoon at Rumble than a permanently
// rejected credential, and treating an outage as fatal takes the chat pane down
// until somebody restarts polyemesis.
func (r *RumbleAdapter) classify(err error) error {
	switch statusOf(err) {
	case http.StatusBadRequest:
		// Measured: a request with no key at all answers 400 with
		// "No access token found in the request". Retrying an empty credential
		// is retrying nothing.
		r.setHealth(StateFailed,
			"Rumble rejected the request as having no API key; check the key from rumble.com/account/livestream-api")
		return Fatal(errors.New("rumble rejected the live-stream API request as carrying no key; " +
			"set " + RumbleChatKeyEnv + " to the key from rumble.com/account/livestream-api"))

	case http.StatusUnauthorized, http.StatusForbidden:
		// Measured: an invalid key answers 403 with a structured body. Rumble
		// lets an operator reset the URL to revoke access, so a key that worked
		// yesterday and 403s today is the expected shape of a revocation.
		r.setHealth(StateFailed,
			"Rumble refused the live-stream API key; it may have been reset. Issue a new one at rumble.com/account/livestream-api")
		return Fatal(errors.New("rumble refused the live-stream API key (it may have been reset or revoked); " +
			"issue a new one at rumble.com/account/livestream-api and update " + RumbleChatKeyEnv))
	}

	// Retryable. The platform's own words are useful here and the key must not
	// be in them: scrub is the second layer behind doJSON's stripQuery, so that
	// a response body echoing the key back cannot carry it into a log.
	return fmt.Errorf("rumble live-stream API: %s", r.scrub(err.Error()))
}

// scrub removes the API key from text on its way out of this adapter.
//
// This is the #310 lesson applied before it can happen again rather than after.
// There, a refused destination wrote its stream key to server.log on every
// retry because one sink was covered and its sibling was not -- so here every
// path that produces operator-visible text goes through this function, not just
// the one that looked risky.
//
// It is not a general secret scanner and does not pretend to be: it removes
// this adapter's own key, which is the only secret this adapter holds.
func (r *RumbleAdapter) scrub(s string) string {
	if r.cfg.Key == "" || s == "" {
		return s
	}
	s = strings.ReplaceAll(s, r.cfg.Key, rumbleKeyPlaceholder)
	// Also the percent-encoded spelling, because the key travels through
	// url.QueryEscape on its way into the request and an error built from a URL
	// would carry that spelling rather than the raw one. #306 is the same
	// mistake in the other direction: the stored spelling of a key and the
	// spelling that reached the wire were allowed to differ.
	if enc := url.QueryEscape(r.cfg.Key); enc != r.cfg.Key {
		s = strings.ReplaceAll(s, enc, rumbleKeyPlaceholder)
	}
	return s
}
