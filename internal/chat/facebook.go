package chat

// Facebook Live comments over the Graph API.
//
// Facebook's live "chat" is the comment thread on the live video object, which
// means this adapter needs the live video id — and polyemesis already has it,
// because internal/oauth creates the broadcast and keeps Broadcast.ID (with
// oauth.FacebookLiveVideoID as the best-effort recovery from a stored stream
// key). Without one there is nothing to read, and that is a state this reports
// plainly rather than an error it raises: a Facebook destination whose key was
// pasted by hand has no id to find, and the operator should be told that in
// one sentence instead of watching an adapter fail in a loop.
//
// There is no long-poll or push option here, so this polls. Facebook publishes
// no per-endpoint quota to pace against the way YouTube does, so the interval
// is a constant with a backoff on error rather than a budget.

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
	// fbPollInterval is the comment poll spacing. Comments on a live video
	// arrive slower than Twitch chat and Facebook rate-limits per app rather
	// than per endpoint, so five seconds is responsive without being greedy.
	fbPollInterval = 5 * time.Second
	// fbMaxPollInterval is where the error backoff stops.
	fbMaxPollInterval = 2 * time.Minute
	// fbDefaultBackfill bounds what the first poll shows, for the same reason
	// YouTube's does: the thread already has history and replaying it as if it
	// were live is disorienting.
	fbDefaultBackfill = 5 * time.Minute
	// fbPageSize is how many comments one poll asks for.
	fbPageSize = 50
)

// FacebookConfig configures the comment poller.
type FacebookConfig struct {
	AccountRef string
	// Channel is the profile or Page name, for the tab header.
	Channel string
	Token   TokenFunc
	// LiveVideoID is the Graph id of the live video whose comments to read.
	// Empty is a supported, explained state — see Run.
	LiveVideoID string
	// Backfill bounds the first poll.
	Backfill time.Duration
	// Interval overrides the poll spacing.
	Interval time.Duration

	APIBase string
	HTTP    *http.Client
	Now     func() time.Time
	Sleep   func(context.Context, time.Duration) bool
}

// FacebookAdapter reads the comments on one live video.
type FacebookAdapter struct {
	cfg FacebookConfig

	mu     sync.Mutex
	health Health
	// lastSeen is the newest comment timestamp delivered so far. Facebook
	// returns the thread newest-first with no cursor that survives a restart,
	// so this timestamp is what stops the same comments being redelivered every
	// poll. The Hub's dedupe catches anything that slips through the equal-time
	// boundary.
	lastSeen time.Time
}

// NewFacebook builds the adapter.
func NewFacebook(cfg FacebookConfig) (*FacebookAdapter, error) {
	if cfg.Token == nil {
		return nil, fmt.Errorf("facebook chat is not configured: no access token, so connect the Facebook account in Settings → Platforms")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://graph.facebook.com/v24.0"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.Backfill <= 0 {
		cfg.Backfill = fbDefaultBackfill
	}
	if cfg.Interval <= 0 {
		cfg.Interval = fbPollInterval
	}
	return &FacebookAdapter{cfg: cfg, health: Health{State: StateConnecting}}, nil
}

func (f *FacebookAdapter) Platform() db.Platform { return db.PlatformFacebook }
func (f *FacebookAdapter) Account() string       { return f.cfg.AccountRef }

// Delete removes one comment from the live video's thread.
//
// Facebook's live chat IS the comment thread on the video, so moderating it is
// comment moderation: DELETE /{comment_id}. There is no chat-specific endpoint
// and no separate moderation surface to find.
//
// Facebook is the only one of the four platforms with a reversible option --
// `is_hidden` takes a comment off the public thread without destroying it -- and
// Hide below uses it. Delete is the irreversible one, kept separate rather than
// hidden behind a flag, because a moderator choosing between "hide" and "destroy"
// should have to say which.
func (f *FacebookAdapter) Delete(ctx context.Context, commentID string) error {
	commentID = strings.TrimSpace(commentID)
	if commentID == "" {
		return fmt.Errorf("no comment id to delete")
	}
	err := doJSON(ctx, f.cfg.HTTP, http.MethodDelete,
		f.cfg.APIBase+"/"+url.PathEscape(commentID), f.cfg.Token, nil, nil)
	if err != nil {
		return f.moderationError(err, "delete")
	}
	return nil
}

// Hide takes a comment off the public thread, or puts it back.
//
// The only reversible moderation primitive across all four platforms. Worth
// having explicitly: a comment hidden in error costs an apology, and a comment
// deleted in error costs the thing itself. Facebook keeps it visible to its
// author and their friends, which is the platform's decision and not something
// polyemesis can change -- so this is "off the public thread", not "gone".
func (f *FacebookAdapter) Hide(ctx context.Context, commentID string, hidden bool) error {
	commentID = strings.TrimSpace(commentID)
	if commentID == "" {
		return fmt.Errorf("no comment id to hide")
	}
	err := doJSON(ctx, f.cfg.HTTP, http.MethodPost,
		f.cfg.APIBase+"/"+url.PathEscape(commentID),
		f.cfg.Token, map[string]any{"is_hidden": hidden}, nil)
	if err != nil {
		verb := "hide"
		if !hidden {
			verb = "unhide"
		}
		return f.moderationError(err, verb)
	}
	return nil
}

// moderationError turns a Graph API refusal into the sentence that fixes it.
//
// The permission story here is the one that costs people a day. Reading comment
// ids on a Page post needs the MODERATE task permission under Page Public
// Content Access, so an app that can happily READ the thread can still be unable
// to act on it — and the error for that reads like a generic permissions
// failure.
func (f *FacebookAdapter) moderationError(err error, verb string) error {
	switch statusOf(err) {
	case http.StatusForbidden:
		return fmt.Errorf("Facebook refused to %s that comment. Acting on a Page's comments needs the "+
			"MODERATE task permission on that Page, which is separate from being able to read them: %w", verb, err)
	case http.StatusNotFound:
		return fmt.Errorf("Facebook no longer has that comment; it may already be gone: %w", err)
	}
	if statusOf(err) == http.StatusUnauthorized {
		return Fatal(fmt.Errorf("facebook rejected the access token; reconnect the Facebook account in Settings → Platforms"))
	}
	return err
}

func (f *FacebookAdapter) Health() Health {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health
}

func (f *FacebookAdapter) setHealth(state State, detail string) {
	f.mu.Lock()
	f.health = Health{State: state, Detail: detail}
	f.mu.Unlock()
}

// SetLiveVideoID points the adapter at a broadcast. The go-live path calls it
// when it creates one, so an adapter attached before the broadcast existed
// starts working without being restarted.
func (f *FacebookAdapter) SetLiveVideoID(id string) {
	f.mu.Lock()
	f.cfg.LiveVideoID = strings.TrimSpace(id)
	f.mu.Unlock()
}

func (f *FacebookAdapter) liveVideoID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg.LiveVideoID
}

// Run polls the comment thread until ctx ends.
func (f *FacebookAdapter) Run(ctx context.Context, sink Sink) error {
	f.mu.Lock()
	f.lastSeen = f.cfg.Now().Add(-f.cfg.Backfill)
	f.mu.Unlock()

	interval := f.cfg.Interval
	for ctx.Err() == nil {
		id := f.liveVideoID()
		if id == "" {
			// Not a failure. A Facebook destination whose key was pasted by
			// hand has no live video object polyemesis knows about, and saying
			// so once is worth more than an adapter that keeps restarting.
			f.setHealth(StateDegraded,
				"Facebook comments need the live video polyemesis created. This destination has no live video id, "+
					"which happens when the stream key was pasted by hand rather than fetched by connecting the account.")
			if !f.cfg.Sleep(ctx, fbMaxPollInterval) {
				return nil
			}
			continue
		}

		_, err := f.poll(ctx, id, sink)
		if err != nil {
			done, fatal := f.classify(err)
			if fatal != nil {
				return fatal
			}
			if done {
				return nil
			}
			// Back off on error, but never past the point where a recovered
			// API takes two minutes to be noticed.
			if interval < fbMaxPollInterval {
				interval *= 2
			}
			f.setHealth(StateDegraded, "Facebook comments are not being read right now: "+err.Error())
		} else {
			interval = f.cfg.Interval
			f.setHealth(StateLive, "")
		}

		if !f.cfg.Sleep(ctx, interval) {
			return nil
		}
	}
	return nil
}

// fbComment is one comment as Graph returns it.
type fbComment struct {
	ID   string `json:"id"`
	From struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"from"`
	Message     string `json:"message"`
	CreatedTime string `json:"created_time"`
}

// poll fetches the newest page and delivers what has not been seen.
func (f *FacebookAdapter) poll(ctx context.Context, videoID string, sink Sink) (int, error) {
	// reverse_chronological gives the newest comments first, which is what a
	// live thread needs: paging forward through an hours-old conversation to
	// reach the present would spend the whole broadcast catching up.
	//
	// live_filter is left at no_filter deliberately. Facebook's "low quality"
	// filter hides comments, and a chat pane that silently omits someone is
	// worse than one that shows a message the operator would have skipped.
	endpoint := fmt.Sprintf("%s/%s/comments?fields=id,from,message,created_time&order=reverse_chronological&live_filter=no_filter&limit=%d",
		f.cfg.APIBase, url.PathEscape(videoID), fbPageSize)

	var out struct {
		Data []fbComment `json:"data"`
	}
	if err := doJSON(ctx, f.cfg.HTTP, http.MethodGet, endpoint, f.cfg.Token, nil, &out); err != nil {
		return 0, err
	}

	f.mu.Lock()
	seenBefore := f.lastSeen
	f.mu.Unlock()

	newest := seenBefore
	// Walked backwards so the oldest of the new comments is delivered first
	// and the pane reads in conversation order.
	delivered := 0
	for i := len(out.Data) - 1; i >= 0; i-- {
		c := out.Data[i]
		m, ok := f.messageFrom(c)
		if !ok || !m.At.After(seenBefore) {
			continue
		}
		if m.At.After(newest) {
			newest = m.At
		}
		delivered++
		sink.Deliver(m)
	}

	f.mu.Lock()
	if newest.After(f.lastSeen) {
		f.lastSeen = newest
	}
	f.mu.Unlock()
	return delivered, nil
}

func (f *FacebookAdapter) messageFrom(c fbComment) (Message, bool) {
	if strings.TrimSpace(c.Message) == "" {
		return Message{}, false
	}
	at, err := time.Parse("2006-01-02T15:04:05-0700", c.CreatedTime)
	if err != nil {
		// Graph has used both offset spellings over the years; RFC 3339 is the
		// other one, and a comment with an unreadable timestamp is still a
		// comment.
		if at, err = time.Parse(time.RFC3339, c.CreatedTime); err != nil {
			at = f.cfg.Now()
		}
	}
	return Message{
		ID:       c.ID,
		Platform: db.PlatformFacebook,
		Account:  f.cfg.AccountRef,
		Channel:  f.cfg.Channel,
		Text:     c.Message,
		At:       at,
		Author: Author{
			ID:   c.From.ID,
			Name: c.From.Name,
		},
	}, true
}

// classify decides what a Graph failure means. Anything unrecognised is
// transient by default: Graph's error taxonomy is large, and treating an
// unfamiliar code as terminal would end a working chat over something that
// would have cleared on the next poll.
func (f *FacebookAdapter) classify(err error) (done bool, fatal error) {
	switch statusOf(err) {
	case http.StatusUnauthorized:
		f.setHealth(StateFailed, "Facebook rejected the access token; reconnect the Facebook account in Settings → Platforms")
		return false, Fatal(fmt.Errorf("facebook rejected the access token; reconnect the Facebook account in Settings → Platforms"))
	case http.StatusNotFound:
		// The live video is gone: the broadcast ended and Facebook has
		// finalised or removed the object.
		f.setHealth(StateStopped, "this Facebook live video has ended")
		return true, nil
	case http.StatusForbidden:
		f.setHealth(StateDegraded,
			"Facebook refused to return comments for this live video. Reading comments needs the same "+
				"permissions the broadcast was created with; if this is a Page, the connection needs "+
				"pages_read_engagement.")
		return false, nil
	}
	return false, nil
}
