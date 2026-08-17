package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// YouTube's broadcast lifecycle: the only state machine in this package that
// answers to a command.
//
// Everything here is covered by the `youtube` scope the provider already asks
// for (youtube.go:41). The transition reference lists `youtube` OR
// `youtube.force-ssl` for all three targets, liveBroadcasts.list accepts either
// plus the narrower youtube.readonly, and liveStreams.list the same three. NO
// SCOPE CHANGE AND NO ScopeVersion BUMP: a bump forces every operator to
// disconnect and reconnect every YouTube account, and nothing in this file
// earns that.
//
// The capability is resolved by type assertion in LifecycleFor, so a drifting
// signature would make YouTube silently stop being a lifecycle platform rather
// than fail to build. Same guard as the one above Stats in youtube.go.
var _ BroadcastLifecycler = (*YouTube)(nil)

const (
	// The transition endpoint. broadcastStatus, id and part all travel as QUERY
	// PARAMETERS -- there is no request body at all, and a body carrying
	// {"status": ...} would be sent, accepted as empty, and change nothing while
	// looking exactly like a successful call.
	ytTransitionPath = "/liveBroadcasts/transition"
	// The bound stream's liveness lives on a different resource.
	ytStreamsPath = "/liveStreams"
	// The parts the transition response is asked for. `part` is required;
	// status is what makes the response worth reading, since it carries the
	// lifeCycleStatus the transition produced.
	ytTransitionParts = "id,status"
)

// BroadcastState reads what YouTube says the broadcast is, plus the two things
// that gate a transition to testing.
//
// TWO CALLS, AND THE SECOND ONE IS NOT OPTIONAL PADDING. The precondition that
// actually refuses in practice -- "the status.streamStatus must be active for
// the stream that the broadcast is bound to" -- is a property of a DIFFERENT
// resource, reachable only through contentDetails.boundStreamId. A state read
// that stopped after the broadcast would report a broadcast that looks perfectly
// ready and transitions straight into errorStreamInactive.
//
// The id is required rather than discovered. liveBroadcast() next door picks
// "the one the operator probably means" for a metadata write, and that heuristic
// is right for a title and catastrophic here: the same ranking that would edit
// the wrong description would END the wrong broadcast.
func (y *YouTube) BroadcastState(ctx context.Context, accessToken, broadcastID string) (*BroadcastLifecycleState, error) {
	id := strings.TrimSpace(broadcastID)
	if id == "" {
		// Refused before any call, for the reason RescheduleBroadcast refuses an
		// empty id: a filtered list call with an empty id parameter is not an
		// error, it is a DIFFERENT QUESTION, and its answer would be attributed
		// to a broadcast nobody named.
		return nil, fmt.Errorf("a YouTube broadcast id is required to read lifecycle state")
	}

	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
			Status struct {
				LifeCycleStatus string `json:"lifeCycleStatus"`
			} `json:"status"`
			ContentDetails struct {
				BoundStreamID string `json:"boundStreamId"`
				MonitorStream struct {
					// A POINTER, so an absent key stays absent. See
					// BroadcastLifecycleState for why a precondition read as
					// false when it was never reported is worse here than
					// anywhere else in this package.
					EnableMonitorStream *bool `json:"enableMonitorStream"`
				} `json:"monitorStream"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	err := getJSON(ctx,
		y.apiEndpoint()+ytBroadcastsPath+"?part=id,snippet,status,contentDetails&id="+url.QueryEscape(id),
		accessToken, nil, &list)
	if err != nil {
		return nil, err
	}
	// len first, never items[0] blind. The list reference documents no 404 for a
	// filtered id that matches nothing -- notFound/liveBroadcastNotFound belongs
	// to liveBroadcasts.delete, not to list -- and an empty items array is the
	// structurally natural reading rather than a documented one. Indexing on
	// that reading would panic on the day it is wrong.
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("YouTube returned no broadcast with id %q; it may belong to another "+
			"channel, or have been deleted", id)
	}
	b := list.Items[0]

	state := &BroadcastLifecycleState{
		BroadcastID:   firstNonEmpty(b.ID, id),
		Title:         b.Snippet.Title,
		Status:        b.Status.LifeCycleStatus,
		BoundStreamID: b.ContentDetails.BoundStreamID,
		MonitorStream: b.ContentDetails.MonitorStream.EnableMonitorStream,
	}
	if state.BoundStreamID == "" {
		// Nothing to ask about. StreamActive stays nil, which reads as
		// "unknown", and ReadyForTesting says the broadcast is bound to nothing
		// rather than blaming a stream that does not exist.
		return state, nil
	}

	var streams struct {
		Items []struct {
			Status struct {
				StreamStatus string `json:"streamStatus"`
			} `json:"status"`
		} `json:"items"`
	}
	err = getJSON(ctx,
		y.apiEndpoint()+ytStreamsPath+"?part=status&id="+url.QueryEscape(state.BoundStreamID),
		accessToken, nil, &streams)
	if err != nil || len(streams.Items) == 0 {
		// THE BROADCAST'S OWN PHASE IS STILL KNOWN AND IS THE ANSWER THAT
		// MATTERS. Losing the second call costs a precondition, and returning an
		// error here would throw away a correct phase read to report the failure
		// of an advisory one -- the trade Stats makes one file over, for the
		// same reason. StreamActive stays nil, so nothing downstream mistakes
		// this for an inactive stream.
		return state, nil
	}
	state.StreamStatus = streams.Items[0].Status.StreamStatus
	// Only "active" satisfies the precondition. The other documented values --
	// created, error, inactive, ready -- are all "not yet", and "ready" is the
	// trap among them: it sounds like the answer and is not.
	active := strings.EqualFold(state.StreamStatus, "active")
	state.StreamActive = &active
	return state, nil
}

// TransitionBroadcast drives the state machine.
//
// THE STATUS IS A QUERY PARAMETER. Written out because the shape is unusual for
// a POST that changes an object: this request has no body whatsoever, and every
// instinct that says "the new status goes in the payload" produces a call
// YouTube accepts and does nothing with.
//
// The transition is NOT retried here. Which refusals are worth another attempt
// is a decision about the show -- errorStreamInactive means wait for the
// encoder, errorExecutingTransition means try again shortly, concurrentBroadcasts-
// ExceedLimit means a human has to stop another broadcast -- and a retry loop
// buried in a provider would make all three look the same to the layer that can
// actually tell them apart.
func (y *YouTube) TransitionBroadcast(ctx context.Context, accessToken, broadcastID string, to BroadcastPhase) (*TransitionResult, error) {
	id := strings.TrimSpace(broadcastID)
	if id == "" {
		return nil, fmt.Errorf("a YouTube broadcast id is required to transition a broadcast")
	}
	if !to.Valid() {
		return nil, fmt.Errorf("%q is not a YouTube broadcast transition; the documented targets are "+
			"%q, %q and %q", to, PhaseTesting, PhaseLive, PhaseComplete)
	}

	q := url.Values{}
	q.Set("broadcastStatus", string(to))
	q.Set("id", id)
	q.Set("part", ytTransitionParts)

	var out struct {
		ID     string `json:"id"`
		Status struct {
			LifeCycleStatus string `json:"lifeCycleStatus"`
		} `json:"status"`
	}
	// nil payload: no body, and requestJSON therefore sends no Content-Type
	// either, which is what a parameter-only POST should look like on the wire.
	err := requestJSON(ctx, http.MethodPost,
		y.apiEndpoint()+ytTransitionPath+"?"+q.Encode(), accessToken, nil, nil, &out)
	if err != nil {
		return classifyTransition(err, id, to)
	}
	return &TransitionResult{
		BroadcastID: firstNonEmpty(out.ID, id),
		Requested:   to,
		Status:      out.Status.LifeCycleStatus,
	}, nil
}

// TransitionRefusal names a refusal YouTube documents BY NAME, so a caller can
// switch on it.
//
// The classification is the point of this file. All four of the refusals below
// arrive as HTTP 403 with the same shape, and they mean, in order: keep waiting,
// re-read and re-plan, this channel is full, and slow down. A caller that saw
// only "403" would have to pick one response for four situations and would get
// three of them wrong.
type TransitionRefusal string

const (
	// RefusalStreamInactive means NO BYTES ARE ARRIVING YET. Verbatim: "The
	// requested transition is not allowed when the stream that is bound to the
	// broadcast is inactive."
	RefusalStreamInactive TransitionRefusal = "streamInactive"
	// RefusalInvalidTransition means the machine will not go from here to there.
	// Verbatim: "The live broadcast can't transition from its current status to
	// the requested status." The fix is to re-read the state and re-plan, never
	// to send the same transition again.
	RefusalInvalidTransition TransitionRefusal = "invalidTransition"
	// RefusalConcurrencyLimit is the ceiling, and IT REFUSES AT TRANSITION
	// RATHER THAN AT CREATE -- a channel can hold more created broadcasts than
	// it can put on air. Verbatim: "The channel already has the maximum number
	// of concurrent live broadcasts."
	//
	// YOUTUBE DOES NOT PUBLISH THE NUMBER, so there is no ceiling constant
	// anywhere in this package and no pre-flight count that could avoid the
	// refusal. Handling the refusal is the entire available strategy; a guessed
	// limit would refuse broadcasts YouTube would have accepted.
	RefusalConcurrencyLimit TransitionRefusal = "concurrencyLimit"
	// RefusalRateLimited is userRequestsExceedRateLimit: the caller is asking
	// too often. No documented number here either.
	RefusalRateLimited TransitionRefusal = "rateLimited"
	// RefusalTransient is backendError/errorExecutingTransition, "An error
	// occurred while changing the broadcast's status" -- the one refusal in the
	// set that says nothing about the broadcast and may simply be tried again.
	RefusalTransient TransitionRefusal = "transient"
)

// TransitionRefused is a refusal YouTube named. It wraps the underlying HTTP
// error, so a caller that only logs still gets the platform's own words, and a
// caller that decides gets the classification without parsing prose.
type TransitionRefused struct {
	BroadcastID string
	Requested   BroadcastPhase
	Refusal     TransitionRefusal
	// Reason is YouTube's own reason string, verbatim, e.g.
	// "errorStreamInactive". Kept because the classification above deliberately
	// collapses two ceiling reasons into one and a bug report wants the exact
	// one that fired.
	Reason string
	err    error
}

func (e *TransitionRefused) Error() string {
	return fmt.Sprintf("YouTube refused the transition to %q on broadcast %s (%s): %v",
		e.Requested, e.BroadcastID, e.Reason, e.err)
}

func (e *TransitionRefused) Unwrap() error { return e.err }

// Fault reports whether this refusal means something is WRONG.
//
// errorStreamInactive is the reason this method exists and the reason it is not
// simply "the call failed". A broadcast whose encoder has not started sending is
// in an entirely normal state -- it is the state EVERY broadcast is in before
// the encoder connects -- and a transition attempted then is a question with a
// boring answer, not an incident. Counted as a fault it would page somebody,
// mark a destination unhealthy, or abort a go-live sequence, for the crime of
// asking a second before the bytes turned up.
//
// Every other documented refusal is a fault: something needs a different action
// (re-plan, stop another broadcast, back off) and silence about it would leave
// the operator waiting for a broadcast that is never going live.
func (e *TransitionRefused) Fault() bool {
	return e.Refusal != RefusalStreamInactive
}

// classifyTransition turns YouTube's error body into either a success or a
// classified refusal.
//
// UNRECOGNISED FAILURES PASS THROUGH UNCHANGED, on purpose. A default branch
// that folded every unknown error into some catch-all refusal would invent a
// classification the docs do not support, and a caller switching on it would act
// on that invention. An unclassified error stays the plain *statusError it
// already was, which every existing caller in this package already handles.
func classifyTransition(err error, broadcastID string, to BroadcastPhase) (*TransitionResult, error) {
	var se *statusError
	if !errors.As(err, &se) {
		// A transport failure, a context cancellation, a body that would not
		// decode. Nothing to classify.
		return nil, err
	}
	for _, reason := range googleErrorReasons(se.payload()) {
		switch reason {
		case "redundantTransition":
			// ALREADY THERE, WHICH IS SUCCESS. This is the path that makes a
			// retry safe: a transition whose response was lost, re-sent, lands
			// here rather than looking like a new failure.
			//
			// Status is deliberately left empty rather than filled in with the
			// requested phase. YouTube refused this call, so it reported no
			// lifeCycleStatus, and writing one here would be this file guessing
			// at the very thing the caller can ask for.
			return &TransitionResult{
				BroadcastID: broadcastID,
				Requested:   to,
				Redundant:   true,
			}, nil
		case "errorStreamInactive":
			return nil, refused(se, broadcastID, to, RefusalStreamInactive, reason)
		case "invalidTransition":
			return nil, refused(se, broadcastID, to, RefusalInvalidTransition, reason)
		case "concurrentBroadcastsExceedLimit", "sharedIngestionBroadcastsExceedLimit":
			return nil, refused(se, broadcastID, to, RefusalConcurrencyLimit, reason)
		case "userRequestsExceedRateLimit":
			return nil, refused(se, broadcastID, to, RefusalRateLimited, reason)
		case "errorExecutingTransition":
			return nil, refused(se, broadcastID, to, RefusalTransient, reason)
		}
	}
	return nil, err
}

func refused(err error, broadcastID string, to BroadcastPhase, r TransitionRefusal, reason string) *TransitionRefused {
	return &TransitionRefused{
		BroadcastID: broadcastID,
		Requested:   to,
		Refusal:     r,
		Reason:      reason,
		err:         err,
	}
}

// googleErrorReasons pulls error.errors[].reason out of a Google API error body.
//
// The reason strings are the only machine-readable part of these responses --
// the human message is localised and the HTTP status is 403 for four unrelated
// situations -- so matching on prose would be matching on the one field Google
// is free to reword.
//
// Parsed from payload() and never from Body: Body is truncated to 300 characters
// for display, and a truncated JSON document decodes as nothing at all. That
// exact bug is recorded in metadata.go:196, where every code-specific branch
// silently stopped firing on long error bodies.
//
// An undecodable body yields no reasons rather than an error, and the caller
// treats "no reasons" as "unclassified" -- which returns the original error
// untouched. There is no path here that turns a parse failure into a
// classification.
func googleErrorReasons(body string) []string {
	var out struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &out) != nil {
		return nil
	}
	reasons := make([]string, 0, len(out.Error.Errors))
	for _, e := range out.Error.Errors {
		if e.Reason != "" {
			reasons = append(reasons, e.Reason)
		}
	}
	return reasons
}
