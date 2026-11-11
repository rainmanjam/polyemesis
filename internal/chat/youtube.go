package chat

// YouTube live chat by polling liveChatMessages.list.
//
// The quota is the entire design problem — see quota.go for the pacer. This
// file is the loop that obeys it: it charges every call to the budget, honours
// the API's own pollingIntervalMillis as a floor, backs off hard while the
// chat is idle, and when the allowance really is gone it stops and says so
// with the reset time rather than hammering an endpoint that will only return
// 403 for the rest of the day.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/internal/backoff"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// YouTubeMaxMessage is the published limit on a live chat message, enforced by
// Send and mirrored in the platform registry (see KickMaxMessage).
const YouTubeMaxMessage = 200

const (
	// ytBroadcastPoll is how often we look for a broadcast to attach to when
	// there is none, and ytBroadcastPollMax is where that backs off to. Each
	// look costs a unit, so a machine left connected overnight must not spend
	// the morning's quota discovering that nobody is live.
	ytBroadcastPoll    = 30 * time.Second
	ytBroadcastPollMax = 5 * time.Minute
	// ytPauseCheck is how often a quota-paused adapter re-examines the clock.
	// Sleeping straight through to the reset would ignore a context that ends
	// in between.
	ytPauseCheck = time.Minute
	// ytClockLayout is how a quota reset time is shown to an operator: a wall
	// clock with its zone, because "resets at 00:00 PDT" is actionable and an
	// RFC 3339 stamp is not.
	ytClockLayout = "15:04 MST"
	// ytDefaultBackfill bounds what the first poll delivers. YouTube's first
	// page carries the recent history of the chat, and replaying an hour of it
	// into the pane as if it had just arrived is disorienting — five minutes is
	// context, not a flood.
	ytDefaultBackfill = 5 * time.Minute
)

// YouTubeConfig configures the polling adapter.
type YouTubeConfig struct {
	AccountRef string
	// Channel is the display name for the tab header.
	Channel string
	Token   TokenFunc
	// LiveChatID skips discovery when the caller already knows it — the
	// go-live path does, because it just created the broadcast.
	LiveChatID string
	// QuotaUnits is the project's daily allowance. Operators who have been
	// granted more should say so here; the pacer is only as good as this
	// number. QuotaReserve is the slice of it held back for sends.
	//
	// poka-yoke: the two distinct types make writing them into this literal the
	// wrong way round a compile error [control]
	//
	// They are adjacent, they are both counts of quota units, and a swap turns
	// a 10,000-unit allowance with a 200 reserve into a 200-unit allowance --
	// which clampQuota then strips the reserve from, leaving chat pacing a
	// hundred times too slowly with nothing anywhere saying so. That is #732,
	// reachable from a two-line edit. See QuotaUnits in quota.go.
	QuotaUnits   QuotaUnits
	QuotaReserve QuotaReserve
	// Quota, when set, is the Google client's shared allowance (quota_pool.go):
	// every adapter on one client ID draws on it, with the vertical read, the
	// audience sampler and the request meter. QuotaUnits and QuotaReserve are
	// then the pool's to apply, and only a test builds an adapter without one.
	Quota *YouTubeQuota

	APIBase string
	HTTP    *http.Client
	Now     func() time.Time
	Sleep   func(context.Context, time.Duration) bool
}

// YouTubeAdapter reads (and writes) one YouTube live chat.
type YouTubeAdapter struct {
	cfg    YouTubeConfig
	budget *budget

	mu     sync.Mutex
	health Health
	chatID string
	// wake ends a wait for a broadcast early; SetLiveChatID sends on it. Buffered one and sent without blocking.
	wake chan struct{}
	// endedID is the chat that last finished and endedAt when; see Run.
	endedID string
	endedAt time.Time
	// act is the activity decoder's state: what it has seen, when it began.
	act ytActivity
	// tallies is the last count each poll message was seen with in a list page;
	// see PollTally.
	tallies map[string]ytTally
}

// NewYouTube builds the adapter. As everywhere else here, a missing token is a
// "not configured" state with a named cause rather than a crash.
func NewYouTube(cfg YouTubeConfig) (*YouTubeAdapter, error) {
	if cfg.Token == nil {
		return nil, fmt.Errorf("youtube chat is not configured: no access token, so connect the YouTube account in Settings → Platforms")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = platform.YouTubeAPIRoot
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = backoff.SleepCtx
	}
	// THE QUOTA PAIR IS NOT NORMALISED HERE. It used to be -- an unset
	// allowance became DefaultQuotaUnits and an unset reserve became
	// DefaultQuotaReserve on the way past -- and that was a second, quieter
	// copy of clampQuota sitting upstream of the real one. The two disagreed:
	// the stored pair (10,000, 0) got a 200-unit reserve through this
	// constructor and no reserve at all through setLimits, so an operator's
	// ability to reply on stream vanished the first time they pressed Save
	// without changing anything. clampQuota is now the only place that decides
	// what an acceptable pair is, and newBudget below is how this constructor
	// asks it.
	return &YouTubeAdapter{
		cfg:    cfg,
		budget: adapterBudget(cfg),
		chatID: cfg.LiveChatID,
		wake:   make(chan struct{}, 1),
		health: Health{State: StateConnecting},
	}, nil
}

func (y *YouTubeAdapter) Platform() db.Platform { return db.PlatformYouTube }
func (y *YouTubeAdapter) Account() string       { return y.cfg.AccountRef }

func (y *YouTubeAdapter) Health() Health {
	y.mu.Lock()
	h := y.health
	y.mu.Unlock()
	q := y.budget.status()
	h.Quota = &q
	return h
}

func (y *YouTubeAdapter) setHealth(state State, detail string) {
	y.mu.Lock()
	y.health = Health{State: state, Detail: detail}
	y.mu.Unlock()
}

// Run discovers the live chat and polls it.
//
// It stays inside this loop across "there is no broadcast yet", rather than
// returning an error and letting the Hub reconnect, because the Hub's backoff
// tops out at thirty seconds and every rediscovery costs a quota unit. Waiting
// for a broadcast is a normal state, not a failure.
func (y *YouTubeAdapter) Run(ctx context.Context, sink Sink) error {
	idleFor := ytBroadcastPoll
	// The chat that last finished (y.endedID) is remembered for ytEndedHold. A broadcast can stay listed as active
	// after its chat has ended or been disabled, so rediscovering the same id soon after means "nothing new", not
	// "a chat to poll". It is NOT remembered for ever, and an explicit SetLiveChatID clears it: a chat pointed at
	// before its broadcast was live answers "not found", counts as ended, and was then refused again when the
	// broadcast went live (found live on staging: chat waited 7 minutes beside a live broadcast).
	for ctx.Err() == nil {
		chatID, err := y.liveChatID(ctx)
		if err != nil {
			return err
		}
		if chatID != "" && y.recentlyEnded(chatID) {
			y.mu.Lock()
			y.chatID = ""
			y.mu.Unlock()
			chatID = ""
		}
		if chatID == "" {
			y.setHealth(StateDegraded, "waiting for a YouTube broadcast to go live; chat starts when it does")
			if !y.sleepOrWake(ctx, idleFor) {
				return nil
			}
			// CLAMPED AFTER DOUBLING, not merely gated before it. Testing
			// `idleFor < max` and then doubling lets the last step overshoot:
			// from 30s the sequence is 30, 60, 120, 240, then 480 -- because
			// 240 is under the 300s ceiling, so it doubles past it and settles
			// there. The constant is called ytBroadcastPollMax and the comment
			// on it says the backoff "stops at" that value; the loop settled
			// 60% higher, and a broadcast going live was discovered up to eight
			// minutes later instead of five.
			//
			// Neither test that guarded this could see it: one divides a day by
			// the constant, the other compares the constant with its opening
			// value. Both are arithmetic ABOUT the loop rather than observation
			// OF it, and both passed throughout.
			if idleFor < ytBroadcastPollMax {
				idleFor *= 2
				if idleFor > ytBroadcastPollMax {
					idleFor = ytBroadcastPollMax
				}
			}
			continue
		}
		idleFor = ytBroadcastPoll
		y.mu.Lock()
		y.endedID = ""
		y.mu.Unlock()

		if err := y.pump(ctx, chatID, sink); err != nil {
			return err
		}
		y.mu.Lock()
		y.endedID, y.endedAt = chatID, y.cfg.Now()
		y.mu.Unlock()
		// The chat ended. Forget the id so the next round rediscovers, in case
		// the operator starts a second broadcast without restarting anything.
		y.mu.Lock()
		y.chatID = ""
		y.mu.Unlock()
		if ctx.Err() != nil {
			return nil
		}
		// Wait before rediscovering: the broadcast list can still show the
		// chat that just ended, and looping straight back spent the day's
		// quota in a second.
		// Wakeable: a SetLiveChatID (the coordinator pointing at the broadcast that is now live) must not wait this out.
		if !y.sleepOrWake(ctx, idleFor) {
			return nil
		}
		idleFor = min(idleFor*2, ytBroadcastPollMax)
	}
	return nil
}

// ytEndedHold is how long a chat that finished is not read again when discovery lists it again.
const ytEndedHold = 3 * time.Minute

// recentlyEnded reports whether id is the chat that finished less than ytEndedHold ago.
func (y *YouTubeAdapter) recentlyEnded(id string) bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.endedID == id && y.cfg.Now().Sub(y.endedAt) < ytEndedHold
}

// SetLiveChatID points the adapter at a chat, and "" lets it find one again. The go-live path calls it with the
// liveChatId from the insert response of the broadcast polyemesis created, so chat reads from that broadcast
// rather than from whichever active one discovery lists first, and an adapter that is waiting starts at once
// instead of after its backoff (up to five minutes).
func (y *YouTubeAdapter) SetLiveChatID(id string) {
	y.mu.Lock()
	y.chatID = strings.TrimSpace(id)
	y.endedID = "" // an explicit pointing overrides "this chat ended": the caller knows the broadcast is live now
	y.mu.Unlock()
	select {
	case y.wake <- struct{}{}:
	default:
	}
}

// LiveChat reports whether a live chat is open: the adapter holds a chat id,
// which it only does while a broadcast is active (it forgets it when the chat
// ends and while it waits for the next).
func (y *YouTubeAdapter) LiveChat() bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.chatID != ""
}

// sleepOrWake is cfg.Sleep that SetLiveChatID can cut short. It reports false only when the sleep itself says to
// stop (its context ended) and nobody woke it.
func (y *YouTubeAdapter) sleepOrWake(ctx context.Context, d time.Duration) bool {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var woke atomic.Bool
	go func() {
		select {
		case <-y.wake:
			woke.Store(true)
			cancel()
		case <-sctx.Done():
		}
	}()
	if y.cfg.Sleep(sctx, d) {
		return true
	}
	return woke.Load() && ctx.Err() == nil
}

// liveChatID returns the cached id, or finds the active broadcast's.
func (y *YouTubeAdapter) liveChatID(ctx context.Context) (string, error) {
	y.mu.Lock()
	id := y.chatID
	y.mu.Unlock()
	if id != "" {
		return id, nil
	}

	var out struct {
		Items []struct {
			Snippet struct {
				LiveChatID string `json:"liveChatId"`
				Title      string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	endpoint := y.cfg.APIBase + "/liveBroadcasts?part=snippet&broadcastStatus=active&broadcastType=all&maxResults=5"
	err := doJSON(ctx, y.cfg.HTTP, http.MethodGet, endpoint, y.cfg.Token, nil, &out)
	y.budget.spend(QuotaCostListBroadcasts)
	if err != nil {
		return "", y.classify(err)
	}
	for _, it := range out.Items {
		if it.Snippet.LiveChatID != "" {
			y.mu.Lock()
			y.chatID = it.Snippet.LiveChatID
			y.mu.Unlock()
			return it.Snippet.LiveChatID, nil
		}
	}
	return "", nil
}

// ytChatItem is one message as the API returns it.
type ytChatItem struct {
	ID      string `json:"id"`
	Snippet struct {
		Type           string `json:"type"`
		PublishedAt    string `json:"publishedAt"`
		DisplayMessage string `json:"displayMessage"`
		// PollDetails is present on a poll message. The reference names the
		// type "pollDetails" in one place and "pollEvent" in another, so a poll
		// is recognised by this object rather than by the type string.
		// UserBannedDetails is present on a userBannedEvent: a moderator (this
		// app, another moderator or Studio) banned someone.
		UserBannedDetails *struct {
			BannedUserDetails struct {
				ChannelID string `json:"channelId"`
			} `json:"bannedUserDetails"`
		} `json:"userBannedDetails"`
		// The event detail blocks, one per type, named as the liveChatMessages
		// resource names them. See youtube_activity.go.
		SuperChatDetails              *ytPaidDetails `json:"superChatDetails"`
		SuperStickerDetails           *ytPaidDetails `json:"superStickerDetails"`
		NewSponsorDetails             *ytNewSponsor  `json:"newSponsorDetails"`
		MemberMilestoneChatDetails    *ytMilestone   `json:"memberMilestoneChatDetails"`
		MembershipGiftingDetails      *ytGifting     `json:"membershipGiftingDetails"`
		GiftMembershipReceivedDetails *ytGiftRecv    `json:"giftMembershipReceivedDetails"`
		PollDetails                   *struct {
			Metadata struct {
				Status  string `json:"status"`
				Options []struct {
					Tally string `json:"tally"`
				} `json:"options"`
			} `json:"metadata"`
		} `json:"pollDetails"`
	} `json:"snippet"`
	AuthorDetails struct {
		ChannelID       string `json:"channelId"`
		DisplayName     string `json:"displayName"`
		IsVerified      bool   `json:"isVerified"`
		IsChatOwner     bool   `json:"isChatOwner"`
		IsChatSponsor   bool   `json:"isChatSponsor"`
		IsChatModerator bool   `json:"isChatModerator"`
	} `json:"authorDetails"`
}

// ytListResponse is the subset of liveChatMessages.list we read.
type ytListResponse struct {
	NextPageToken         string       `json:"nextPageToken"`
	PollingIntervalMillis int          `json:"pollingIntervalMillis"`
	OfflineAt             string       `json:"offlineAt"`
	Items                 []ytChatItem `json:"items"`
}

// pump polls one live chat until it ends or ctx does. A nil return means the
// chat finished normally.
func (y *YouTubeAdapter) pump(ctx context.Context, chatID string, sink Sink) error {
	// The no-replay baseline is the moment watching begins, before the first
	// list call, so an empty first page cannot move it past a later event.
	y.beginActivity()
	var (
		pageToken   string
		idle        = 1.0
		first       = true
		apiInterval time.Duration
		// failures counts consecutive transient failures, so the in-place
		// wait grows and eventually hands over to the Hub's own backoff.
		failures int
		// authRetried allows one reactive retry of a 401: the TokenFunc is
		// asked again, so a token that expired between refresh and call is
		// replaced. A second 401 is the account's, not the clock's.
		authRetried bool
	)

	for ctx.Err() == nil {
		endpoint := youtubeMessagesURL(y.cfg.APIBase, chatID, pageToken)

		var resp ytListResponse
		err := doJSON(ctx, y.cfg.HTTP, http.MethodGet, endpoint, y.cfg.Token, nil, &resp)
		// Charged whether or not it succeeded: Google counted the call.
		y.budget.spend(QuotaCostListMessages)

		if err != nil {
			cont, ret := y.listFailed(ctx, err, &failures, &authRetried)
			if cont {
				continue
			}
			return ret
		}

		failures, authRetried = 0, false
		y.mu.Lock()
		y.act.scope = false // the chat reads fine, whatever refused earlier
		y.mu.Unlock()
		cutoff := time.Time{}
		if first {
			cutoff = y.cfg.Now().Add(-ytDefaultBackfill)
			first = false
		}
		delivered := y.deliverPage(resp.Items, cutoff, sink)

		pageToken = resp.NextPageToken
		apiInterval = pollInterval(resp.PollingIntervalMillis, apiInterval)
		idle = nextIdle(idle, delivered)
		// offlineAt is YouTube telling us the broadcast has ended and the chat
		// is closing. Believing it saves the rest of the day's quota that
		// polling a dead chat would spend.
		if resp.OfflineAt != "" {
			y.setHealth(StateStopped, "the YouTube broadcast has ended")
			return nil
		}

		if y.pauseBeforeNext(ctx, apiInterval, idle) {
			return nil
		}
	}
	return nil
}

// deliverPage hands one page to the sink and reports how many were new enough
// to show. cutoff is zero for every page after the first.
func (y *YouTubeAdapter) deliverPage(items []ytChatItem, cutoff time.Time, sink Sink) int {
	delivered := 0
	for _, it := range items {
		// Activity first, and before messageFrom drops an item with no text: a
		// gifting event has none. The chat line is delivered below as before.
		y.activityFrom(it, sink)
		if applyYouTubeRemoval(it, sink) {
			continue
		}
		if it.Snippet.PollDetails != nil {
			y.notePoll(it)
		}
		if y.deliverMessage(it, cutoff, sink) {
			delivered++
		}
	}
	return delivered
}

// applyYouTubeRemoval acts on a deleted message or a ban. It reports true when
// the item is a tombstone, which is not delivered as a chat line.
func applyYouTubeRemoval(it ytChatItem, sink Sink) bool {
	// A tombstone marks where a deleted message used to be: its id and
	// publish time with nothing else. The reference says it is NOT sent at
	// the moment of deletion, only shown in place of the message, so this
	// catches a deletion when that stretch of chat is read again (the first
	// page after a reconnect), not necessarily when it happens. The
	// messageDeletedEvent and messageRetractedEvent types were removed from
	// the reference (2026-06-23) because the API never returned them, and
	// nothing here ever read them.
	// https://developers.google.com/youtube/v3/live/docs/liveChatMessages
	if it.Snippet.Type == "tombstone" {
		retract(sink, it.ID)
		return true
	}
	// A userBannedEvent IS sent when it happens, including a ban made by
	// another moderator or in Studio. YouTube clears a banned user's
	// backlog on its side, as Hub.Ban already assumes, so drop theirs here.
	if it.Snippet.Type == "userBannedEvent" {
		if d := it.Snippet.UserBannedDetails; d != nil && d.BannedUserDetails.ChannelID != "" {
			retractUser(sink, d.BannedUserDetails.ChannelID)
		}
		// Falls through: the event line is still delivered, so a ban placed
		// by another moderator or in Studio leaves a visible record.
	}
	return false
}

// deliverMessage delivers the item's chat line when it has one at or after the
// cutoff. It reports whether a line was delivered.
func (y *YouTubeAdapter) deliverMessage(it ytChatItem, cutoff time.Time, sink Sink) bool {
	m, ok := y.messageFrom(it)
	if !ok {
		return false
	}
	if !cutoff.IsZero() && m.At.Before(cutoff) {
		return false
	}
	sink.Deliver(m)
	return true
}

// pollInterval takes the interval YouTube asked for, when it asked for one.
func pollInterval(millis int, current time.Duration) time.Duration {
	if millis > 0 {
		return time.Duration(millis) * time.Millisecond
	}
	return current
}

// nextIdle backs off after an empty page and resets after one that delivered.
func nextIdle(idle float64, delivered int) float64 {
	if delivered == 0 {
		return idle * IdleBackoffStep
	}
	return 1
}

// pauseBeforeNext waits out the next read. It reports true when pump should
// stop: the quota wait was cancelled, or the sleep was.
func (y *YouTubeAdapter) pauseBeforeNext(ctx context.Context, apiInterval time.Duration, idle float64) bool {
	wait, ok := y.budget.intervalFor(apiInterval, idle)
	if !ok {
		return !y.waitForQuota(ctx)
	}
	y.setHealth(StateLive, "")
	return !y.cfg.Sleep(ctx, wait)
}

// youtubeMessagesURL is the liveChatMessages page to read next.
func youtubeMessagesURL(apiBase, chatID, pageToken string) string {
	endpoint := apiBase + "/liveChat/messages?part=snippet,authorDetails&maxResults=200" +
		"&liveChatId=" + url.QueryEscape(chatID)
	if pageToken != "" {
		endpoint += "&pageToken=" + url.QueryEscape(pageToken)
	}
	return endpoint
}

// listFailed decides what a failed page read means for pump. cont says to read
// again; otherwise pump returns ret.
func (y *YouTubeAdapter) listFailed(ctx context.Context, err error, failures *int, authRetried *bool) (cont bool, ret error) {
	// authRetried allows one reactive retry of a 401: the TokenFunc is
	// asked again, so a token that expired between refresh and call is
	// replaced. A second 401 is the account's, not the clock's.
	if statusOf(err) == http.StatusUnauthorized && !*authRetried {
		*authRetried = true
		return true, nil
	}
	*failures++
	done, retry, cerr := y.handleListError(ctx, err, *failures)
	if cerr != nil {
		return false, cerr
	}
	if done {
		return false, nil
	}
	if retry {
		return true, nil
	}
	return false, err
}

// handleListError decides what a failed poll means.
//
// done ends the chat cleanly, retry loops again without returning, and a
// non-nil error is fatal. Anything unrecognised falls through to the caller's
// plain error, which the Hub retries with backoff — the fail-open direction,
// since a status we have never seen is more likely transient than terminal.
func (y *YouTubeAdapter) handleListError(ctx context.Context, err error, failures int) (done, retry bool, fatal error) {
	if ferr := y.fatalFor(err); ferr != nil {
		if ytScopeReason(reasonOf(err)) {
			// Read path only: a refused moderation write says nothing about
			// whether the chat can be read.
			y.mu.Lock()
			y.act.scope = true
			y.mu.Unlock()
		}
		y.setHealth(StateFailed, ferr.Error())
		return false, false, ferr
	}
	switch reason := reasonOf(err); {
	case oauth.IsSpentQuotaReason(reason):
		// The platform has corrected our estimate. Stop until the reset
		// instead of spending the rest of the day collecting 403s.
		y.budget.pause()
		if !y.waitForQuota(ctx) {
			return true, false, nil
		}
		return false, true, nil
	case reason == "rateLimitExceeded", reason == "userRateLimitExceeded", reason == "backendError":
		// Transient. Wait as long as Google asked (Retry-After), or longer
		// each time it repeats, with jitter so accounts do not retry in
		// lockstep; after ytMaxInPlaceRetries the Hub's backoff takes over.
		if failures > ytMaxInPlaceRetries {
			return false, false, nil
		}
		if !y.cfg.Sleep(ctx, ytRateWait(failures, retryAfterFrom(err))) {
			return true, false, nil
		}
		return false, true, nil
	case reason == "forbidden":
		// Google documents this on liveChatMessages.list as "you don't have
		// the permissions required to retrieve messages". It is the account's
		// problem, not the chat closing: treating it as the end made Run
		// rediscover the same chat and be refused again, without pause.
		ferr := Fatal(fmt.Errorf("YouTube refused to let this account read the live chat; reconnect the YouTube account in Settings → Platforms, or make sure it is the broadcast's owner or a moderator"))
		y.setHealth(StateFailed, ferr.Error())
		return false, false, ferr
	case reason == "liveChatDisabled", reason == "liveChatEnded", reason == "liveChatNotFound":
		y.setHealth(StateStopped, "this YouTube broadcast has no live chat")
		return true, false, nil
	}

	switch statusOf(err) {
	case http.StatusGone:
		y.setHealth(StateStopped, "the YouTube live chat has closed")
		return true, false, nil
	}
	// A bare 404 is NOT "the chat closed": a closed chat answers 404 with the
	// liveChatNotFound reason (handled above), whereas a 404 without it means
	// the route or resource is wrong, and calling that a finished chat hid a
	// wrong path behind a chat that looked like it was cycling. It falls
	// through as a plain error the Hub retries and shows.
	return false, false, nil
}

const (
	// ytMaxInPlaceRetries is how many consecutive rate-limit or backend
	// failures pump waits out itself before returning to the Hub.
	ytMaxInPlaceRetries = 5
	// ytRateWaitCap bounds one in-place wait, however long Retry-After asks.
	ytRateWaitCap = 2 * time.Minute
)

// ytRateWait is how long to wait before retrying a rate-limited poll: the
// larger of what Google asked for and an escalating floor, capped, plus up to
// a quarter again of jitter.
func ytRateWait(failures int, retryAfter time.Duration) time.Duration {
	if failures < 1 {
		failures = 1
	}
	w := backoff.Exponential(failures-1, MinPollInterval*2, ytRateWaitCap)
	if retryAfter > w {
		w = retryAfter
	}
	if w > ytRateWaitCap {
		w = ytRateWaitCap
	}
	return w + time.Duration(rand.Int64N(int64(w)/4+1))
}

// fatalFor returns a Fatal error, with an instruction the operator can act
// on, for a failure no retry can fix, and nil otherwise.
func ytScopeReason(reason string) bool {
	return reason == "insufficientPermissions" || reason == "insufficientScope" || reason == "ACCESS_TOKEN_SCOPE_INSUFFICIENT"
}

func (y *YouTubeAdapter) fatalFor(err error) error {
	if IsFatal(err) {
		return err
	}
	switch reason := reasonOf(err); {
	case ytScopeReason(reason):
		return Fatal(fmt.Errorf("the connected YouTube account has not granted the permission this needs; reconnect the YouTube account in Settings → Platforms and approve every permission"))
	case reason == "liveStreamingNotEnabled":
		return Fatal(fmt.Errorf("live streaming is not enabled on this YouTube channel; enable it in YouTube Studio (it can take up to 24 hours), then restart chat"))
	}
	if statusOf(err) == http.StatusUnauthorized {
		return Fatal(fmt.Errorf("youtube rejected the access token; reconnect the YouTube account in Settings → Platforms"))
	}
	return nil
}

// waitForQuota parks until the allowance resets, checking often enough that a
// shutdown is not delayed by hours. It returns false when the context ended.
func (y *YouTubeAdapter) waitForQuota(ctx context.Context) bool {
	for {
		q := y.budget.status()
		left := q.ResetAt.Sub(y.cfg.Now())
		if left <= 0 || (!q.Paused && q.Remaining > QuotaCostListMessages) {
			return ctx.Err() == nil
		}
		y.setHealth(StateDegraded, fmt.Sprintf(
			"YouTube chat is paused: this project's daily API quota is spent. It resets at %s (in %s). "+
				"Other platforms are unaffected, and you can raise the quota in the Google Cloud console.",
			q.ResetAt.Format(ytClockLayout), left.Round(time.Minute)))
		step := ytPauseCheck
		if left < step {
			step = left
		}
		if !y.cfg.Sleep(ctx, step) {
			return false
		}
	}
}

// messageFrom normalises one live chat message.
func (y *YouTubeAdapter) messageFrom(it ytChatItem) (Message, bool) {
	text := it.Snippet.DisplayMessage
	if strings.TrimSpace(text) == "" {
		return Message{}, false
	}
	kind := it.Snippet.Type
	a := it.AuthorDetails
	owner, moderator, sponsor, verified := a.IsChatOwner, a.IsChatModerator, a.IsChatSponsor, a.IsVerified

	at, err := time.Parse(time.RFC3339, it.Snippet.PublishedAt)
	if err != nil {
		at = y.cfg.Now()
	}

	var badges []Badge
	if owner {
		badges = append(badges, Badge{ID: "broadcaster", Label: "Owner"})
	}
	if moderator {
		badges = append(badges, Badge{ID: "moderator", Label: "Moderator"})
	}
	if sponsor {
		badges = append(badges, Badge{ID: "member", Label: "Member"})
	}
	if verified {
		badges = append(badges, Badge{ID: "verified", Label: "Verified"})
	}
	// Super Chats and member milestones arrive through the same list with a
	// different type. They carry a displayMessage, so they are shown rather
	// than dropped — a paid message is the one an operator most wants to see —
	// and the type is kept as a badge so the UI can mark it.
	if kind != "" && kind != "textMessageEvent" {
		badges = append(badges, Badge{ID: kind, Label: ytEventLabel(kind)})
	}

	return Message{
		ID:       it.ID,
		Platform: db.PlatformYouTube,
		Account:  y.cfg.AccountRef,
		Channel:  y.cfg.Channel,
		Text:     text,
		At:       at,
		Author: Author{
			ID:          a.ChannelID,
			Name:        a.DisplayName,
			Badges:      badges,
			Moderator:   moderator,
			Subscriber:  sponsor,
			Broadcaster: owner,
		},
	}, true
}

func ytEventLabel(kind string) string {
	switch kind {
	case "superChatEvent":
		return "Super Chat"
	case "superStickerEvent":
		return "Super Sticker"
	case "newSponsorEvent":
		return "New member"
	case "memberMilestoneChatEvent":
		return "Member milestone"
	default:
		return kind
	}
}

// Send posts to the live chat.
//
// It costs 20 units (liveChatMessages.insert) against the same daily allowance, which is why the
// budget holds a reserve back from polling: an operator who cannot reply
// because the chat pane spent everything reading would rightly consider the
// feature broken.
func (y *YouTubeAdapter) Send(ctx context.Context, text string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, fmt.Errorf("nothing to send")
	}
	if n := len([]rune(text)); n > YouTubeMaxMessage {
		return Message{}, fmt.Errorf("YouTube accepts %d characters and this message is %d", YouTubeMaxMessage, n)
	}

	y.mu.Lock()
	chatID := y.chatID
	y.mu.Unlock()
	if chatID == "" {
		return Message{}, fmt.Errorf("YouTube has no live chat open right now")
	}
	if !y.budget.allow(QuotaCostSendMessage) {
		q := y.budget.status()
		return Message{}, fmt.Errorf("YouTube's daily API quota is spent; it resets at %s",
			q.ResetAt.Format(ytClockLayout))
	}

	payload := map[string]any{
		"snippet": map[string]any{
			"liveChatId":         chatID,
			"type":               "textMessageEvent",
			"textMessageDetails": map[string]any{"messageText": text},
		},
	}
	var out struct {
		ID      string `json:"id"`
		Snippet struct {
			PublishedAt string `json:"publishedAt"`
		} `json:"snippet"`
	}
	err := doJSON(ctx, y.cfg.HTTP, http.MethodPost, y.cfg.APIBase+"/liveChat/messages?part=snippet",
		y.cfg.Token, payload, &out)
	y.budget.spend(QuotaCostSendMessage)
	if err != nil {
		return Message{}, y.classify(err)
	}

	at, perr := time.Parse(time.RFC3339, out.Snippet.PublishedAt)
	if perr != nil {
		at = y.cfg.Now()
	}
	// Returned with YouTube's own id so that when the poll delivers our
	// message back — and it will — the dedupe key suppresses the second copy.
	return Message{
		ID:       out.ID,
		Platform: db.PlatformYouTube,
		Account:  y.cfg.AccountRef,
		Channel:  y.cfg.Channel,
		Author:   Author{ID: y.cfg.AccountRef, Name: firstNonEmpty(y.cfg.Channel, "you"), Broadcaster: true},
		Text:     text,
		At:       at,
	}, nil
}

// Delete removes one message from the live chat.
//
// This needs NO new OAuth scope. liveChatMessages.delete accepts
// https://www.googleapis.com/auth/youtube, which internal/oauth/youtube.go has
// always requested — so every YouTube account already connected can do this
// today, with no reconnect and no consent screen. The capability matrix recorded
// YouTube moderation as "unverified" for a long time, which is exactly the
// fail-open default working as intended: nobody had read the docs, so nothing
// claimed it was impossible.
//
// Unlike Send, this does NOT refuse when the budget looks spent. A message a
// moderator has decided to remove stays on stream while we decline to spend 20
// units, and the API's own 403 remains the authority on when the quota is
// actually gone. Spending is still recorded, so the pacer keeps reading honestly.
func (y *YouTubeAdapter) Delete(ctx context.Context, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return fmt.Errorf("no message id to delete")
	}
	err := doJSON(ctx, y.cfg.HTTP, http.MethodDelete,
		y.cfg.APIBase+"/liveChat/messages?id="+url.QueryEscape(messageID),
		y.cfg.Token, nil, nil)
	y.budget.spend(QuotaCostDeleteMessage)
	if err != nil {
		// 403 here is usually authority rather than quota: the connected
		// account is not the broadcaster and not a moderator of that chat.
		// Saying so beats "forbidden", which sends people to reconnect an
		// account that was never the problem.
		if statusOf(err) == http.StatusForbidden && !oauth.IsSpentQuotaReason(reasonOf(err)) {
			return fmt.Errorf("YouTube refused the deletion. The connected account has to own the "+
				"broadcast or be a moderator of its chat: %w", err)
		}
		if statusOf(err) == http.StatusNotFound {
			return fmt.Errorf("YouTube does not have that message any more; it may already be deleted: %w", err)
		}
		return y.classify(err)
	}
	return nil
}

// Ban removes a user from the live chat, permanently or for a duration.
//
// Like Delete, this needs no new scope: liveChatBans.insert accepts
// auth/youtube, which is already granted. Verified against Google's reference
// rather than assumed, because "it is probably the same scope" is how a feature
// ships and then 403s on every existing account.
//
// YouTube counts in SECONDS. Kick counts in minutes. That is why this takes a
// Duration and converts here.
func (y *YouTubeAdapter) Ban(ctx context.Context, userID string, d time.Duration, _ string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("no user id to ban")
	}
	y.mu.Lock()
	chatID := y.chatID
	y.mu.Unlock()
	if chatID == "" {
		return fmt.Errorf("YouTube has no live chat open right now")
	}

	snippet := map[string]any{
		"liveChatId":        chatID,
		"type":              "permanent",
		"bannedUserDetails": map[string]any{"channelId": userID},
	}
	if d > 0 {
		// Rounded UP, not truncated: a 500ms timeout rounding to zero seconds
		// would be sent as a permanent ban, and the difference between those two
		// is not something to leave to integer division.
		secs := int64((d + time.Second - 1) / time.Second)
		snippet["type"] = "temporary"
		snippet["banDurationSeconds"] = secs
	}

	err := doJSON(ctx, y.cfg.HTTP, http.MethodPost,
		y.cfg.APIBase+"/liveChat/bans?part=snippet", y.cfg.Token,
		map[string]any{"snippet": snippet}, nil)
	y.budget.spend(QuotaCostBan)
	if err != nil {
		if statusOf(err) == http.StatusForbidden && !oauth.IsSpentQuotaReason(reasonOf(err)) {
			return fmt.Errorf("YouTube refused the ban. The connected account has to own the broadcast "+
				"or be a moderator of its chat: %w", err)
		}
		return y.classify(err)
	}
	return nil
}

// Unban lifts a ban. YouTube addresses this by the BAN's id rather than the
// user's, which polyemesis does not keep, so this reports the limitation instead
// of failing obscurely.
func (y *YouTubeAdapter) Unban(ctx context.Context, userID string) error {
	return fmt.Errorf("YouTube removes a ban by the ban's own id, which polyemesis does not store. " +
		"Lift it from YouTube Studio → Live chat, where the ban is listed")
}

// classify turns an API error into the message an operator can act on, without
// echoing anything that could carry a credential.
func (y *YouTubeAdapter) classify(err error) error {
	if oauth.IsSpentQuotaReason(reasonOf(err)) {
		y.budget.pause()
		q := y.budget.status()
		return fmt.Errorf("YouTube's daily API quota is spent; chat resumes at %s", q.ResetAt.Format(ytClockLayout))
	}
	if ferr := y.fatalFor(err); ferr != nil {
		return ferr
	}
	return err
}

// SetQuota replaces the daily allowance this adapter paces against, without
// dropping the connection.
//
// #732 wired the operator's number into NewYouTube, which answered the question
// only for an adapter not yet built: chatAdapter reads the allowance when it
// builds one, at startup or when an account is connected, and never again for
// that adapter. An operator granted a larger quota by a YouTube API
// Services audit would set it, be told it was saved, and go on polling at the
// default rate until the next restart -- which is the same defect #732 was
// filed for, moved one step later.
//
// The reload table would not let that ship. Every leaf in db.Settings has to
// name what happens when it changes mid-stream, and the honest answer without
// this method was ClassNextStart, a class whose own documentation calls it an
// admission rather than a design.
//
// The parameters are plain ints because this method IS the quotaPacer
// interface, whose whole point is that a platform package can declare the
// capability without importing anything of ours -- see hub.go. The two named
// types start one line down, at the call into setLimits, which is the last
// place the pair can be got the wrong way round; that line is covered by
// TestSetQuotaReachesTheRealYouTubeAdapterAndItsReportedQuota, which pushes a
// distinguishable pair through a real adapter and reads back what it reports.
func (y *YouTubeAdapter) SetQuota(units, reserve int) {
	if y == nil || y.budget == nil {
		return
	}
	y.budget.setLimits(QuotaUnits(units), QuotaReserve(reserve))
}

// adapterBudget is the client's shared budget when one was handed in, and a
// private one otherwise.
func adapterBudget(cfg YouTubeConfig) *budget {
	if cfg.Quota != nil {
		return cfg.Quota.b
	}
	return newBudget(cfg.QuotaUnits, cfg.QuotaReserve, cfg.Now)
}

// ytTally is a poll's counts as last seen in a list page.
type ytTally struct {
	votes []int
	live  bool
}

// ytMaxTallies bounds the poll cache. A stream runs a handful of polls.
const ytMaxTallies = 32

// notePoll remembers the tally a list page carried for a poll message. The
// reference says the tally is present only when the request is authorised by
// the channel owner; a poll seen without one is not recorded. An all-zero tally
// is not recorded either: the poll message is delivered when it is created,
// before anyone has voted, and no documented call re-reads it, so a row of
// zeros is as likely a snapshot from the start as a count. PollTally therefore
// reports "nothing yet" until a page carries a count that is not zero.
func (y *YouTubeAdapter) notePoll(it ytChatItem) {
	md := it.Snippet.PollDetails.Metadata
	if it.ID == "" || len(md.Options) == 0 {
		return
	}
	votes := make([]int, len(md.Options))
	total := 0
	for i, o := range md.Options {
		n, err := strconv.Atoi(o.Tally)
		if err != nil || n < 0 {
			return
		}
		votes[i] = n
		total += n
	}
	if total == 0 {
		return
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.tallies == nil || len(y.tallies) >= ytMaxTallies {
		y.tallies = map[string]ytTally{}
	}
	y.tallies[it.ID] = ytTally{votes: votes, live: md.Status != "closed"}
}

// PollTally returns the latest count seen for a running poll, read from the
// chat the adapter is already polling -- no extra API call. ok is false until a
// list page has carried a tally for it. live is false once the page said the
// poll is closed.
func (y *YouTubeAdapter) PollTally(pollID string) (votes []int, live, ok bool) {
	y.mu.Lock()
	defer y.mu.Unlock()
	t, found := y.tallies[pollID]
	if !found {
		return nil, false, false
	}
	return append([]int(nil), t.votes...), t.live, true
}
