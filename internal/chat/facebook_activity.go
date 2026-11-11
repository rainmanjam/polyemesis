package chat

// Facebook activity: the reaction tally.
//
// WHAT FACEBOOK OFFERS. The live video's `reactions` edge
// (GET /{live-video-id}/reactions?type=...&summary=total_count) returns a
// running total, per reaction type, and nothing else useful: no per-reaction
// event, no id, no actor on the aggregate. So this reports a DELTA over a
// window, as one `reaction` activity with `count` and `breakdown`, no actor.
//
// WHAT IT DOES NOT OFFER. Facebook Stars (the live-video tip) are not on any
// Graph live-video edge or field in the current reference, so there is no tip
// kind here and ActivityKinds says why. The live-status webhook is deferred
// (D7).
//
// SEMANTICS.
//   - Off by default (ActivitySettings.Reactions). Off means no request is made.
//   - No replay. The first sample after Run starts (a restart, a reconnect, a
//     new live video id) only seeds the baseline and emits nothing. A total that
//     goes DOWN (people un-react) moves the baseline down and emits nothing.
//   - The dedupe id is synthetic -- the video and the window start -- because
//     Facebook sends none. A window is never emitted twice.
//   - A refusal for permission marks the kind unavailable with the reason and
//     backs off for a long time. It is never a comments failure.
//   - Cadence: every third comment poll AND at least fbReactionEvery apart.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

const (
	// fbReactionPolls is how many comment polls pass between reaction samples.
	fbReactionPolls = 3
	// fbReactionEvery is the least time between two reaction samples.
	fbReactionEvery = 30 * time.Second
	// fbReactionRetry is how long a permission refusal is left alone.
	fbReactionRetry = 10 * time.Minute
	// fbSourceReactions is Source.Type of the tally.
	fbSourceReactions = "live_video.reactions"
)

// fbReactionTypes are the reaction types read for the breakdown, as Graph names
// them on the edge's `type` parameter. Wire keys are the lowercase names.
var fbReactionTypes = []string{"LIKE", "LOVE", "CARE", "HAHA", "WOW", "SAD", "ANGRY"}

const fbReactionsNeedAccessDetail = "Facebook refused to return reactions for this live video. Reactions use " +
	"pages_read_engagement and the Live Video API, which Meta App Review must approve for the app."

// fbReactions is the reaction tally's state. Guarded by FacebookAdapter.mu.
type fbReactions struct {
	video       string
	seeded      bool
	total       int64
	byType      map[string]int64
	windowStart time.Time
	polls       int
	lastSample  time.Time
	retryAfter  time.Time
	unavailable string // reason, "" when available
}

func (f *FacebookAdapter) reactionsOn() bool {
	return f.cfg.ReactionsEnabled != nil && f.cfg.ReactionsEnabled()
}

// resetReactions forgets the baseline. Called as Run starts, so a restart or a
// reconnect re-seeds silently instead of reporting everything since last time.
func (f *FacebookAdapter) resetReactions() {
	f.mu.Lock()
	f.react = fbReactions{}
	f.mu.Unlock()
}

// ActivityKinds reports the activity kinds Facebook can give this account. It
// satisfies the API package's activityKindReporter.
func (f *FacebookAdapter) ActivityKinds() []activity.KindStatus {
	f.mu.Lock()
	reason := f.react.unavailable
	f.mu.Unlock()
	rx := activity.KindStatus{Kind: activity.KindReaction, State: activity.KindOn}
	switch {
	case !f.reactionsOn():
		rx.State, rx.Detail = activity.KindOff, "Reactions are switched off in Activity settings."
	case reason != "":
		rx.State, rx.Detail = activity.KindUnavailable, reason
	}
	return []activity.KindStatus{
		rx,
		{Kind: activity.KindTip, State: activity.KindUnavailable,
			Detail: "Facebook does not expose Stars on live videos through the Graph API."},
	}
}

// pollReactions samples the tally when it is due. Its errors never reach the
// comment loop: reactions are additive and must not degrade chat.
func (f *FacebookAdapter) pollReactions(ctx context.Context, videoID string, sink Sink) {
	if !f.reactionsOn() {
		// Forget the baseline so switching back on re-seeds silently instead of
		// reporting everything that came in while off. The App Review state and
		// backoff survive the toggle.
		f.mu.Lock()
		f.react = fbReactions{unavailable: f.react.unavailable, retryAfter: f.react.retryAfter}
		f.mu.Unlock()
		return
	}
	now := f.cfg.Now()
	if !f.claimReactionSample(now, videoID) {
		return
	}

	total, err := f.reactionCount(ctx, videoID, "")
	if err != nil {
		f.reactionFailed(err, now)
		return
	}
	f.mu.Lock()
	f.react.unavailable = ""
	prev, seeded := f.react.total, f.react.seeded
	f.mu.Unlock()
	delta := total - prev
	if seeded && delta == 0 {
		f.mu.Lock()
		f.react.windowStart = now
		f.mu.Unlock()
		return
	}

	// A change (or the seed): read the breakdown, so a drop moves the per-type
	// baseline too and the next rise is measured from where it really is. A type that cannot be read is
	// left out; the count is still true.
	f.mu.Lock()
	oldTypes := f.react.byType
	f.mu.Unlock()
	types := f.readReactionTypes(ctx, videoID, oldTypes)

	f.mu.Lock()
	st := &f.react
	start := st.windowStart
	st.total, st.byType, st.windowStart, st.seeded = total, types, now, true
	f.mu.Unlock()
	if !seeded || delta < 0 {
		return // the baseline, or people un-reacting: not news
	}
	breakdown := reactionBreakdown(types, oldTypes)
	emitActivity(sink, activity.Event{
		Kind:      activity.KindReaction,
		At:        now,
		Count:     int(min(delta, activity.MaxCount)),
		Breakdown: breakdown,
		Source: activity.Source{
			Type: fbSourceReactions,
			ID:   fmt.Sprintf("%s:%d", videoID, start.Unix()),
		},
	})
}

// claimReactionSample reports whether this poll should sample the reactions: the
// first poll of every third, not too soon after the last sample, and not while
// Facebook has asked to back off. A due sample is stamped with now.
func (f *FacebookAdapter) claimReactionSample(now time.Time, videoID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := &f.react
	if st.video != videoID {
		*st = fbReactions{video: videoID}
	}
	st.polls++
	due := st.polls%fbReactionPolls == 1 // the first poll, then every third
	if !due || (!st.lastSample.IsZero() && now.Sub(st.lastSample) < fbReactionEvery) || now.Before(st.retryAfter) {
		return false
	}
	st.lastSample = now
	return true
}

// readReactionTypes reads the count for each reaction type. A type that cannot
// be read is left out; the count is still true.
func (f *FacebookAdapter) readReactionTypes(ctx context.Context, videoID string, oldTypes map[string]int64) map[string]int64 {
	types := map[string]int64{}
	for _, ty := range fbReactionTypes {
		k := strings.ToLower(ty)
		if n, err := f.reactionCount(ctx, videoID, ty); err == nil {
			types[k] = n
		} else if old, ok := oldTypes[k]; ok {
			types[k] = old // keep its baseline; a failed read must not zero it
		}
	}
	return types
}

// reactionBreakdown is the per-type growth since the last baseline. nil when no
// type grew.
func reactionBreakdown(types, oldTypes map[string]int64) map[string]int {
	breakdown := map[string]int{}
	for k, n := range types {
		old, ok := oldTypes[k]
		if !ok {
			continue // no baseline for this type yet: its lifetime total is not news
		}
		if d := n - old; d > 0 {
			breakdown[k] = int(min(d, activity.MaxCount))
		}
	}
	if len(breakdown) == 0 {
		return nil
	}
	return breakdown
}

// reactionFailed records why a sample failed. Permission is a kind state; the
// rest is transient and silent.
func (f *FacebookAdapter) reactionFailed(err error, now time.Time) {
	if fbTokenDead(err) {
		return // the comment poll reports a dead token; one voice is enough
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if fbReactionPermission(err) {
		f.react.unavailable = fbReactionsNeedAccessDetail
		f.react.retryAfter = now.Add(fbReactionRetry)
		return
	}
	if wait, limited := fbRateLimited(err); limited {
		f.react.retryAfter = now.Add(min(max(wait, fbReactionEvery), fbMaxRateWait))
	}
}

// fbReactionPermission is a refusal that a missing permission or missing App
// Review explains: HTTP 403, or Graph codes 10 and 200-299.
func fbReactionPermission(err error) bool {
	if statusOf(err) == http.StatusForbidden {
		return true
	}
	c := graphCode(err)
	return c == 10 || (c >= 200 && c <= 299)
}

// reactionCount reads summary.total_count of the reactions edge, optionally for
// one type. A body with no total is an error, not a zero.
func (f *FacebookAdapter) reactionCount(ctx context.Context, videoID, typ string) (int64, error) {
	q := url.Values{"summary": {"total_count"}, "limit": {"0"}}
	if typ != "" {
		q.Set("type", typ)
	}
	endpoint := fmt.Sprintf("%s/%s/reactions?%s", f.cfg.APIBase, url.PathEscape(videoID), q.Encode())
	var out struct {
		Summary struct {
			Total *int64 `json:"total_count"`
		} `json:"summary"`
	}
	tok, usedPage := f.commentTokenSource(ctx)
	err := doJSON(ctx, f.cfg.HTTP, http.MethodGet, endpoint, tok, nil, &out)
	if err != nil {
		if usedPage {
			f.dropPageToken(err)
		}
		return 0, err
	}
	if out.Summary.Total == nil || *out.Summary.Total < 0 {
		return 0, fmt.Errorf("facebook returned no reaction total")
	}
	return *out.Summary.Total, nil
}
