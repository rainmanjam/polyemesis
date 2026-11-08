package eventsub

import (
	"strings"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

// Spec is one EventSub subscription type polyemesis asks for, with what Twitch
// requires of the account for it. The table is Catalogue; it was checked
// against the subscription-types reference.
type Spec struct {
	Kind    activity.Kind
	Type    string
	Version string
	// Scopes: holding ANY ONE of these allows the type. Empty means none is
	// needed. The reference accepts either the read or the manage scope for
	// polls, predictions, redemptions and shoutouts, so a token that holds the
	// manage scope (polyemesis already asks for channel:manage:polls and
	// moderator:manage:shoutouts) needs no extra scope for those.
	Scopes []string
	// NeedsTier marks a type Twitch sends only to Affiliates and Partners.
	NeedsTier bool
	// Direction is "in" or "out" for the types that are one half of a pair.
	Direction string
	cond      func(id string) map[string]string
}

// Condition is the subscription's condition for a channel whose owner's token
// makes the subscriptions. Follows and shoutouts name the broadcaster as their
// own moderator, which is what the reference asks when the authorisation is
// the broadcaster's rather than a moderator's.
func (s Spec) Condition(broadcasterID string) map[string]string { return s.cond(broadcasterID) }

// Holds reports whether the granted scopes allow the type.
func (s Spec) Holds(granted []string) bool {
	if len(s.Scopes) == 0 {
		return true
	}
	for _, want := range s.Scopes {
		for _, g := range granted {
			if g == want {
				return true
			}
		}
	}
	return false
}

// ScopeList splits a stored scope string (space or comma separated).
func ScopeList(s string) []string {
	return strings.Fields(strings.ReplaceAll(s, ",", " "))
}

func broadcaster(id string) map[string]string {
	return map[string]string{"broadcaster_user_id": id}
}

func broadcasterAndModerator(id string) map[string]string {
	return map[string]string{"broadcaster_user_id": id, "moderator_user_id": id}
}

// Catalogue lists every subscription type, in the order they are created.
func Catalogue() []Spec {
	return []Spec{
		{Kind: activity.KindFollow, Type: "channel.follow", Version: "2",
			Scopes: []string{"moderator:read:followers"}, cond: broadcasterAndModerator},

		{Kind: activity.KindSubscribe, Type: "channel.subscribe", Version: "1",
			Scopes: []string{"channel:read:subscriptions"}, NeedsTier: true, cond: broadcaster},
		{Kind: activity.KindResub, Type: "channel.subscription.message", Version: "1",
			Scopes: []string{"channel:read:subscriptions"}, NeedsTier: true, cond: broadcaster},
		{Kind: activity.KindGift, Type: "channel.subscription.gift", Version: "1",
			Scopes: []string{"channel:read:subscriptions"}, NeedsTier: true, cond: broadcaster},

		{Kind: activity.KindTip, Type: "channel.cheer", Version: "1",
			Scopes: []string{"bits:read"}, NeedsTier: true, cond: broadcaster},

		{Kind: activity.KindRaid, Type: "channel.raid", Version: "1", Direction: "in",
			cond: func(id string) map[string]string { return map[string]string{"to_broadcaster_user_id": id} }},
		{Kind: activity.KindRaid, Type: "channel.raid", Version: "1", Direction: "out",
			cond: func(id string) map[string]string { return map[string]string{"from_broadcaster_user_id": id} }},

		{Kind: activity.KindRedemption, Type: "channel.channel_points_custom_reward_redemption.add", Version: "1",
			Scopes: []string{"channel:read:redemptions", "channel:manage:redemptions"}, NeedsTier: true, cond: broadcaster},

		// Whether a hype train is Affiliate/Partner-only is not in the API
		// reference, so NeedsTier is NOT set: the create answer decides.
		{Kind: activity.KindHypeTrain, Type: "channel.hype_train.begin", Version: "2",
			Scopes: []string{"channel:read:hype_train"}, cond: broadcaster},
		{Kind: activity.KindHypeTrain, Type: "channel.hype_train.progress", Version: "2",
			Scopes: []string{"channel:read:hype_train"}, cond: broadcaster},
		{Kind: activity.KindHypeTrain, Type: "channel.hype_train.end", Version: "2",
			Scopes: []string{"channel:read:hype_train"}, cond: broadcaster},

		{Kind: activity.KindPoll, Type: "channel.poll.begin", Version: "1",
			Scopes: []string{"channel:read:polls", "channel:manage:polls"}, NeedsTier: true, cond: broadcaster},
		{Kind: activity.KindPoll, Type: "channel.poll.progress", Version: "1",
			Scopes: []string{"channel:read:polls", "channel:manage:polls"}, NeedsTier: true, cond: broadcaster},
		{Kind: activity.KindPoll, Type: "channel.poll.end", Version: "1",
			Scopes: []string{"channel:read:polls", "channel:manage:polls"}, NeedsTier: true, cond: broadcaster},

		{Kind: activity.KindPrediction, Type: "channel.prediction.begin", Version: "1",
			Scopes: []string{"channel:read:predictions", "channel:manage:predictions"}, NeedsTier: true, cond: broadcaster},
		{Kind: activity.KindPrediction, Type: "channel.prediction.progress", Version: "1",
			Scopes: []string{"channel:read:predictions", "channel:manage:predictions"}, NeedsTier: true, cond: broadcaster},
		{Kind: activity.KindPrediction, Type: "channel.prediction.lock", Version: "1",
			Scopes: []string{"channel:read:predictions", "channel:manage:predictions"}, NeedsTier: true, cond: broadcaster},
		{Kind: activity.KindPrediction, Type: "channel.prediction.end", Version: "1",
			Scopes: []string{"channel:read:predictions", "channel:manage:predictions"}, NeedsTier: true, cond: broadcaster},

		{Kind: activity.KindStream, Type: "stream.online", Version: "1", cond: broadcaster},
		{Kind: activity.KindStream, Type: "stream.offline", Version: "1", cond: broadcaster},
		{Kind: activity.KindStream, Type: "channel.update", Version: "2", cond: broadcaster},

		{Kind: activity.KindShoutout, Type: "channel.shoutout.create", Version: "1", Direction: "out",
			Scopes: []string{"moderator:read:shoutouts", "moderator:manage:shoutouts"}, cond: broadcasterAndModerator},
		{Kind: activity.KindShoutout, Type: "channel.shoutout.receive", Version: "1", Direction: "in",
			Scopes: []string{"moderator:read:shoutouts", "moderator:manage:shoutouts"}, cond: broadcasterAndModerator},
	}
}

// Kinds lists the activity kinds the catalogue covers, in display order.
func Kinds() []activity.Kind {
	var out []activity.Kind
	seen := map[activity.Kind]bool{}
	for _, s := range Catalogue() {
		if !seen[s.Kind] {
			seen[s.Kind] = true
			out = append(out, s.Kind)
		}
	}
	return out
}

// Broadcaster types, from GET /helix/users: broadcaster_type.
const (
	TierNone      = ""
	TierAffiliate = "affiliate"
	TierPartner   = "partner"
)
