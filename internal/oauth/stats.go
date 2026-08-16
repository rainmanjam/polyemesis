package oauth

import (
	"context"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// LiveStats is a point-in-time read of a connected channel's broadcast.
//
// Offline is a normal answer, not an error: a channel that is not live has a
// viewer count of zero and nothing has gone wrong. Every consumer has to hold
// that distinction, because "0 viewers" and "we could not ask" look identical
// in a number and mean opposite things to an operator.
//
// THIS TYPE WAS CALLED KickStats AND LIVED NOWHERE IN PARTICULAR. Kick was the
// only platform that could answer, so the type took its name and the interface
// below was declared in internal/api, next to the handler that wanted it. Both
// were reasonable when there was one implementor and neither survives a second:
// a cross-platform capability named after one platform reads as Kick-specific
// to anyone deciding whether to implement it, and an interface outside this
// package cannot have the Set twin that endpoints.go requires of every
// capability. The shape needed no change -- it was already neutral -- so this
// is a rename and a move, not a redesign.
type LiveStats struct {
	Live bool `json:"live"`
	// ViewerCount IS A POINTER BECAUSE "NOBODY IS WATCHING" AND "WE WERE NOT
	// TOLD" ARE DIFFERENT ANSWERS, AND EVERY PLATFORM HAS A WAY OF GIVING THE
	// SECOND ONE. It was an int, which can only say zero, and all three
	// documented cases below would have been reported as an audience of none:
	//
	//   YouTube omits liveStreamingDetails.concurrentViewers entirely when
	//   there are no current viewers, when the owner has HIDDEN the count, and
	//   after the broadcast ends -- three states, one absent key.
	//   Kick, verbatim: "Viewer count will be 0 if the streamer has opted not
	//   to share their viewer count."
	//   Twitch answers an empty data array for a channel that is not live,
	//   which carries no count rather than a count of zero.
	//
	// A streamer who has hidden their viewer count is the case that turns this
	// from pedantry into a bug report: polyemesis would show them 0 viewers,
	// on a stream with an audience, with no indication the number was never
	// sent. docs/DESIGN-SYSTEM.md's rule for the UI is the same one -- a false
	// zero on a live stream is worse than a blank -- and a UI cannot render
	// "not reported" from a type that cannot express it.
	//
	// omitempty drops the key when it is nil, so the wire says the same thing
	// the platform said: nothing. Consumers must branch on presence.
	ViewerCount *int   `json:"viewerCount,omitempty"`
	Title       string `json:"title,omitempty"`
	Category    string `json:"category,omitempty"`
	Language    string `json:"language,omitempty"`
	Slug        string `json:"slug,omitempty"`
	// StartedAt IS A POINTER BECAUSE omitempty DOES NOTHING TO A time.Time.
	// encoding/json only honours omitempty for empty scalars, maps and slices;
	// a struct is never empty to it, so this field used to serialise the zero
	// time as "startedAt":"0001-01-01T00:00:00Z" on every offline channel and
	// every timestamp we failed to parse. A consumer correctly branching on the
	// absence of viewerCount got a confidently wrong start time in the same
	// payload, which is the failure this type spent ViewerCount's comment
	// preventing one field earlier.
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// Source names the endpoint the numbers came from, so a viewer count that
	// disagrees with the platform's own dashboard can be traced without a
	// packet capture. It matters more with several platforms than it did with
	// one: "which of the two endpoints answered" is the first question when two
	// numbers disagree.
	Source string `json:"source,omitempty"`
}

// LiveStatter is the optional capability for a platform that will say how many
// people are watching.
//
// A platform opts in by having the method; there is no registration list. Same
// shape as MetadataPusher and CompliancePusher -- see metadata.go:157 for the
// pattern and endpoints.go:153 for why the Set twin below is not optional.
type LiveStatter interface {
	Provider
	Stats(ctx context.Context, clientID, accessToken string) (*LiveStats, error)
}

// StatsFor reports whether a platform can be asked for viewer numbers.
//
// The bool is the whole point and callers must branch on it rather than on a
// platform name: internal/api answers 200 with supported:false when it is
// false, because "we cannot ask" and "the account is gone" are different
// problems with different fixes.
func StatsFor(p db.Platform) (LiveStatter, bool) {
	prov, err := Get(p)
	if err != nil {
		return nil, false
	}
	s, ok := prov.(LiveStatter)
	return s, ok
}
