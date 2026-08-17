package oauth

import (
	"context"
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// Commanding a broadcast's lifecycle: the optional capability for a platform
// whose broadcast is an OBJECT WITH A STATE MACHINE rather than a side effect of
// bytes arriving.
//
// Exactly one platform qualifies today, and that is the whole reason this is an
// optional capability instead of a method on Provider. Twitch and Kick have no
// transition call at all: a Twitch or Kick stream starts when RTMP starts and
// ends when it stops, so there is nothing to command and a stub method returning
// "unsupported" would be a lie with a signature. ScheduledBroadcaster records
// the same reasoning at oauth.go:298 -- the value of the interface is that
// ABSENT is a supported answer, handled once by the caller.
//
// NOTHING IN THIS BUILD CALLS IT YET, DELIBERATELY. A transition is the one
// platform write that can stop a broadcast that is already going out, so
// deciding WHEN to send one is its own piece of work with its own review. This
// round delivers the capability and the classification of its refusals.

// BroadcastPhase is a transition TARGET. YouTube's transition call takes exactly
// three, and they are spelled the way the API spells them because the operator
// comparing this against YouTube Studio must see the same word.
//
// A string type rather than free text because the target is a query parameter
// on a state machine: an unrecognised value would be a request YouTube refuses
// for a reason ("invalidTransition") that reads like a state problem rather than
// a typo. Transitions that are not documented -- back to created, or to ready --
// are absent because they are not documented, not because they were forgotten.
type BroadcastPhase string

const (
	// PhaseTesting sends the feed to the monitor stream only. It has TWO
	// documented preconditions, both quoted on the transition reference and both
	// carried on BroadcastLifecycleState below.
	PhaseTesting BroadcastPhase = "testing"
	// PhaseLive puts the broadcast in front of the audience.
	PhaseLive BroadcastPhase = "live"
	// PhaseComplete ends it. There is no documented way back.
	PhaseComplete BroadcastPhase = "complete"
)

// Valid reports whether p is one of the three documented targets. Checked
// before any HTTP call, because a request built from an unchecked string spends
// a quota unit to be told what a switch statement already knew.
func (p BroadcastPhase) Valid() bool {
	switch p {
	case PhaseTesting, PhaseLive, PhaseComplete:
		return true
	default:
		return false
	}
}

// BroadcastLifecycleState is what the platform says the broadcast is RIGHT NOW.
//
// IDEMPOTENCY COMES FROM ASKING, NOT FROM REMEMBERING. The failure this exists
// for is "the request succeeded and the response was lost": a local record of
// what polyemesis believes it sent is wrong precisely in that case, and a
// broadcast id is directly queryable, so the platform's own answer is available
// for the cost of one read. No caller should keep a shadow copy of a state
// machine the platform is authoritative for.
type BroadcastLifecycleState struct {
	BroadcastID string `json:"broadcastId"`
	Title       string `json:"title,omitempty"`
	// Status is the platform's own word for the phase -- for YouTube: created,
	// ready, testing, testStarting, live, liveStarting, complete, revoked.
	// Passed through unmapped for the reason BroadcastWindow.LifeCycleStatus is
	// (youtube_broadcast.go:93): an operator holding YouTube Studio open in
	// another tab must be able to compare the two strings.
	Status string `json:"status"`
	// BoundStreamID is the ingest this broadcast listens to. It is the join
	// between the broadcast and the stream whose liveness gates the transition
	// to testing, and without it the precondition below cannot be read at all.
	BoundStreamID string `json:"boundStreamId,omitempty"`
	// StreamStatus is the bound stream's liveness verbatim -- active, created,
	// error, inactive, ready. NOT the health: those are two different fields on
	// the same resource and conflating them is a documented bug. Only
	// streamStatus governs the transition; healthStatus is advisory and its
	// ordering is counter-intuitive ("good" is BETTER than "ok"), which is why
	// this type carries the one that decides and not the one that advises.
	StreamStatus string `json:"streamStatus,omitempty"`
	// StreamActive AND MonitorStream ARE POINTERS BECAUSE "NO" AND "WE WERE NOT
	// TOLD" ARE DIFFERENT ANSWERS, the rule LiveStats.ViewerCount spells out at
	// length in stats.go. Here the cost of collapsing them is sharper than a
	// wrong number on a dashboard: both fields are PRECONDITIONS. A false read
	// as "the stream is not active" would have a caller sit and wait for bytes
	// that have been arriving for ten minutes, on a state read that simply
	// failed or omitted the key.
	//
	// A nil therefore means "unknown", and the readiness check below refuses to
	// call a broadcast ready on one.
	StreamActive  *bool `json:"streamActive,omitempty"`
	MonitorStream *bool `json:"monitorStream,omitempty"`
}

// ReadyForTesting reports whether both documented preconditions for the
// transition to testing are known to hold, and says which one does not when the
// answer is no.
//
// Both come from the same required-parameter cell on the transition reference,
// verbatim: the "contentDetails.monitorStream.enableMonitorStream property is
// set to true", and "the status.streamStatus must be active for the stream that
// the broadcast is bound to."
//
// It is ADVISORY AND THE API REMAINS THE AUTHORITY, exactly as BroadcastWindow
// is: a stream can go inactive between this read and the transition, so the
// refusal is still handled. What this buys is a caller that can wait for bytes
// instead of spending a transition to be told none are arriving.
//
// Unknown is not ready. A nil precondition returns false with a reason that says
// it was not read, rather than being treated as either answer.
func (s *BroadcastLifecycleState) ReadyForTesting() (bool, string) {
	switch {
	case s.MonitorStream == nil:
		return false, "YouTube did not report whether this broadcast's monitor stream is enabled, " +
			"and the transition to testing requires it"
	case !*s.MonitorStream:
		return false, "this broadcast has its monitor stream disabled, and YouTube requires " +
			"contentDetails.monitorStream.enableMonitorStream to be true before a broadcast can go to testing"
	case s.BoundStreamID == "":
		return false, "this broadcast is not bound to a stream, so there is no ingest whose liveness " +
			"could satisfy the transition to testing"
	case s.StreamActive == nil:
		return false, "YouTube did not report the bound stream's status, so whether bytes are arriving is unknown"
	case !*s.StreamActive:
		return false, fmt.Sprintf("the bound stream is %q rather than \"active\": YouTube will refuse the "+
			"transition until the encoder is actually sending", s.StreamStatus)
	}
	return true, ""
}

// TransitionResult is what a transition DID. It is small on purpose: the
// platform is the authority on the resulting state, and a caller that needs the
// current phase reads it with BroadcastState rather than trusting a field
// echoed back from a write.
type TransitionResult struct {
	BroadcastID string         `json:"broadcastId"`
	Requested   BroadcastPhase `json:"requested"`
	// Status is the lifeCycleStatus the transition response carried, when it
	// carried one. Empty is normal on the redundant path below.
	Status string `json:"status,omitempty"`
	// Redundant means the broadcast was ALREADY in the requested phase and
	// YouTube refused the call with redundantTransition.
	//
	// THIS IS A SUCCESS AND MUST BE REPORTED AS ONE. It is the exact shape of
	// "the request landed and the response was lost, so it was sent again", and
	// a retry that reports failure because the first attempt worked is worse
	// than no retry at all -- it invites a caller to escalate, or to send a
	// different transition, on a broadcast that is doing what was asked.
	Redundant bool `json:"redundant,omitempty"`
}

// BroadcastLifecycler is the optional capability for a platform whose broadcast
// state can be COMMANDED.
//
// A platform opts in by having the methods; there is no registration list. Same
// shape as LiveStatter and MetadataPusher -- see stats.go:78 for the pattern and
// endpoints.go:153 for why the Set twin is not optional.
type BroadcastLifecycler interface {
	Provider
	// BroadcastState reads the current phase and the transition preconditions.
	// The broadcast id is required: a lifecycle read against "whichever
	// broadcast we guess you meant" is how the wrong show gets ended.
	BroadcastState(ctx context.Context, accessToken, broadcastID string) (*BroadcastLifecycleState, error)
	// TransitionBroadcast asks the platform to move the broadcast to a phase.
	//
	// A refusal that the platform documents by name arrives as a
	// *TransitionRefused, so a caller can tell "no bytes are arriving yet" from
	// "you cannot get there from here" from "this channel is at its concurrent
	// broadcast ceiling" -- three refusals with the same HTTP status and three
	// different correct responses. Anything else arrives as the underlying
	// error, unclassified rather than guessed at.
	TransitionBroadcast(ctx context.Context, accessToken, broadcastID string, to BroadcastPhase) (*TransitionResult, error)
}

// LifecycleFor reports whether a platform's broadcast lifecycle can be
// commanded.
//
// The bool is the whole point and callers must branch on it rather than on a
// platform name, for the reason StatsFor states: "this platform has no such
// thing" and "there is no provider for this platform" are both false here, and
// no caller does anything different about them.
func LifecycleFor(p db.Platform) (BroadcastLifecycler, bool) {
	prov, err := Get(p)
	if err != nil {
		return nil, false
	}
	bl, ok := prov.(BroadcastLifecycler)
	return bl, ok
}
