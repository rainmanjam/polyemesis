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

// ytReuseWindow is how far a candidate's start may be from the marker's time plus the lead and still be taken as the
// broadcast that attempt made (the start is set to the minute, so this is generous by a margin).
const ytReuseWindow = time.Minute

// LiveNowAttemptMaxAge is how long an attempt marker is believed: much longer than an insert takes, short enough
// that days later nothing of the operator's can be mistaken for the broadcast the marker stands for.
const LiveNowAttemptMaxAge = 12 * time.Minute

// ytDefaultPrivacy is the visibility of a broadcast made here when the destination names none: public, the
// owner's decision. A destination's compliance.privacy overrides it.
const ytDefaultPrivacy = "public"

// ytBroadcastTitleMax is YouTube's limit on a broadcast title, in characters.
const ytBroadcastTitleMax = 100

// EventFields are the create-time fields beyond the title and privacy that OBS offers: the description, whether the
// broadcast is for kids, latency, DVR and 360 projection. Zero values send nothing, except that a go-live
// broadcast always states made-for-kids explicitly (false when the destination set none).
type EventFields struct {
	Description string
	// MadeForKids is status.selfDeclaredMadeForKids. nil on a go-live broadcast sends false: YouTube requires an
	// explicit answer, and "not for kids" is the default the owner chose. nil on a scheduled show sends nothing,
	// so an existing setup is unchanged.
	MadeForKids *bool
	// Latency is normal, low or ultraLow; empty leaves YouTube's default.
	Latency string
	// EnableDvr nil leaves YouTube's default (on).
	EnableDvr *bool
	// Projection360 makes the broadcast 360-degree. Off sends nothing (rectangular).
	Projection360 bool
}

// contentDetails is the create-time contentDetails these fields add, empty when none is set.
func (e EventFields) contentDetails() map[string]any {
	cd := map[string]any{}
	if e.Latency != "" {
		cd["latencyPreference"] = e.Latency
	}
	if e.EnableDvr != nil {
		cd["enableDvr"] = *e.EnableDvr
	}
	if e.Projection360 {
		cd["projection"] = "360"
	}
	return cd
}

// LiveNowRequest is what the coordinator knows about the destination.
type LiveNowRequest struct {
	EventFields
	// EventID is a picked existing broadcast to use instead of creating one. RebindEvent is the operator's
	// confirmation to move it off a different, idle stream.
	EventID     string
	RebindEvent bool
	// AttemptAt is the moment an earlier attempt of ours stamped its marker, immediately before its insert; zero
	// when there is no live marker. An upcoming broadcast that looks like ours is reused ONLY when it starts within
	// about a minute of AttemptAt plus the lead, which is what proves it is the one that attempt made; without a
	// marker nothing is adopted, because an operator's own Studio event can match the same shape.
	AttemptAt time.Time
	// BeforeInsert, when set, runs immediately before each insert and must succeed for the insert to be sent. The
	// coordinator uses it to persist the marker, so a marker exists only for a broadcast that may have been made.
	BeforeInsert func() error
	// Category is a YouTube category name applied to the created broadcast's video; a failure is a warning.
	Category string
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

// UnairedDeleter is the optional capability of removing a broadcast polyemesis created that never aired.
type UnairedDeleter interface {
	// DeleteUnairedBroadcast deletes the broadcast (one unit) only when status, the state the caller just read, is
	// created or ready. The caller is responsible for knowing polyemesis created it; see deleteBroadcast.
	DeleteUnairedBroadcast(ctx context.Context, accessToken, broadcastID, status string) error
}

var _ UnairedDeleter = (*YouTube)(nil)

// DeleteUnairedBroadcast implements UnairedDeleter.
func (y *YouTube) DeleteUnairedBroadcast(ctx context.Context, accessToken, broadcastID, status string) error {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "created", "ready":
	default:
		return fmt.Errorf("broadcast %s is %q, not created or ready, so it may have aired and is not deleted", broadcastID, status)
	}
	return y.deleteBroadcast(ctx, accessToken, broadcastID)
}

// LiveNowResult is the broadcast that was made, or the picked event that was taken over.
type LiveNowResult struct {
	// Existing is true when a picked event was used and nothing was created. A result with Existing false is a
	// broadcast polyemesis created: freshly inserted, or Reused from its own earlier attempt.
	Existing bool
	// Reused is true when the broadcast came from an earlier attempt of ours (MayReuse) and was not inserted now.
	Reused bool
	// Warnings are parts that did not apply (the category) without failing the open.
	Warnings    []string
	BroadcastID string
	// LiveChatID is the insert response's snippet.liveChatId, for the chat adapter. Empty when YouTube sent none.
	LiveChatID string
	StreamID   string
}

// ErrEventRebindNeeded marks a picked event bound to another stream, which polyemesis moves only on the
// operator's confirmation. ErrEventStreamActive marks one whose bound stream is receiving video, which is never
// moved.
var (
	ErrEventRebindNeeded = errors.New("the picked YouTube event is bound to a different stream")
	ErrEventStreamActive = errors.New("the picked YouTube event's stream is active")
	// errEventFinished is internal: the picked event is complete, revoked or gone, so the open falls back to creating.
	errEventFinished = errors.New("the picked YouTube event has finished")
)

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
// inserts a broadcast and binds it. Quota: the list, the active read, the insert and the bind are one unit each, plus the upcoming read on a retry, so
// four units a go-live, five when it looks for an earlier attempt's broadcast (a failed bind adds one for the delete).
func (y *YouTube) OpenLiveNow(ctx context.Context, accessToken string, req LiveNowRequest) (*LiveNowResult, error) {
	if strings.TrimSpace(req.StreamKey) == "" {
		return nil, errors.New("this destination has no stream key, so there is no stream to bind a broadcast to")
	}
	streams, err := y.listStreamsParts(ctx, accessToken, "id,snippet,cdn,status")
	if err != nil {
		return nil, err
	}
	stream, err := chooseLiveNowStream(streams, req)
	if err != nil {
		return nil, err
	}
	active, err := y.activeOnStream(ctx, accessToken, stream.ID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.EventID) != "" {
		r, err := y.adoptEvent(ctx, accessToken, req, stream, streams, active)
		if !errors.Is(err, errEventFinished) {
			return r, err
		}
		// A finished event is never reused: fall through to creating one.
	}
	if active != "" {
		return nil, &ErrActiveBroadcastOnStream{BroadcastID: active}
	}
	// A broadcast of ours that an earlier attempt made and did not finish recording (the answer was lost, the daemon
	// died, the record write failed) is reused instead of making another, but only when the caller can prove an
	// earlier attempt happened (MayReuse). One more unit when it looks.
	var created *ytBroadcast
	if !req.AttemptAt.IsZero() {
		if created, err = y.ownUpcoming(ctx, accessToken, req, stream.ID); err != nil {
			return nil, err
		}
	}
	reused := created != nil
	if created == nil {
		if req.BeforeInsert != nil {
			if err := req.BeforeInsert(); err != nil {
				return nil, fmt.Errorf("polyemesis could not record that it was about to create a broadcast, so it did not: %w", err)
			}
		}
		if created, err = y.insertLiveNow(ctx, accessToken, req); err != nil {
			return nil, err
		}
	}
	if err := y.bindBroadcast(ctx, accessToken, created.ID, stream.ID); err != nil {
		if reused {
			// Not ours to delete on the strength of a heuristic match: leave it, and say so.
			return nil, fmt.Errorf("YouTube would not bind broadcast %s, left from an earlier attempt, to the stream: %w", created.ID, err)
		}
		// Deleted, not named in a message: see IngestFor's long comment for why an orphan is the worse outcome.
		if y.deleteBroadcast(ctx, accessToken, created.ID) == nil {
			return nil, fmt.Errorf("YouTube would not bind the new broadcast %s to the stream, so it was deleted "+
				"rather than left as an event page for a show that publishes elsewhere: %w", created.ID, err)
		}
		return nil, fmt.Errorf("YouTube created broadcast %s, would not bind it to the stream, and would not delete "+
			"it either; delete broadcast %s in YouTube Studio: %w", created.ID, created.ID, err)
	}
	res := &LiveNowResult{BroadcastID: created.ID, LiveChatID: created.Snippet.LiveChatID, StreamID: stream.ID, Reused: reused}
	if strings.TrimSpace(req.Category) != "" {
		// The category lives on the video, a second round trip (about 52 units). The broadcast is already made and
		// bound, so a refusal here is a warning and never fails the open.
		if _, err := y.setCategory(ctx, accessToken, created.ID, req.Category); err != nil {
			res.Warnings = append(res.Warnings, "the category was not applied: "+err.Error())
		}
	}
	return res, nil
}

// ownUpcoming finds an upcoming broadcast an earlier attempt of ours made for this open: created or ready, bound to
// this stream or not yet bound, with our title, auto-start on and auto-stop off (what insertLiveNow sends), and a
// start time within the lead window of now. It is a heuristic by necessity: YouTube has no field to mark a broadcast
// as ours, and a false match would only reuse an event the operator made with the same title and settings.
func (y *YouTube) ownUpcoming(ctx context.Context, accessToken string, req LiveNowRequest, streamID string) (*ytBroadcast, error) {
	var list struct {
		Items []ytBroadcast `json:"items"`
	}
	q := url.Values{}
	q.Set("part", "id,snippet,status,contentDetails")
	q.Set("broadcastStatus", "upcoming")
	q.Set("broadcastType", "all")
	q.Set("maxResults", "50")
	if err := y.getJSON(ctx, y.apiEndpoint()+ytBroadcastsPath+"?"+q.Encode(), accessToken, nil, &list); err != nil {
		return nil, err
	}
	title := liveNowTitle(req.Title)
	for i := range list.Items {
		b := &list.Items[i]
		if b.Snippet.Title != title || !b.ContentDetails.EnableAutoStart || b.ContentDetails.EnableAutoStop {
			continue
		}
		if bound := b.ContentDetails.BoundStreamID; bound != "" && bound != streamID {
			continue
		}
		if st := strings.ToLower(b.Status.LifeCycleStatus); st != "created" && st != "ready" {
			continue
		}
		start, err := time.Parse(time.RFC3339, b.Snippet.ScheduledStartTime)
		want := req.AttemptAt.Add(ytLiveNowLead)
		if err != nil || start.Sub(want) > ytReuseWindow || want.Sub(start) > ytReuseWindow {
			continue
		}
		return b, nil
	}
	return nil, nil
}

// liveNowTitle is the title insertLiveNow sends for a requested one.
func liveNowTitle(requested string) string {
	title := []rune(strings.TrimSpace(requested))
	if len(title) == 0 {
		title = []rune("polyemesis live")
	}
	if len(title) > ytBroadcastTitleMax {
		title = title[:ytBroadcastTitleMax]
	}
	return string(title)
}

// adoptEvent takes over the picked event: bound to this destination's stream (moved only when confirmed and the
// other stream is idle), recorded, and its own chat used. It changes none of the event's own settings. A finished
// or vanished event returns errEventFinished so the caller creates a broadcast instead; a live event on this stream
// that is not the picked one is left alone (ErrActiveBroadcastOnStream).
func (y *YouTube) adoptEvent(ctx context.Context, accessToken string, req LiveNowRequest, stream *ytLiveStream,
	streams []ytLiveStream, activeOnStream string) (*LiveNowResult, error) {
	id := strings.TrimSpace(req.EventID)
	b, err := y.broadcastByID(ctx, accessToken, id)
	if errors.Is(err, errBroadcastNotListed) {
		return nil, errEventFinished
	}
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(b.Status.LifeCycleStatus) {
	case "complete", "revoked":
		return nil, errEventFinished
	}
	if activeOnStream != "" && activeOnStream != id {
		return nil, &ErrActiveBroadcastOnStream{BroadcastID: activeOnStream}
	}
	if bound := b.ContentDetails.BoundStreamID; bound != stream.ID {
		if bound != "" {
			for i := range streams {
				if streams[i].ID == bound && strings.EqualFold(streams[i].Status.StreamStatus, "active") {
					return nil, fmt.Errorf("the picked event is bound to a stream that is receiving video right now, "+
						"so polyemesis will not move it (stream %s): %w", bound, ErrEventStreamActive)
				}
			}
			if !req.RebindEvent {
				return nil, fmt.Errorf("the picked event is bound to another stream (%s); confirm moving it to this "+
					"destination's stream in the destination dialog, or pick another event: %w", bound, ErrEventRebindNeeded)
			}
		}
		if err := y.bindBroadcast(ctx, accessToken, id, stream.ID); err != nil {
			return nil, fmt.Errorf("YouTube would not bind the picked event %s to this destination's stream: %w", id, err)
		}
	}
	return &LiveNowResult{Existing: true, BroadcastID: id, LiveChatID: b.Snippet.LiveChatID, StreamID: stream.ID}, nil
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
	title := liveNowTitle(req.Title)
	privacy := strings.TrimSpace(req.Privacy)
	if privacy == "" {
		privacy = ytDefaultPrivacy
	}
	details := req.EventFields.contentDetails()
	details["enableAutoStart"], details["enableAutoStop"] = true, false
	if !req.CaptionPOST {
		// EXPLICIT, because YouTube's default is ON and the testing transition then has preconditions that fail for a
		// stream that was already active when the broadcast was bound (auto-start never fires). With the monitor stream
		// off the coordinator's fallback goes straight to live. A caption destination needs it on and sets it below.
		details["monitorStream"] = map[string]any{"enableMonitorStream": false}
	}
	if req.CaptionPOST {
		details["closedCaptionsType"] = ytClosedCaptionsHTTPPost
		details["monitorStream"] = map[string]any{
			"enableMonitorStream":    true,
			"broadcastStreamDelayMs": int(req.CaptionDelay / time.Millisecond),
		}
	}
	snippet := map[string]any{
		"title":              title,
		"scheduledStartTime": time.Now().Add(ytLiveNowLead).UTC().Format(time.RFC3339),
	}
	if d := strings.TrimSpace(req.Description); d != "" {
		snippet["description"] = d
	}
	// Explicit, and false unless the destination declared otherwise.
	kids := false
	if req.MadeForKids != nil {
		kids = *req.MadeForKids
	}
	payload := map[string]any{
		"snippet":        snippet,
		"status":         map[string]any{"privacyStatus": privacy, "selfDeclaredMadeForKids": kids},
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
