package db

import (
	"fmt"
	"sort"
)

// Compliance metadata: the fields that are not a nicety.
//
// Everything in internal/oauth's Metadata so far describes the broadcast --
// title, description, category. These describe the OBLIGATION: who the
// programme is for, who may see it, and what a viewer is about to be shown.
// Getting them wrong is not a cosmetic failure. COPPA is a law, Twitch requires
// labels for several content classes, and going live publicly by accident
// cannot be undone once people have seen it.
//
// They are therefore separate from Metadata rather than folded into it, for
// three reasons: they apply to a different API part on YouTube, an entirely
// different endpoint for made-for-kids, and they must never be sent by
// accident -- see PrivacyStatus below for what "by accident" costs.

// PrivacyStatus is YouTube's broadcast visibility.
//
// The empty value means LEAVE IT ALONE, and that distinction is load-bearing.
// YouTube's liveBroadcasts.update is destructive BY PART, not by field: sending
// `part=status` without a privacyStatus does not leave the existing setting in
// place, it "will remove the existing privacy setting and revert to the
// default". A naive PATCH-shaped implementation can therefore make a private
// broadcast public, and the operator finds out from the audience.
//
// So: a status write happens only when the operator has chosen a value, and
// when it happens it always carries one.
type PrivacyStatus string

const (
	PrivacyUnchanged PrivacyStatus = ""
	PrivacyPublic    PrivacyStatus = "public"
	PrivacyUnlisted  PrivacyStatus = "unlisted"
	PrivacyPrivate   PrivacyStatus = "private"
)

// PrivacyStatuses is every value an operator may pick, in the order to offer
// them: least exposure first, because the safe choice should be the near one.
var PrivacyStatuses = []PrivacyStatus{PrivacyPrivate, PrivacyUnlisted, PrivacyPublic}

// ValidPrivacy reports whether p is a value YouTube accepts.
func ValidPrivacy(p PrivacyStatus) bool {
	if p == PrivacyUnchanged {
		return true
	}
	for _, v := range PrivacyStatuses {
		if v == p {
			return true
		}
	}
	return false
}

// FacebookPrivacy is Facebook's audience for a live video.
//
// Empty means LEAVE IT ALONE, exactly as PrivacyStatus does and for the same
// reason: a privacy write that happens by accident is one the operator finds out
// about from the audience.
//
// Deliberately NOT PrivacyStatus. That type is documented as YouTube's
// visibility and its values are public/unlisted/private. Facebook has no
// unlisted and YouTube has no friends, so sharing the type would need a lossy
// mapping in the one field here where being wrong cannot be taken back -- and a
// translation layer is somewhere for that wrongness to hide.
type FacebookPrivacy string

const (
	FBPrivacyUnchanged        FacebookPrivacy = ""
	FBPrivacySelf             FacebookPrivacy = "SELF"
	FBPrivacyFriends          FacebookPrivacy = "ALL_FRIENDS"
	FBPrivacyFriendsOfFriends FacebookPrivacy = "FRIENDS_OF_FRIENDS"
	FBPrivacyEveryone         FacebookPrivacy = "EVERYONE"
)

// FacebookPrivacies is every value an operator may pick, least exposure first,
// because the safe choice should be the near one.
var FacebookPrivacies = []FacebookPrivacy{
	FBPrivacySelf, FBPrivacyFriends, FBPrivacyFriendsOfFriends, FBPrivacyEveryone,
}

// ValidFacebookPrivacy reports whether p is a value Facebook accepts.
func ValidFacebookPrivacy(p FacebookPrivacy) bool {
	if p == FBPrivacyUnchanged {
		return true
	}
	for _, v := range FacebookPrivacies {
		if v == p {
			return true
		}
	}
	return false
}

// TwitchLabels are the content classification labels Twitch will WRITE.
//
// Twitch requires a label for several content classes, so this is compliance
// rather than decoration.
//
// The read shape and the write shape differ, and that is the trap. A channel
// reads its labels back as a flat list of ids; a write takes
// `[{"id":"Gambling","is_enabled":true}]`. Copying the read shape into a write
// produces a request Twitch rejects, and the operator sees a go-live that
// failed for no visible reason.
//
// MatureGame is deliberately absent: it is READABLE and NOT WRITABLE. Offering
// it would give the operator a control that silently never applies.
var TwitchLabels = []string{
	"DebatedSocialIssuesAndPolitics",
	"DrugsIntoxication",
	"Gambling",
	"ProfanityVulgarity",
	"SexualThemes",
	"ViolentGraphic",
}

// ValidTwitchLabel reports whether id is one Twitch accepts on a write.
func ValidTwitchLabel(id string) bool {
	for _, l := range TwitchLabels {
		if l == id {
			return true
		}
	}
	return false
}

// Compliance is the obligation metadata for one destination.
//
// Every field's zero value means "do not touch this", never "set it to the
// default". A destination that has never been given a compliance setting must
// produce exactly the API calls it produced before this existed.
type Compliance struct {
	// Privacy is YouTube's broadcast visibility. Empty leaves it alone.
	Privacy PrivacyStatus `json:"privacy,omitempty"`
	// MadeForKids is YouTube's COPPA self-declaration. nil leaves it alone;
	// the pointer exists precisely so that "false" is expressible and distinct
	// from "unset", because "this is not for children" is a real declaration
	// and not the absence of one.
	MadeForKids *bool `json:"madeForKids,omitempty"`
	// Labels are Twitch content classification labels, id -> enabled. Absent
	// keys are left alone; a key set to false actively CLEARS that label,
	// which is how an operator removes one.
	Labels map[string]bool `json:"labels,omitempty"`
	// FacebookPrivacy is applied when the Facebook LiveVideo is CREATED, and
	// attempted again best-effort on a metadata push. Empty leaves it alone.
	FacebookPrivacy FacebookPrivacy `json:"facebookPrivacy,omitempty"`
}

// Empty reports whether there is nothing to push.
func (c Compliance) Empty() bool {
	return c.Privacy == PrivacyUnchanged && c.MadeForKids == nil &&
		len(c.Labels) == 0 && c.FacebookPrivacy == FBPrivacyUnchanged
}

// Problems reports what a platform will refuse, so the operator is told at save
// time rather than at go-live.
func (c Compliance) Problems() []string {
	var probs []string
	if !ValidPrivacy(c.Privacy) {
		probs = append(probs, fmt.Sprintf("unknown privacy setting %q (public, unlisted, private)", c.Privacy))
	}
	if !ValidFacebookPrivacy(c.FacebookPrivacy) {
		probs = append(probs, fmt.Sprintf(
			"unknown Facebook privacy %q (SELF, ALL_FRIENDS, FRIENDS_OF_FRIENDS, EVERYONE)",
			c.FacebookPrivacy))
	}
	ids := make([]string, 0, len(c.Labels))
	for id := range c.Labels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if ValidTwitchLabel(id) {
			continue
		}
		// MatureGame is called out by name because it is the one an operator
		// is most likely to try: it appears when they READ their channel, and
		// Twitch simply will not accept it on a write.
		if id == "MatureGame" {
			probs = append(probs, "Twitch will not accept MatureGame on a write: "+
				"it is derived from the category and can be read but never set")
			continue
		}
		probs = append(probs, fmt.Sprintf("unknown Twitch content label %q", id))
	}
	return probs
}
