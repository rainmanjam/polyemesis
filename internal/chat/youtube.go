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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

const (
	// YouTubeMaxMessage is the published limit on a live chat message.
	YouTubeMaxMessage = 200
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
	// Backfill bounds the first poll's history.
	Backfill time.Duration

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
}

// NewYouTube builds the adapter. As everywhere else here, a missing token is a
// "not configured" state with a named cause rather than a crash.
func NewYouTube(cfg YouTubeConfig) (*YouTubeAdapter, error) {
	if cfg.Token == nil {
		return nil, fmt.Errorf("youtube chat is not configured: no access token, so connect the YouTube account in Settings → Platforms")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://www.googleapis.com/youtube/v3"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.Backfill <= 0 {
		cfg.Backfill = ytDefaultBackfill
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
		budget: newBudget(cfg.QuotaUnits, cfg.QuotaReserve, cfg.Now),
		chatID: cfg.LiveChatID,
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
	for ctx.Err() == nil {
		chatID, err := y.liveChatID(ctx)
		if err != nil {
			return err
		}
		if chatID == "" {
			y.setHealth(StateDegraded, "waiting for a YouTube broadcast to go live; chat starts when it does")
			if !y.cfg.Sleep(ctx, idleFor) {
				return nil
			}
			if idleFor < ytBroadcastPollMax {
				idleFor *= 2
			}
			continue
		}
		idleFor = ytBroadcastPoll

		if err := y.pump(ctx, chatID, sink); err != nil {
			return err
		}
		// The chat ended. Forget the id so the next round rediscovers, in case
		// the operator starts a second broadcast without restarting anything.
		y.mu.Lock()
		y.chatID = ""
		y.mu.Unlock()
		if ctx.Err() != nil {
			return nil
		}
	}
	return nil
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
	var (
		pageToken   string
		idle        = 1.0
		first       = true
		apiInterval time.Duration
	)

	for ctx.Err() == nil {
		endpoint := y.cfg.APIBase + "/liveChatMessages?part=snippet,authorDetails&maxResults=200" +
			"&liveChatId=" + url.QueryEscape(chatID)
		if pageToken != "" {
			endpoint += "&pageToken=" + url.QueryEscape(pageToken)
		}

		var resp ytListResponse
		err := doJSON(ctx, y.cfg.HTTP, http.MethodGet, endpoint, y.cfg.Token, nil, &resp)
		// Charged whether or not it succeeded: Google counted the call.
		y.budget.spend(QuotaCostListMessages)

		if err != nil {
			done, retry, cerr := y.handleListError(ctx, err)
			if cerr != nil {
				return cerr
			}
			if done {
				return nil
			}
			if retry {
				continue
			}
			return err
		}

		cutoff := time.Time{}
		if first {
			cutoff = y.cfg.Now().Add(-y.cfg.Backfill)
			first = false
		}
		delivered := y.deliverPage(resp.Items, cutoff, sink)

		pageToken = resp.NextPageToken
		if resp.PollingIntervalMillis > 0 {
			apiInterval = time.Duration(resp.PollingIntervalMillis) * time.Millisecond
		}
		if delivered == 0 {
			idle *= IdleBackoffStep
		} else {
			idle = 1
		}
		// offlineAt is YouTube telling us the broadcast has ended and the chat
		// is closing. Believing it saves the rest of the day's quota that
		// polling a dead chat would spend.
		if resp.OfflineAt != "" {
			y.setHealth(StateStopped, "the YouTube broadcast has ended")
			return nil
		}

		wait, ok := y.budget.intervalFor(apiInterval, idle)
		if !ok {
			if !y.waitForQuota(ctx) {
				return nil
			}
			continue
		}
		y.setHealth(StateLive, "")
		if !y.cfg.Sleep(ctx, wait) {
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
		m, ok := y.messageFrom(it)
		if !ok {
			continue
		}
		if !cutoff.IsZero() && m.At.Before(cutoff) {
			continue
		}
		delivered++
		sink.Deliver(m)
	}
	return delivered
}

// handleListError decides what a failed poll means.
//
// done ends the chat cleanly, retry loops again without returning, and a
// non-nil error is fatal. Anything unrecognised falls through to the caller's
// plain error, which the Hub retries with backoff — the fail-open direction,
// since a status we have never seen is more likely transient than terminal.
func (y *YouTubeAdapter) handleListError(ctx context.Context, err error) (done, retry bool, fatal error) {
	switch reasonOf(err) {
	case "quotaExceeded", "dailyLimitExceeded", "dailyLimitExceededUnreg":
		// The platform has corrected our estimate. Stop until the reset
		// instead of spending the rest of the day collecting 403s.
		y.budget.pause()
		if !y.waitForQuota(ctx) {
			return true, false, nil
		}
		return false, true, nil
	case "rateLimitExceeded", "userRateLimitExceeded", "backendError":
		// Transient. Wait out a poll interval and carry on.
		if !y.cfg.Sleep(ctx, MinPollInterval*2) {
			return true, false, nil
		}
		return false, true, nil
	case "liveChatDisabled", "liveChatEnded", "liveChatNotFound", "forbidden":
		y.setHealth(StateStopped, "this YouTube broadcast has no live chat")
		return true, false, nil
	}

	switch statusOf(err) {
	case http.StatusUnauthorized:
		y.setHealth(StateFailed, "YouTube rejected the access token; reconnect the YouTube account in Settings → Platforms")
		return false, false, Fatal(fmt.Errorf("youtube rejected the access token; reconnect the YouTube account in Settings → Platforms"))
	case http.StatusNotFound:
		y.setHealth(StateStopped, "the YouTube live chat has closed")
		return true, false, nil
	}
	return false, false, nil
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
// It costs fifty units against the same daily allowance, which is why the
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
	err := doJSON(ctx, y.cfg.HTTP, http.MethodPost, y.cfg.APIBase+"/liveChatMessages?part=snippet",
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
// moderator has decided to remove stays on stream while we decline to spend 50
// units, and the API's own 403 remains the authority on when the quota is
// actually gone. Spending is still recorded, so the pacer keeps reading honestly.
func (y *YouTubeAdapter) Delete(ctx context.Context, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return fmt.Errorf("no message id to delete")
	}
	err := doJSON(ctx, y.cfg.HTTP, http.MethodDelete,
		y.cfg.APIBase+"/liveChatMessages?id="+url.QueryEscape(messageID),
		y.cfg.Token, nil, nil)
	y.budget.spend(QuotaCostDeleteMessage)
	if err != nil {
		// 403 here is usually authority rather than quota: the connected
		// account is not the broadcaster and not a moderator of that chat.
		// Saying so beats "forbidden", which sends people to reconnect an
		// account that was never the problem.
		if statusOf(err) == http.StatusForbidden && reasonOf(err) != "quotaExceeded" {
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
		y.cfg.APIBase+"/liveChatBans?part=snippet", y.cfg.Token,
		map[string]any{"snippet": snippet}, nil)
	y.budget.spend(QuotaCostBan)
	if err != nil {
		if statusOf(err) == http.StatusForbidden && reasonOf(err) != "quotaExceeded" {
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
	switch reasonOf(err) {
	case "quotaExceeded", "dailyLimitExceeded":
		y.budget.pause()
		q := y.budget.status()
		return fmt.Errorf("YouTube's daily API quota is spent; chat resumes at %s", q.ResetAt.Format(ytClockLayout))
	}
	if statusOf(err) == http.StatusUnauthorized {
		return Fatal(fmt.Errorf("youtube rejected the access token; reconnect the YouTube account in Settings → Platforms"))
	}
	return err
}

// SetQuota replaces the daily allowance this adapter paces against, without
// dropping the connection.
//
// #732 wired the operator's number into NewYouTube, which answered the question
// only for a process that had not started yet: chatAdapter runs once, from
// StartChat, from main. An operator granted a larger quota by a YouTube API
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
