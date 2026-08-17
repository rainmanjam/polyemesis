package db

// Broadcast lifecycle bookkeeping: what the coordinator in internal/api knows
// about the ONE broadcast a destination is currently driving.
//
// READ THIS IF YOU ARE DEBUGGING A BROADCAST THAT DID NOT START. Everything in
// here is a RECORD OF WHAT THE PLATFORM SAID, never a belief about what
// polyemesis asked for. Phase is the platform's own word, copied verbatim from
// the last state read or transition response; Fault is the sentence an operator
// is shown. If Phase says "testing" and the watch page says "starting soon",
// the coordinator agrees with you -- look at Fault and Attempts next, because
// between them they say whether it is still trying and why the last try failed.
//
// WHY THIS IS NOT AnnouncementSet (internal/db/announcement.go), which was
// extracted for exactly this kind of reuse. That type holds a SET -- one marker
// per show, keyed by schedule, because one destination is reached by every
// schedule that names it and two schedules are two shows. Nothing here is
// plural: a destination has one platform, one connected account and, at any
// instant, one broadcast that is either on air or not. Reusing the set would
// have meant inventing a key ("which schedule is this live broadcast for?")
// that has no answer at the moment it matters -- an operator pressing Go Live
// by hand belongs to no schedule at all.
//
// AND IT IS PLATFORM-NEUTRAL WHERE AnnouncementSet IS EXPLICITLY NOT. The set
// says "one set per platform" because a Facebook live_video id is addressable
// only with the Facebook token that created it, and pooling ids would strip the
// fact that says which token acts on which id. That argument does not reach
// here, because this block is not a pool: the id it holds belongs to the row's
// own Platform and is acted on with the row's own AccountID. There is nothing
// to confuse it with.

// BroadcastControl is the lifecycle coordinator's per-destination state.
//
// IT IS DELIBERATELY OUTSIDE THE ENGINE'S RESTART HASH, and that is the single
// property that makes it safe to write to a LIVE destination -- which
// internal/api/preannounce.go may not do. Preannounce refuses an enabled row
// because it writes StreamKey, and StreamKey is inside Target(), which is the
// first element of destSpec: writing it under a running FFmpeg cycles the
// process at a moment nobody chose. No field below reaches destSpecFor, so no
// field below reaches an argv, so no write of this block can restart anything.
// internal/engine/lifecycle_spec_test.go pins that, by mutating every field
// here and requiring both spec hashes to come back byte-identical.
type BroadcastControl struct {
	// BroadcastID is WHICH broadcast the rest of this block describes.
	//
	// It is copied from the announcement marker (see AnnouncementSet, mirrored
	// into FacebookSettings.BroadcastID for every platform that can
	// pre-announce, YouTube included) the first time the coordinator adopts a
	// destination, and then held here so the two cannot drift: an announcement
	// marker is rewritten by the pre-announce sweep whenever a schedule moves,
	// and a phase recorded against the id that marker USED to carry would be
	// attributed to a broadcast that is not the one on air.
	//
	// Empty means the coordinator has nothing to act on. That is not an error
	// state -- it is every destination on every platform without a broadcast
	// object, and most installs are entirely made of them.
	BroadcastID string `json:"broadcastId,omitempty"`
	// Phase is the PLATFORM'S OWN WORD for where the broadcast is, verbatim and
	// unmapped -- for YouTube: created, ready, testing, testStarting, live,
	// liveStarting, complete, revoked.
	//
	// Passed through rather than translated for the reason
	// oauth.BroadcastLifecycleState.Status states: an operator holding YouTube
	// Studio open in another tab must be able to compare the two strings. A
	// local vocabulary would make the one screen that could confirm a diagnosis
	// the one screen that disagrees with us.
	//
	// NEVER GUESSED. It is only ever written from a state read or from a
	// transition response that carried a lifeCycleStatus. A phase set to what
	// was REQUESTED would be wrong in precisely the case this exists for --
	// the request that was refused, or whose response was lost.
	Phase string `json:"phase,omitempty"`
	// Attempts is how many CONSECUTIVE sweeps have faulted on this broadcast.
	//
	// COUNTED, NEVER TIMED, and the reasoning is announceOne's verbatim
	// (preannounce.go:252-260): a wall clock would have to be persisted, a
	// persisted timestamp is absent on every row an upgrade finds, absent reads
	// as infinitely old, and an install would age every record at once on the
	// first sweep after an upgrade. A counter absent on an upgraded row reads as
	// zero, which is "nothing has failed yet" -- the safe direction, and the
	// true one.
	//
	// It is also the whole of the retry policy. There is no second backoff:
	// the sweep tick IS the interval, so a retry cannot arrive faster than the
	// platform is being asked anyway.
	Attempts int `json:"attempts,omitempty"`
	// Fault is what went wrong, in the operator's words, or empty when nothing
	// has. It renders on the destination card.
	//
	// A STRING RATHER THAN A CODE because every consumer is a human. The
	// classification that a program acts on lives in oauth.TransitionRefusal and
	// is consumed at the point of refusal; what survives into the row is the
	// sentence that says what to DO -- "stop another broadcast on this channel"
	// and "this is polyemesis's shared-stream ceiling, not yours" are the same
	// HTTP status and must never render as the same sentence.
	Fault string `json:"fault,omitempty"`
}

// Empty reports whether this block says anything at all.
//
// Used to decide whether a destination is the coordinator's business, and to
// keep an upgraded row -- which decodes to the zero value -- from being treated
// as one it has an opinion about.
func (b BroadcastControl) Empty() bool {
	return b.BroadcastID == "" && b.Phase == "" && b.Attempts == 0 && b.Fault == ""
}
