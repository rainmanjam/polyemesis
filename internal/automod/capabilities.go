package automod

import (
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/platform"
)

// Capabilities answers what a platform can actually be made to do.
//
// This is the gate the whole matrix hangs off. A switch offering an action a
// platform cannot perform is a promise that fails SILENTLY: the operator ticks
// "auto-delete on Kick", nothing ever happens, and they believe that channel is
// protected. An unavailable cell must therefore be inert and explained, never
// an unticked box that looks like a choice.
type Capabilities interface { // NOSONAR godre:S8196: the name is the answer to "what can this platform do", which the matrix and operator UI refer to; the method is Can, which has no -er noun, and "Checker" would not say what is checked
	// Can reports whether the platform supports the action, and why not when it
	// does not. The reason is shown to the operator, so it is written for them
	// rather than for a log.
	Can(p db.Platform, a Action) (bool, string)
}

// PlatformCaps is the built-in capability table.
//
// It mirrors what internal/chat's adapters actually implement. The pairing is
// asserted by a guard test rather than trusted: two tables describing
// the same platforms is exactly the drift this repo already writes guards for
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
		// Upstream, reversible hide. Facebook is the only platform with an API
		// for it; everywhere else the honest answer is the local hide above, and
		// pretending otherwise would be the silent-failure case.
		if moderationOf(p).Hide {
			return true, ""
		}
		return false, "only Facebook can hide a message upstream; use the local hide instead"

	case ActionDelete:
		if !moderationOf(p).Delete {
			return false, noModeration(p, "delete a message")
		}
		return true, ""

	case ActionTimeout, ActionBan:
		// Both route through Hub.Ban, which type-asserts chat.Banner, so the
		// registry's TimeoutBan must agree with the adapters. The pairing is
		// asserted by TestRegistryAgreesWithTheChatAdapters in internal/chat.
		if !moderationOf(p).TimeoutBan {
			return false, noModeration(p, "time out or ban a person")
		}
		// Facebook blocks a person from the Page and has no duration to give
		// the block, so a timeout is refused where a ban is not.
		if a == ActionTimeout && moderationOf(p).NoTimeout {
			return false, "Facebook cannot time anyone out: blocking a person from a Page has no duration. " +
				"Use a ban, or delete or hide the comment instead"
		}
		return true, ""
	}
	return false, "unknown action"
}

// moderationOf is the registry's moderation profile for p. A platform the
// registry knows and gives no profile (Rumble: chat is read, nothing can be
// acted on) can do nothing upstream. A name with no row at all keeps the old
// answer -- able to delete, timeout and ban, unable to hide.
func moderationOf(p db.Platform) platform.Moderation {
	row, ok := platform.ByEnum(string(p))
	if !ok {
		return platform.Moderation{Delete: true, TimeoutBan: true}
	}
	if row.Moderation == nil {
		return platform.Moderation{}
	}
	return *row.Moderation
}

// noModeration is the operator-facing reason for an action the platform has no
// way to perform. The local hide is always there instead.
func noModeration(p db.Platform, what string) string {
	name := string(p)
	if row, ok := platform.ByEnum(string(p)); ok {
		name = row.Name
	}
	return name + " has no chat API polyemesis can use to " + what + "; use the local hide instead"
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
