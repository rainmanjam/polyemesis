package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// YouTube's planned rollover (#19): replace a live broadcast with a fresh one on the same ingest, before the
// platform's archive cap lands on it.
//
// THE ORDER IS THE WHOLE DESIGN, and each step is there because the step before it can fail without harm.
//
//  1. Read the old broadcast's state. Nothing is written unless it is live, and it must be bound to a stream.
//  2. Create the new broadcast, with its own id, carrying the title, description and privacy the caller chose.
//  3. Bind it to the OLD broadcast's stream. Never streamFor: that first-rtmp fallback can adopt the #7
//     vertical stream, and a broadcast bound to the vertical stream is the wrong broadcast on air.
//  4. Take the new broadcast live. A refusal here leaves the old broadcast running, and the new one is removed,
//     because it is an event page nobody asked for.
//  5. Only once the new broadcast is live, end the old one. The one stream feeds both for a moment, which is the
//     point: the viewer never sees the feed stop.
//
// If the channel will not carry two live broadcasts at once (channelFull, sharedIngestionFull), the old one is
// ended first and the new one is taken live straight after. That is a gap, and the outcome says so. If the second
// half of that fails, the new broadcast is left in its ready state rather than removed, so the destination's
// coordinator can take it live as soon as the stream is delivering, which is how it would take any new broadcast live.
//
// NOTHING HERE STOPS A STREAM. The only writes are broadcast writes; an ingest is never touched. That is the rule
// internal/api/lifecycle.go states for its own transitions, and this path keeps it.
//
// PROVISIONAL, NOT VERIFIED ON A LIVE CHANNEL. Whether auto-start fires on a broadcast bound to an already-active
// stream is unknown, so both outcomes are handled: a broadcast that is already live is not transitioned again, and
// one that is not is transitioned explicitly. The 12-hour archive behaviour that makes a rollover worth doing is
// documented; the rollover mechanics are not, and they are the thing to check on a test channel first.

// RolloverRequest is what the caller asks for. Title, Description and Privacy come from the caller, which decides
// them from the recurring template the live broadcast came from, or leaves them empty to copy the live broadcast.
type RolloverRequest struct {
	OldBroadcastID string
	// Title, when set, is the new broadcast's title. Empty copies the old broadcast's title.
	Title string
	// Description and Privacy follow the same rule. Privacy is one of public, unlisted or private.
	Description string
	Privacy     string
	// Now is the rollover's own clock. The new broadcast is scheduled ytRolloverStartLead after it, because YouTube
	// wants a scheduledStartTime in the future and Now is already past when the insert lands.
	Now time.Time
}

// RolloverOutcome is what a rollover did. A nil error with a set NewBroadcastID means the new broadcast is live
// and the destination should be pointed at it, even when Warnings says the old one could not be ended.
type RolloverOutcome struct {
	NewBroadcastID string
	// OldEnded is whether the old broadcast was ended by this rollover.
	OldEnded bool
	// Gap is true when the old broadcast ended before the new one went live. The feed still flowed, but viewers
	// saw a break. PROVISIONAL: it happens only when the channel refuses two live broadcasts at once.
	Gap bool
	// Warnings are things an operator must act on that did not stop the rollover, such as an old broadcast that
	// stayed live because it could not be ended.
	Warnings []string
}

// RolloverError is a rollover that did not complete. Stage names the step that failed, OldStillLive says
// whether the old broadcast is still on air, and NewBroadcastID is set if a new broadcast exists after the failure.
type RolloverError struct {
	Stage          string
	OldStillLive   bool
	NewBroadcastID string
	err            error
}

func (e *RolloverError) Error() string {
	return fmt.Sprintf("the planned rollover stopped at %s: %v", e.Stage, e.err)
}

func (e *RolloverError) Unwrap() error { return e.err }

// rolloverSource is the old broadcast's settings, read so the new one matches them where the caller did not decide.
type rolloverSource struct {
	Title           string
	Description     string
	Privacy         string
	EnableAutoStart bool
}

// RollOverBroadcast carries out a planned rollover of one YouTube broadcast. See the file comment for the order.
func (y *YouTube) RollOverBroadcast(ctx context.Context, accessToken string, req RolloverRequest) (*RolloverOutcome, error) {
	oldID := strings.TrimSpace(req.OldBroadcastID)
	if oldID == "" {
		return nil, &RolloverError{Stage: "read", err: fmt.Errorf("no live broadcast id to roll over")}
	}
	state, err := y.BroadcastState(ctx, accessToken, oldID)
	if err != nil {
		return nil, &RolloverError{Stage: "read", OldStillLive: true, err: err}
	}
	if !broadcastIsOnAir(state.Status) {
		return nil, &RolloverError{Stage: "read", err: fmt.Errorf("broadcast %s is %q, not live, so there is nothing to roll over", oldID, state.Status)}
	}
	if state.BoundStreamID == "" {
		return nil, &RolloverError{Stage: "read", OldStillLive: true, err: fmt.Errorf("broadcast %s is not bound to a stream, so a replacement has no ingest to join", oldID)}
	}
	src, err := y.rolloverSourceFor(ctx, accessToken, oldID)
	if err != nil {
		return nil, &RolloverError{Stage: "read", OldStillLive: true, err: err}
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = src.Title
	}
	description := firstNonEmptyString(req.Description, src.Description)
	privacy := strings.ToLower(strings.TrimSpace(req.Privacy))
	if privacy == "" {
		privacy = src.Privacy
	}
	if privacy == "" {
		// Fail closed. A source whose privacy cannot be read must not widen to public, so the
		// replacement starts private and a person sets it deliberately.
		privacy = "private"
	}

	// A broadcast taking captions by HTTP POST passes them on (#8). closedCaptionsType can be set only while the
	// replacement is created or ready (docs-youtube B13), so it is born with them, with the monitor stream on and the
	// old broadcast's delay (C5, C7). Without this the replacement went live with captions off and every POST to the
	// ingestion URL showed nothing, with no warning.
	captionPOST := state.ClosedCaptionsType == ytClosedCaptionsHTTPPost
	newID, err := y.createRolloverBroadcast(ctx, accessToken, rolloverBody{
		Title: title, Description: description, Privacy: privacy,
		EnableAutoStart: src.EnableAutoStart, At: req.Now.Add(ytRolloverStartLead),
		CaptionPOST: captionPOST, CaptionDelayMs: rolloverCaptionDelay(state.StreamDelayMs),
	})
	if err != nil {
		return nil, &RolloverError{Stage: "create", OldStillLive: true, err: err}
	}

	// From here on a new broadcast exists. Every failure before it is live removes it, because it is a public event
	// page nobody is watching, and the next attempt will create its own.
	abandon := func(stage string, cause error) (*RolloverOutcome, error) {
		if delErr := y.deleteBroadcast(ctx, accessToken, newID); delErr != nil {
			return nil, &RolloverError{Stage: stage, OldStillLive: true, NewBroadcastID: newID,
				err: fmt.Errorf("%w; the replacement %s could not be removed either, so delete it in YouTube Studio: %v", cause, newID, delErr)}
		}
		return nil, &RolloverError{Stage: stage, OldStillLive: true, err: cause}
	}

	if err := y.bindBroadcast(ctx, accessToken, newID, state.BoundStreamID); err != nil {
		return abandon("bind", err)
	}

	gap := false
	newState, err := y.BroadcastState(ctx, accessToken, newID)
	if err != nil {
		return abandon("start", err)
	}
	if captionPOST && !broadcastIsOnAir(newState.Status) {
		// The monitor stream the captions need forces a testing stage before live (B14, B22).
		if err := y.reachTesting(ctx, accessToken, newID, newState.Status); err != nil {
			return abandon("testing", err)
		}
	}
	if !broadcastIsOnAir(newState.Status) {
		_, terr := y.TransitionBroadcast(ctx, accessToken, newID, PhaseLive)
		if terr != nil {
			var refused *TransitionRefused
			if !errors.As(terr, &refused) || !channelCannotCarryTwo(refused.Refusal) {
				// A failed answer does not prove the transition did not happen: a 500 can follow a transition
				// YouTube carried out, and auto-start can take the replacement live in the meantime. Deleting a
				// broadcast that is on air would break the rule this file keeps, so the state is read again first.
				again, rerr := y.BroadcastState(ctx, accessToken, newID)
				if rerr != nil {
					// Nothing here knows whether the replacement is on air, so it is kept and named.
					return nil, &RolloverError{Stage: "start", OldStillLive: true, NewBroadcastID: newID,
						err: fmt.Errorf("%w; the replacement %s could not be read again, so it was kept rather than deleted; check it in YouTube Studio: %v", terr, newID, rerr)}
				}
				if !broadcastIsOnAir(again.Status) {
					return abandon("start", terr)
				}
				// The replacement is on air after all, so the rollover carries on below and ends the old one.
			} else {
				// The channel will not carry two live broadcasts at once. End the old one, then take the new one live.
				gap = true
				if _, endErr := y.TransitionBroadcast(ctx, accessToken, oldID, PhaseComplete); endErr != nil {
					return abandon("end", endErr)
				}
				if _, startErr := y.TransitionBroadcast(ctx, accessToken, newID, PhaseLive); startErr != nil {
					// The gap is now real and the new broadcast will not start. Keep it, so the coordinator can take
					// it live once the stream is delivering, and report that the old one is ended.
					return nil, &RolloverError{Stage: "start", OldStillLive: false, NewBroadcastID: newID,
						err: fmt.Errorf("the old broadcast was ended to make room, but the replacement %s would not go live: %w", newID, startErr)}
				}
				return &RolloverOutcome{NewBroadcastID: newID, OldEnded: true, Gap: true}, nil
			}
		}
	}

	out := &RolloverOutcome{NewBroadcastID: newID, Gap: gap}
	if _, err := y.TransitionBroadcast(ctx, accessToken, oldID, PhaseComplete); err != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf("the replacement %s is live, but the old broadcast %s could not be ended and is still on air; end it in YouTube Studio: %v", newID, oldID, err))
		return out, nil
	}
	out.OldEnded = true
	return out, nil
}

// broadcastIsOnAir reports whether a YouTube lifeCycleStatus means the broadcast is already carrying the feed to
// viewers, or on its way to. Anything else is not live, and an unknown word is never read as live.
func broadcastIsOnAir(status string) bool {
	return status == "live" || status == "liveStarting"
}

// channelCannotCarryTwo names the two refusals that mean the channel or its ingest will not hold a second live
// broadcast next to the old one. Those, and only those, make the gap path the right move.
func channelCannotCarryTwo(r TransitionRefusal) bool {
	return r == RefusalChannelFull || r == RefusalSharedIngestionFull
}

// rolloverSourceFor reads the old broadcast's snippet, status and contentDetails in one call. It is a separate read
// from BroadcastState on purpose: BroadcastState reads only what the coordinator needs, and the rollover needs the
// description, the privacy and the auto-start setting as well.
func (y *YouTube) rolloverSourceFor(ctx context.Context, accessToken, broadcastID string) (rolloverSource, error) {
	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"snippet"`
			Status struct {
				PrivacyStatus string `json:"privacyStatus"`
			} `json:"status"`
			ContentDetails struct {
				EnableAutoStart bool `json:"enableAutoStart"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	q := url.Values{}
	q.Set("part", "id,snippet,status,contentDetails")
	q.Set("id", broadcastID)
	if err := y.getJSON(ctx, y.apiEndpoint()+ytBroadcastsPath+"?"+q.Encode(), accessToken, nil, &list); err != nil {
		return rolloverSource{}, err
	}
	for _, it := range list.Items {
		if it.ID != broadcastID {
			continue
		}
		return rolloverSource{
			Title:           it.Snippet.Title,
			Description:     it.Snippet.Description,
			Privacy:         strings.ToLower(it.Status.PrivacyStatus),
			EnableAutoStart: it.ContentDetails.EnableAutoStart,
		}, nil
	}
	return rolloverSource{}, fmt.Errorf("YouTube no longer lists broadcast %s on this channel", broadcastID)
}

// rolloverBody is what a rollover's create sends.
type rolloverBody struct {
	Title           string
	Description     string
	Privacy         string
	EnableAutoStart bool
	At              time.Time
	// CaptionPOST makes the replacement take captions by HTTP POST, with the monitor stream on and CaptionDelayMs
	// of delay, the way the old broadcast did.
	CaptionPOST    bool
	CaptionDelayMs int
}

// rolloverCaptionDelay is the old broadcast's caption delay when it is one YouTube accepts for captions by HTTP POST
// (30 s or 60 s, docs-youtube C5), and 30 s otherwise.
func rolloverCaptionDelay(ms *int) int {
	if ms != nil && (*ms == 30000 || *ms == 60000) {
		return *ms
	}
	return 30000
}

// ytRolloverTestingWait is how long a caption rollover waits, read by read, for its replacement to reach testing
// after the transition is accepted. PROVISIONAL: YouTube documents testStarting but not how long it lasts.
var ytRolloverTestingWait = []time.Duration{
	2 * time.Second, 3 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second, 10 * time.Second,
}

// reachTesting takes a caption replacement to testing and waits until YouTube reports it there, because live is
// refused until it is. status is the replacement's last read state.
func (y *YouTube) reachTesting(ctx context.Context, accessToken, id, status string) error {
	if status != "testing" && status != "testStarting" {
		if _, err := y.TransitionBroadcast(ctx, accessToken, id, PhaseTesting); err != nil {
			return err
		}
	}
	waits := y.testingWait
	if waits == nil {
		waits = ytRolloverTestingWait
	}
	for attempt := 0; ; attempt++ {
		st, err := y.BroadcastState(ctx, accessToken, id)
		if err != nil {
			return err
		}
		if st.Status == "testing" {
			return nil
		}
		if attempt >= len(waits) {
			return fmt.Errorf("the replacement %s did not reach testing (it is %q), and a broadcast with captions by HTTP POST must pass through testing before it can go live", id, st.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waits[attempt]):
		}
	}
}

// createRolloverBroadcast is liveBroadcasts.insert for a replacement. It is separate from createScheduledBroadcast,
// which deliberately names only snippet and status and sends no contentDetails. A rollover must send the monitor
// setting, because YouTube defaults the monitor stream on, and an enabled monitor stream forces a testing stage
// the rollover must skip. It sends enableAutoStart explicitly, to match the old broadcast, because the setting
// cannot change once the stream is active.
func (y *YouTube) createRolloverBroadcast(ctx context.Context, accessToken string, b rolloverBody) (string, error) {
	payload := map[string]any{
		"snippet": map[string]any{
			"title":              b.Title,
			"description":        b.Description,
			"scheduledStartTime": b.At.UTC().Format(time.RFC3339),
		},
		"status": map[string]any{"privacyStatus": b.Privacy},
		// The monitor flag is nested under monitorStream, the same shape lifecycle.go and youtube_broadcast.go
		// use. A flat contentDetails.enableMonitorStream key is ignored by YouTube.
		"contentDetails": map[string]any{
			"monitorStream":   map[string]any{"enableMonitorStream": false},
			"enableAutoStart": b.EnableAutoStart,
		},
	}
	if b.CaptionPOST {
		// The one case the monitor stream is on: captions by HTTP POST need its delay (docs-youtube C7), and the
		// testing stage it forces is taken by reachTesting.
		payload["contentDetails"] = map[string]any{
			"closedCaptionsType": ytClosedCaptionsHTTPPost,
			"monitorStream": map[string]any{
				"enableMonitorStream":    true,
				"broadcastStreamDelayMs": b.CaptionDelayMs,
			},
			"enableAutoStart": b.EnableAutoStart,
		}
	}
	var created ytBroadcast
	err := y.requestJSON(ctx, http.MethodPost,
		y.apiEndpoint()+ytBroadcastsPath+"?part=snippet,status,contentDetails",
		accessToken, payload, nil, &created)
	if err != nil {
		return "", ytBroadcastCreateAdvice(err)
	}
	if strings.TrimSpace(created.ID) == "" {
		return "", fmt.Errorf("YouTube accepted the replacement broadcast but returned no id, so nothing here can move it or end it; check YouTube Studio before rolling over again")
	}
	return created.ID, nil
}

// ytRolloverStartLead is how far ahead of the rollover's clock the replacement is scheduled. YouTube's insert wants
// a scheduledStartTime in the future (docs-youtube B3), and "now" is already past when the request lands.
// PROVISIONAL: YouTube documents no minimum lead, so two minutes is a choice, small enough that the replacement is
// still the show starting now.
const ytRolloverStartLead = 2 * time.Minute

func firstNonEmptyString(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// BroadcastRollover is the optional capability for a platform whose live broadcast can be replaced without the
// destination being torn down. Only YouTube has one today; a platform with no such method is never offered a
// rollover action, and its countdown is shown but not acted on.
type BroadcastRollover interface {
	Provider
	RollOverBroadcast(ctx context.Context, accessToken string, req RolloverRequest) (*RolloverOutcome, error)
	RolloverSessionStart(ctx context.Context, accessToken, broadcastID string) (time.Time, error)
}

var _ BroadcastRollover = (*YouTube)(nil)

// RolloverFor reports whether a platform has a broadcast rollover, and the bool is the whole answer the caller
// branches on, as with As[BroadcastLifecycler].
func RolloverFor(p db.Platform) (BroadcastRollover, bool) {
	prov, err := Get(p)
	if err != nil {
		return nil, false
	}
	br, ok := prov.(BroadcastRollover)
	return br, ok
}

// RolloverSessionStart reads the platform's own live start for a broadcast: snippet.actualStartTime. It is zero
// when YouTube has not recorded one yet, and the caller then waits rather than counting from a guess.
func (y *YouTube) RolloverSessionStart(ctx context.Context, accessToken, broadcastID string) (time.Time, error) {
	id := strings.TrimSpace(broadcastID)
	if id == "" {
		return time.Time{}, fmt.Errorf("a YouTube broadcast id is required to read its start time")
	}
	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				ActualStartTime string `json:"actualStartTime"`
			} `json:"snippet"`
		} `json:"items"`
	}
	q := url.Values{}
	q.Set("part", "id,snippet")
	q.Set("id", id)
	if err := y.getJSON(ctx, y.apiEndpoint()+ytBroadcastsPath+"?"+q.Encode(), accessToken, nil, &list); err != nil {
		return time.Time{}, err
	}
	for _, it := range list.Items {
		if it.ID != id {
			continue
		}
		if parsed := parseYouTubeTime(it.Snippet.ActualStartTime); parsed != nil {
			return *parsed, nil
		}
		return time.Time{}, nil
	}
	return time.Time{}, fmt.Errorf("YouTube no longer lists broadcast %s on this channel", id)
}
