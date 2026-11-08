package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// YouTube's live-now broadcast: the one polyemesis creates for a destination that is going out right now, so the
// broadcast it reads chat from and ends is its own rather than whatever YouTube Studio's "Stream now" makes.
//
// THE RULES THIS FILE KEEPS, each with a test:
//
//   - IT NEVER MAKES A STREAM AND NEVER MOVES A KEY. The stream is found by the destination's stored key (or a
//     stream id the operator pinned) in one liveStreams.list. A key the channel does not list is an error that
//     names the cause and leaves the operator's key where it is: pickStream's "create one when none matches" is
//     exactly what this must not inherit, because a new key would silently break the encoder that holds the old.
//   - IT NEVER MAKES A SECOND BROADCAST ON A STREAM THAT ALREADY HAS A LIVE ONE (ErrActiveBroadcastOnStream), so
//     a destination that was already on air in Studio when polyemesis started driving it is left alone.
//   - AUTO-START IS ON AND AUTO-STOP IS OFF. A reconnect gap must never complete the broadcast: polyemesis ends
//     it, through the coordinator, when the operator switches the destination off.

// ytLiveNowLead is how far ahead scheduledStartTime is set. snippet.scheduledStartTime is required on insert, and
// the broadcast goes live when video arrives (auto-start), not at this time.
const ytLiveNowLead = 2 * time.Minute

// ytDefaultPrivacy is the visibility of a broadcast made here when the destination names none: public, the
// owner's decision. A destination's compliance.privacy overrides it.
const ytDefaultPrivacy = "public"

// ytBroadcastTitleMax is YouTube's limit on a broadcast title, in characters.
const ytBroadcastTitleMax = 100

// LiveNowRequest is what the coordinator knows about the destination.
type LiveNowRequest struct {
	// StreamKey is the key the destination publishes with. It selects the stream and is never sent anywhere.
	StreamKey string
	// StreamID pins the stream; empty means the one StreamKey belongs to.
	StreamID string
	// Title is the broadcast title. Empty falls back to a plain name.
	Title string
	// Privacy is public, unlisted or private; empty means public.
	Privacy string
	// CaptionPOST makes the broadcast with the caption settings an HTTP POST caption destination needs.
	CaptionPOST  bool
	CaptionDelay time.Duration
}

// LiveNowResult is the broadcast that was made.
type LiveNowResult struct {
	BroadcastID string
	// LiveChatID is the insert response's snippet.liveChatId, for the chat adapter. Empty when YouTube sent none.
	LiveChatID string
	StreamID   string
}

// ErrStreamNotListed marks a stored key (or pinned stream) the channel does not list.
var ErrStreamNotListed = errors.New("YouTube does not list a stream for this destination")

// ErrActiveBroadcastOnStream marks a stream that already carries a live or testing broadcast.
type ErrActiveBroadcastOnStream struct{ BroadcastID string }

func (e *ErrActiveBroadcastOnStream) Error() string {
	return fmt.Sprintf("YouTube already has an active broadcast (%s) on this stream", e.BroadcastID)
}

var _ BroadcastCreator = (*YouTube)(nil)

// LivestreamAccess is always open: YouTube has no Livestream gate.
func (y *YouTube) LivestreamAccess() bool { return true }

// OpenBroadcast implements BroadcastCreator with the defaults. The coordinator uses OpenLiveNow, which carries the
// destination's title and privacy and returns the chat id.
func (y *YouTube) OpenBroadcast(ctx context.Context, accessToken, sourceKey string) (string, error) {
	r, err := y.OpenLiveNow(ctx, accessToken, LiveNowRequest{StreamKey: sourceKey})
	if err != nil {
		return "", err
	}
	return r.BroadcastID, nil
}

// OpenLiveNow lists the channel's streams, finds the destination's, refuses to double up on a live broadcast, then
// inserts a broadcast and binds it. Quota: the list, the active read, the insert and the bind are one unit each, so
// four units a go-live (a failed bind adds one for the delete).
func (y *YouTube) OpenLiveNow(ctx context.Context, accessToken string, req LiveNowRequest) (*LiveNowResult, error) {
	if strings.TrimSpace(req.StreamKey) == "" {
		return nil, errors.New("this destination has no stream key, so there is no stream to bind a broadcast to")
	}
	streams, err := y.listStreams(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	stream, err := chooseLiveNowStream(streams, req)
	if err != nil {
		return nil, err
	}
	if active, err := y.activeOnStream(ctx, accessToken, stream.ID); err != nil {
		return nil, err
	} else if active != "" {
		return nil, &ErrActiveBroadcastOnStream{BroadcastID: active}
	}
	created, err := y.insertLiveNow(ctx, accessToken, req)
	if err != nil {
		return nil, err
	}
	if err := y.bindBroadcast(ctx, accessToken, created.ID, stream.ID); err != nil {
		// Deleted, not named in a message: see IngestFor's long comment for why an orphan is the worse outcome.
		if y.deleteBroadcast(ctx, accessToken, created.ID) == nil {
			return nil, fmt.Errorf("YouTube would not bind the new broadcast %s to the stream, so it was deleted "+
				"rather than left as an event page for a show that publishes elsewhere: %w", created.ID, err)
		}
		return nil, fmt.Errorf("YouTube created broadcast %s, would not bind it to the stream, and would not delete "+
			"it either; delete broadcast %s in YouTube Studio: %w", created.ID, created.ID, err)
	}
	return &LiveNowResult{BroadcastID: created.ID, LiveChatID: created.Snippet.LiveChatID, StreamID: stream.ID}, nil
}

// chooseLiveNowStream picks the destination's stream and never creates one.
func chooseLiveNowStream(streams []ytLiveStream, req LiveNowRequest) (*ytLiveStream, error) {
	if id := strings.TrimSpace(req.StreamID); id != "" {
		for i := range streams {
			if streams[i].ID != id {
				continue
			}
			if streams[i].CDN.IngestionInfo.StreamName != req.StreamKey {
				// The broadcast would be bound to a stream that receives nothing: the encoder publishes with a
				// different key. Neither key is named.
				return nil, fmt.Errorf("the pinned YouTube stream %s is not the stream this destination's key "+
					"belongs to, so a broadcast bound to it would stay dark; pick the stream matching the key, "+
					"or refresh the stream key: %w", id, ErrStreamNotListed)
			}
			return &streams[i], nil
		}
		return nil, fmt.Errorf("the pinned YouTube stream %s is no longer on this channel; pick another stream: %w",
			id, ErrStreamNotListed)
	}
	if s := streamWithKey(streams, req.StreamKey); s != nil {
		return s, nil
	}
	return nil, fmt.Errorf("none of this channel's YouTube streams carries this destination's stored stream key "+
		"(it was deleted in YouTube Studio, or the key belongs to another channel); polyemesis will not replace "+
		"it, because the encoder holds it. Refresh the stream key, or paste the right one: %w", ErrStreamNotListed)
}

// activeOnStream is the id of an active (live or testing) broadcast bound to streamID, or "".
func (y *YouTube) activeOnStream(ctx context.Context, accessToken, streamID string) (string, error) {
	var list struct {
		Items []ytBroadcast `json:"items"`
	}
	q := url.Values{}
	q.Set("part", "id,snippet,contentDetails")
	q.Set("broadcastStatus", "active")
	q.Set("broadcastType", "all")
	q.Set("maxResults", "50")
	if err := y.getJSON(ctx, y.apiEndpoint()+ytBroadcastsPath+"?"+q.Encode(), accessToken, nil, &list); err != nil {
		return "", err
	}
	for _, b := range list.Items {
		if b.ContentDetails.BoundStreamID == streamID {
			return b.ID, nil
		}
	}
	return "", nil
}

// insertLiveNow is liveBroadcasts.insert for a go-live. part names exactly the three parts the body carries.
func (y *YouTube) insertLiveNow(ctx context.Context, accessToken string, req LiveNowRequest) (*ytBroadcast, error) {
	title := []rune(strings.TrimSpace(req.Title))
	if len(title) == 0 {
		title = []rune("polyemesis live")
	}
	if len(title) > ytBroadcastTitleMax {
		title = title[:ytBroadcastTitleMax]
	}
	privacy := strings.TrimSpace(req.Privacy)
	if privacy == "" {
		privacy = ytDefaultPrivacy
	}
	details := map[string]any{"enableAutoStart": true, "enableAutoStop": false}
	if req.CaptionPOST {
		details["closedCaptionsType"] = ytClosedCaptionsHTTPPost
		details["monitorStream"] = map[string]any{
			"enableMonitorStream":    true,
			"broadcastStreamDelayMs": int(req.CaptionDelay / time.Millisecond),
		}
	}
	payload := map[string]any{
		"snippet": map[string]any{
			"title":              string(title),
			"scheduledStartTime": time.Now().Add(ytLiveNowLead).UTC().Format(time.RFC3339),
		},
		"status":         map[string]any{"privacyStatus": privacy},
		"contentDetails": details,
	}
	var created ytBroadcast
	// requestJSON, not postJSON: ytBroadcastCreateAdvice needs the whole error body. See createScheduledBroadcast.
	err := y.requestJSON(ctx, http.MethodPost, y.apiEndpoint()+ytBroadcastsPath+"?part=snippet,status,contentDetails",
		accessToken, payload, nil, &created)
	if err != nil {
		return nil, ytBroadcastCreateAdvice(err)
	}
	if strings.TrimSpace(created.ID) == "" {
		return nil, errors.New("YouTube accepted the broadcast but returned no id, so nothing here can bind or end " +
			"it; check YouTube Studio before switching this destination on again")
	}
	return &created, nil
}

// LiveNowOpener is the BroadcastCreator whose open carries the destination's title, privacy and pinned stream and
// returns the chat id. The coordinator asks for it by name, as it does for X's publisher.
type LiveNowOpener interface {
	BroadcastCreator
	OpenLiveNow(ctx context.Context, accessToken string, req LiveNowRequest) (*LiveNowResult, error)
}

var _ LiveNowOpener = (*YouTube)(nil)

// YouTubeQuotaSpent reports whether err is YouTube saying the project's daily allowance is used up (a 403 with
// quotaExceeded or dailyLimitExceeded). A short rate-limit window is not that.
func YouTubeQuotaSpent(err error) bool {
	if err == nil {
		return false
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.Status == http.StatusForbidden && bodyNamesSpentQuota([]byte(adviceBody(err)))
	}
	// getJSON flattens a refusal into text carrying the status and the start of the body.
	msg := err.Error()
	return strings.Contains(msg, "(403)") && bodyNamesSpentQuota([]byte(msg))
}
