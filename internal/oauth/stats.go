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
	Live        bool      `json:"live"`
	ViewerCount int       `json:"viewerCount"`
	Title       string    `json:"title,omitempty"`
	Category    string    `json:"category,omitempty"`
	Language    string    `json:"language,omitempty"`
	Slug        string    `json:"slug,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
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
