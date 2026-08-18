package automod

import "github.com/rainmanjam/polyemesis/internal/db"

// Capabilities answers what a platform can actually be made to do.
//
// This is the gate the whole matrix hangs off. A switch offering an action a
// platform cannot perform is a promise that fails SILENTLY: the operator ticks
// "auto-delete on Kick", nothing ever happens, and they believe that channel is
// protected. An unavailable cell must therefore be inert and explained, never
// an unticked box that looks like a choice.
type Capabilities interface {
	// Can reports whether the platform supports the action, and why not when it
	// does not. The reason is shown to the operator, so it is written for them
	// rather than for a log.
	Can(p db.Platform, a Action) (bool, string)
}

// PlatformCaps is the built-in capability table.
//
// It mirrors what internal/chat's adapters actually implement. The pairing is
// asserted by a guard test rather than trusted: two tables describing the same
// four platforms is exactly the drift this repo already writes guards for
// elsewhere, and here the failure mode is an operator believing a channel is
// moderated when nothing is wired to it.
//
// THAT GUARD DID NOT EXIST WHEN THIS COMMENT WAS WRITTEN, and the tables had
// already drifted: ban and timeout were claimed for Facebook, which implements
// neither. It exists now --
// internal/chat.TestTheCapabilityTableMatchesWhatTheAdaptersImplement -- and it
// fails the build in both directions, so a capability cannot be claimed without
// an adapter or withheld when one is present.
type PlatformCaps struct{}

// Can implements Capabilities.
func (PlatformCaps) Can(p db.Platform, a Action) (bool, string) {
	switch a {
	case ActionFlag, ActionHideLocal:
		// Both are entirely local to polyemesis: flagging records a note and
		// hiding locally affects only this operator's view. Neither touches a
		// platform API, so neither can be unsupported by one.
		return true, ""

	case ActionHide:
		// Upstream, reversible hide. Facebook is the only one of the four with
		// an API for it; everywhere else the honest answer is the local hide
		// above, and pretending otherwise would be the silent-failure case.
		if p == db.PlatformFacebook {
			return true, ""
		}
		return false, "only Facebook can hide a message upstream; use the local hide instead"

	case ActionDelete:
		return true, ""

	case ActionTimeout, ActionBan:
		// FACEBOOK HAS NO CHAT BAN API, and claiming otherwise was the exact
		// failure the doc comment above says this gate exists to prevent: the
		// operator ticked the box, the Summary counted it as armed, and a rule
		// firing produced one error line in a log nobody reads. Both actions
		// route through Hub.Ban, which type-asserts chat.Banner -- and
		// FacebookAdapter implements Delete, Hide, Run and Health, not Ban.
		//
		// The pairing is now actually asserted, by
		// TestTheCapabilityTableMatchesWhatTheAdaptersImplement in
		// internal/chat, which is the guard this comment claimed to have and
		// did not.
		if p == db.PlatformFacebook {
			return false, "Facebook has no chat ban API. Delete or hide the comment " +
				"instead, or ban the person from the Facebook page itself"
		}
		return true, ""
	}
	return false, "unknown action"
}

// KnownActions reports whether an action is one this package understands, so a
// stored matrix carrying a key from a newer version is ignored rather than
// misread as something it is not.
func KnownActions(a Action) bool {
	for _, x := range Actions {
		if x == a {
			return true
		}
	}
	return false
}

// KnownChecker is the same for checkers.
func KnownChecker(c Checker) bool {
	for _, x := range Checkers {
		if x == c {
			return true
		}
	}
	return false
}

// KnownPlatform is the same for platforms.
func KnownPlatform(p db.Platform) bool {
	for _, x := range Platforms {
		if x == p {
			return true
		}
	}
	return false
}
