package activityhub

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/twitch/eventsub"
)

// outcome is what became of one notification at the mapper.
type outcome int

const (
	outcomeEmit    outcome = iota // an event to emit
	outcomeIgnored                // understood, deliberately not an event
	outcomeBad                    // malformed, or missing what the kind needs
)

// mapped is a notification turned into an activity event. pollID is set for
// the poll types, so the source can skip polls polyemesis mirrored.
type mapped struct {
	ev     activity.Event
	pollID string
}

// mapTwitch turns one notification into an activity event. It never keeps or
// logs the raw payload. selfID is the connected channel's user id, which tells
// a raid in from a raid out. at is the message timestamp, the fallback for an
// event that carries none of its own.
func mapTwitch(typ string, raw json.RawMessage, at time.Time, selfID string) (mapped, outcome) {
	ev := activity.Event{Platform: "twitch", Source: activity.Source{Type: typ}}
	when := func(own string) time.Time {
		if t := eventsub.ParseTime(own); !t.IsZero() {
			return t
		}
		return at
	}
	decode := func(v any) bool { return json.Unmarshal(raw, v) == nil }
	user := func(u eventsub.User) *activity.Actor {
		return &activity.Actor{ID: u.ID, Login: u.Login, Name: u.Name}
	}

	switch typ {
	case "channel.follow":
		var e eventsub.FollowEvent
		if !decode(&e) || e.ID == "" {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Actor, ev.At = activity.KindFollow, user(e.User), when(e.FollowedAt)

	case "channel.subscribe":
		var e eventsub.SubscribeEvent
		if !decode(&e) || e.ID == "" {
			return mapped{}, outcomeBad
		}
		if e.IsGift {
			// The gift arrives as one channel.subscription.gift (total=N) plus
			// N of these; the gift is the event, its receipts are not.
			return mapped{}, outcomeIgnored
		}
		ev.Kind, ev.Actor, ev.Tier, ev.Product, ev.At = activity.KindSubscribe, user(e.User), e.Tier, "sub", at

	case "channel.subscription.message":
		var e eventsub.SubscriptionMessageEvent
		if !decode(&e) || e.ID == "" || e.CumulativeMonths < 1 {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Actor, ev.Tier, ev.Product, ev.At = activity.KindResub, user(e.User), e.Tier, "sub", at
		ev.Months, ev.Message = e.CumulativeMonths, e.Message.Text
		if e.StreakMonths != nil {
			ev.Streak = *e.StreakMonths
		}

	case "channel.subscription.gift":
		var e eventsub.SubscriptionGiftEvent
		if !decode(&e) || e.Total < 1 || (!e.IsAnonymous && e.ID == "") {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Tier, ev.Product, ev.Count, ev.At = activity.KindGift, e.Tier, "sub", e.Total, at
		ev.Actor = giftActor(e.User, e.IsAnonymous)

	case "channel.cheer":
		var e eventsub.CheerEvent
		if !decode(&e) || e.Bits < 1 || (!e.IsAnonymous && e.ID == "") {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.At, ev.Message = activity.KindTip, at, e.Message
		ev.Actor = giftActor(e.User, e.IsAnonymous)
		// Bits are a platform unit and are never converted (design 1).
		ev.Amount = &activity.Amount{Unit: "bits", Units: e.Bits}

	case "channel.raid":
		var e eventsub.RaidEvent
		if !decode(&e) {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.At, ev.Viewers = activity.KindRaid, at, e.Viewers
		switch {
		case e.ToID != "" && e.ToID == selfID:
			ev.Direction = "in"
			ev.Other = &activity.Actor{ID: e.FromID, Login: e.FromLogin, Name: e.FromName}
		case e.FromID != "" && e.FromID == selfID:
			ev.Direction = "out"
			ev.Other = &activity.Actor{ID: e.ToID, Login: e.ToLogin, Name: e.ToName}
		default:
			return mapped{}, outcomeIgnored
		}

	case "channel.channel_points_custom_reward_redemption.add":
		var e eventsub.RedemptionEvent
		if !decode(&e) || e.User.ID == "" || e.Reward.Title == "" {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Actor, ev.At = activity.KindRedemption, user(e.User), when(e.RedeemedAt)
		ev.Title, ev.Message, ev.Status = e.Reward.Title, e.UserInput, e.Status

	case "channel.hype_train.begin", "channel.hype_train.progress", "channel.hype_train.end":
		var e eventsub.HypeTrainEvent
		if !decode(&e) || e.ID == "" {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Phase, ev.Level, ev.At = activity.KindHypeTrain, phaseOf(typ), e.Level, at
		p := &activity.Progress{Total: e.Progress, Goal: e.Goal}
		if ends := eventsub.ParseTime(e.ExpiresAt); !ends.IsZero() && ev.Phase != "end" {
			p.EndsAt = &ends
		}
		ev.Progress = p

	case "channel.poll.begin", "channel.poll.progress", "channel.poll.end":
		var e eventsub.PollEvent
		if !decode(&e) || e.ID == "" || strings.TrimSpace(e.Title) == "" {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Phase, ev.Title, ev.At = activity.KindPoll, phaseOf(typ), e.Title, at
		p := &activity.Progress{}
		for _, c := range e.Choices {
			p.Options = append(p.Options, activity.ProgressOption{Label: c.Title, Votes: c.Votes})
			p.Total += c.Votes
		}
		if ends := eventsub.ParseTime(e.EndsAt); !ends.IsZero() {
			p.EndsAt = &ends
		}
		ev.Progress = p
		if ev.Phase == "end" {
			ev.Status = e.Status
		}
		return mapped{ev: ev, pollID: e.ID}, outcomeEmit

	case "channel.prediction.begin", "channel.prediction.progress", "channel.prediction.lock", "channel.prediction.end":
		var e eventsub.PredictionEvent
		if !decode(&e) || e.ID == "" || strings.TrimSpace(e.Title) == "" {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Phase, ev.Title, ev.At = activity.KindPrediction, phaseOf(typ), e.Title, at
		p := &activity.Progress{}
		for _, o := range e.Outcomes {
			p.Options = append(p.Options, activity.ProgressOption{Label: o.Title, Votes: o.Users, Points: o.ChannelPoints})
			p.Total += o.ChannelPoints
		}
		if ends := eventsub.ParseTime(e.LocksAt); !ends.IsZero() && ev.Phase != "end" && ev.Phase != "lock" {
			p.EndsAt = &ends
		}
		ev.Progress = p
		if ev.Phase == "end" {
			ev.Status = e.Status
		}

	case "stream.online":
		var e eventsub.StreamOnlineEvent
		if !decode(&e) || e.ID == "" {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Phase, ev.At, ev.Status = activity.KindStream, "online", when(e.StartedAt), e.Type

	case "stream.offline":
		ev.Kind, ev.Phase, ev.At = activity.KindStream, "offline", at

	case "channel.update":
		var e eventsub.ChannelUpdateEvent
		if !decode(&e) {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Phase, ev.Title, ev.At = activity.KindStream, "update", e.Title, at

	case "channel.shoutout.create":
		var e eventsub.ShoutoutCreateEvent
		if !decode(&e) || e.ToID == "" {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Direction, ev.Viewers, ev.At = activity.KindShoutout, "out", e.ViewerCount, when(e.StartedAt)
		ev.Other = &activity.Actor{ID: e.ToID, Login: e.ToLogin, Name: e.ToName}

	case "channel.shoutout.receive":
		var e eventsub.ShoutoutReceiveEvent
		if !decode(&e) || e.FromID == "" {
			return mapped{}, outcomeBad
		}
		ev.Kind, ev.Direction, ev.Viewers, ev.At = activity.KindShoutout, "in", e.ViewerCount, when(e.StartedAt)
		ev.Other = &activity.Actor{ID: e.FromID, Login: e.FromLogin, Name: e.FromName}

	default:
		return mapped{}, outcomeIgnored
	}
	return mapped{ev: ev}, outcomeEmit
}

// giftActor is the gifter or the cheerer: a named user, or anonymous, in which
// case Twitch sends null for every user field.
func giftActor(u eventsub.User, anonymous bool) *activity.Actor {
	if anonymous {
		return &activity.Actor{Anonymous: true}
	}
	return &activity.Actor{ID: u.ID, Login: u.Login, Name: u.Name}
}

// phaseOf is the last dot-separated word of a begin/progress/lock/end type.
func phaseOf(typ string) string {
	return typ[strings.LastIndex(typ, ".")+1:]
}
