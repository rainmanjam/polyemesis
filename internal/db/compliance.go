package db

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
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
// Empty means LEAVE IT ALONE, as PrivacyStatus does — but for a WEAKER reason,
// and the difference matters. PrivacyStatus documents a specific mechanism:
// YouTube's update is destructive by PART, so omitting a value actively reverts
// the setting. Nothing of the kind is known about Facebook's update endpoint,
// because the Graph API reference documents no update section for LiveVideo at
// all. What holds here is only the general one: a privacy write nobody asked for
// is one the operator finds out about from the audience.
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
	// FacebookPrivacy is applied when the Facebook LiveVideo is CREATED, for a
	// user or profile target only: a Page broadcast has no personal audience
	// for this to apply to, so IngestFor suppresses it there regardless of what
	// is stored here. It can also be changed on a broadcast already live,
	// through oauth.Facebook.PushCompliance / UpdateLiveVideoPrivacy --
	// Facebook documents no Updating section for LiveVideo at all, so that path
	// only ever reports success once a read-back confirms the value actually
	// changed. Empty leaves it alone.
	FacebookPrivacy FacebookPrivacy `json:"facebookPrivacy,omitempty"`
	// YouTube is the YouTube broadcast's contentDetails controls. A pointer, so
	// a destination that never set one stores and serves exactly what it did
	// before. They live here rather than in a column of their own because this
	// is the per-destination JSON that already carries YouTube's privacy and
	// COPPA declaration and that the metadata push already applies.
	YouTube *YouTubeBroadcastControls `json:"youtube,omitempty"`
	// X is the chat access mode and broadcast language an X go-live carries. A
	// pointer for the same reason YouTube is, and it lives in this JSON for the
	// same one: no column, no schema change, and it goes with the row. It is
	// NOT part of Empty: compliance is what a CompliancePusher sends, and X is
	// not one -- these two values travel on the PUBLISH instead. Only an X
	// destination may hold it; see Destination.xGoLiveProblems.
	X *XBroadcastControls `json:"x,omitempty"`
}

// XChatOptionMax is the largest chat_option POST /2/broadcasts accepts.
const XChatOptionMax = 6

// xLocalePattern is the shape of a locale sent as the go-live "locale": a BCP 47
// style tag such as "en", "pt-BR" or "zh-Hans". X's page states only "Broadcast
// language, e.g. en", so this guards against a pasted sentence rather than
// naming the languages X knows; X's own refusal is shown in X's words.
var xLocalePattern = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8}){0,2}$`)

// XBroadcastControls are the two options an X go-live carries besides its
// title. Every field's zero value means "leave it out", so X applies its own.
type XBroadcastControls struct {
	// ChatOption is who may chat, 0 to 6 as X documents them: 0 default, 1 chat
	// off, 2 everyone, 3 verified accounts, 4 accounts the broadcaster follows,
	// 5 the broadcaster's subscribers, 6 accounts followed by an account the
	// broadcaster follows. nil sends nothing.
	ChatOption *int `json:"chatOption,omitempty"`
	// Locale is the broadcast language tag. Empty sends nothing.
	Locale string `json:"locale,omitempty"`
}

// Empty reports whether there is nothing to send.
func (c *XBroadcastControls) Empty() bool {
	return c == nil || (c.ChatOption == nil && strings.TrimSpace(c.Locale) == "")
}

// Problems reports what X will refuse.
func (c *XBroadcastControls) Problems() []string {
	if c == nil {
		return nil
	}
	var probs []string
	if c.ChatOption != nil && (*c.ChatOption < 0 || *c.ChatOption > XChatOptionMax) {
		probs = append(probs, fmt.Sprintf("unknown X chat option %d: use 0 to %d", *c.ChatOption, XChatOptionMax))
	}
	if loc := strings.TrimSpace(c.Locale); loc != "" && !xLocalePattern.MatchString(loc) {
		probs = append(probs, fmt.Sprintf("unknown X broadcast language %q: use a language tag such as en or pt-BR", c.Locale))
	}
	return probs
}

// YouTube latency and closed-caption values, as liveBroadcasts.contentDetails
// spells them (https://developers.google.com/youtube/v3/live/docs/liveBroadcasts).
const (
	YTLatencyNormal   = "normal"
	YTLatencyLow      = "low"
	YTLatencyUltraLow = "ultraLow"

	YTCaptionsDisabled = "closedCaptionsDisabled"
	YTCaptionsHTTPPost = "closedCaptionsHttpPost"
	YTCaptionsEmbedded = "closedCaptionsEmbedded"
)

// YouTubeBroadcastControls are the liveBroadcasts contentDetails toggles an
// operator can set per destination. Every field's zero value means "do not
// touch it". YouTube freezes them once the broadcast leaves created/ready.
//
// Never here: madeForKids (read-only on YouTube; the COPPA declaration is
// Compliance.MadeForKids, which is selfDeclaredMadeForKids) and the deprecated
// enableClosedCaptions (closedCaptionsType replaces it).
type YouTubeBroadcastControls struct {
	// LatencyPreference is normal, low or ultraLow.
	LatencyPreference string `json:"latencyPreference,omitempty"`
	EnableDvr         *bool  `json:"enableDvr,omitempty"`
	RecordFromStart   *bool  `json:"recordFromStart,omitempty"`
	EnableAutoStart   *bool  `json:"enableAutoStart,omitempty"`
	EnableAutoStop    *bool  `json:"enableAutoStop,omitempty"`
	// ClosedCaptionsType is closedCaptionsDisabled, closedCaptionsHttpPost or
	// closedCaptionsEmbedded.
	ClosedCaptionsType string `json:"closedCaptionsType,omitempty"`
	// StreamID pins the YouTube liveStream a live-now broadcast is bound to. Empty means the stream whose key
	// this destination already publishes with. It is NOT a toggle and is never pushed: it is excluded from Empty.
	StreamID string `json:"streamId,omitempty"`
	// StudioStreamNow turns off the broadcast polyemesis creates at go-live ("Use YouTube Studio's Stream now
	// instead"): the destination then publishes to its key and YouTube's own Stream now makes the broadcast. The
	// zero value is the default, polyemesis's own broadcast. Not a toggle and never pushed: excluded from Empty.
	StudioStreamNow bool `json:"studioStreamNow,omitempty"`
	// EventID is an existing YouTube broadcast ("event") the operator picked to use at go-live instead of a new one:
	// polyemesis binds it to the destination's stream if it is not already, records it, and reads chat from it. It
	// never reuses a finished event: once the event completes the destination falls back to creating broadcasts.
	EventID string `json:"eventId,omitempty"`
	// RebindEvent is the operator's confirmation that a picked event bound to a DIFFERENT, idle stream may be moved
	// to this destination's stream. An event whose bound stream is active is never rebound.
	RebindEvent bool `json:"rebindEvent,omitempty"`
	// Description, Category and Projection360 are applied when polyemesis creates a go-live broadcast, and to a
	// scheduled show it creates (never to a picked event, whose own settings are left as they are). Category is a
	// YouTube category name, applied to the video after the create; Projection360 makes a 360-degree broadcast.
	// Latency, DVR and the made-for-kids declaration use the controls above and Compliance.MadeForKids.
	Description   string `json:"description,omitempty"`
	Category      string `json:"category,omitempty"`
	Projection360 bool   `json:"projection360,omitempty"`
}

// YouTubeDescriptionMax and YouTubeCategoryMax bound the free-text create fields: YouTube's description limit
// is 5000 bytes, and a category name is a short label.
const (
	YouTubeDescriptionMax = 5000
	YouTubeCategoryMax    = 60
)

// OnlyForYouTube reports whether any YouTube-only setting is present.
func (c *YouTubeBroadcastControls) OnlyForYouTube() bool {
	return c != nil && (c.StreamID != "" || c.StudioStreamNow || c.EventID != "" || c.RebindEvent ||
		c.Description != "" || c.Category != "" || c.Projection360)
}

// ytStreamIDPattern is the shape of a liveStream id: URL-safe characters, with dots, dashes and underscores.
var ytStreamIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Empty reports whether there is nothing to push.
func (c *YouTubeBroadcastControls) Empty() bool {
	return c == nil || (c.LatencyPreference == "" && c.ClosedCaptionsType == "" &&
		c.EnableDvr == nil && c.RecordFromStart == nil && c.EnableAutoStart == nil && c.EnableAutoStop == nil)
}

// Problems reports what YouTube will refuse.
func (c *YouTubeBroadcastControls) Problems() []string {
	if c == nil {
		return nil
	}
	var probs []string
	if c.StreamID != "" && !ytStreamIDPattern.MatchString(c.StreamID) {
		probs = append(probs, "the pinned YouTube stream id is not a stream id; pick the stream from the list")
	}
	if c.EventID != "" && !ytStreamIDPattern.MatchString(c.EventID) {
		probs = append(probs, "the YouTube event id is not a broadcast id; pick the event from the list")
	}
	if n := len(c.Description); n > YouTubeDescriptionMax {
		probs = append(probs, fmt.Sprintf("the YouTube broadcast description is %d bytes and YouTube allows %d", n, YouTubeDescriptionMax))
	}
	if n := utf8.RuneCountInString(c.Category); n > YouTubeCategoryMax {
		probs = append(probs, fmt.Sprintf("the YouTube category name is %d characters; use the category's name from YouTube", n))
	}
	switch c.LatencyPreference {
	case "", YTLatencyNormal, YTLatencyLow, YTLatencyUltraLow:
	default:
		probs = append(probs, fmt.Sprintf("unknown YouTube latency %q (normal, low, ultraLow)", c.LatencyPreference))
	}
	switch c.ClosedCaptionsType {
	case "", YTCaptionsDisabled, YTCaptionsHTTPPost, YTCaptionsEmbedded:
	default:
		probs = append(probs, fmt.Sprintf(
			"unknown YouTube closed-caption type %q (closedCaptionsDisabled, closedCaptionsHttpPost, closedCaptionsEmbedded)",
			c.ClosedCaptionsType))
	}
	if c.LatencyPreference == YTLatencyUltraLow &&
		c.ClosedCaptionsType != "" && c.ClosedCaptionsType != YTCaptionsDisabled {
		probs = append(probs, "YouTube ultra-low latency does not support closed captions")
	}
	return probs
}

// Empty reports whether there is nothing to push.
func (c Compliance) Empty() bool {
	return c.Privacy == PrivacyUnchanged && c.MadeForKids == nil &&
		len(c.Labels) == 0 && c.FacebookPrivacy == FBPrivacyUnchanged && c.YouTube.Empty()
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
	probs = append(probs, c.YouTube.Problems()...)
	probs = append(probs, c.X.Problems()...)
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
