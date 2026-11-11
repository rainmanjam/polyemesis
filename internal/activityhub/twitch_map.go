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
	m := twitchMapping{
		typ:    typ,
		raw:    raw,
		at:     at,
		selfID: selfID,
		ev:     activity.Event{Platform: "twitch", Source: activity.Source{Type: typ}},
	}
	if oc := m.handle(); oc != outcomeEmit {
		return mapped{}, oc
	}
	return mapped{ev: m.ev, pollID: m.pollID}, outcomeEmit
}

// twitchMapping carries one notification through its per-type handler. The
// handler fills ev (and pollID for the poll types) and reports an outcome.
type twitchMapping struct {
	typ    string
	raw    json.RawMessage
	at     time.Time
	selfID string
	ev     activity.Event
	pollID string
}

// handle routes the notification to the handler for its type. An unknown type
// is understood but deliberately not an event.
func (m *twitchMapping) handle() outcome {
	switch m.typ {
	case "channel.follow":
		return m.follow()
	case "channel.subscribe":
		return m.subscribe()
	case "channel.subscription.message":
		return m.resub()
	case "channel.subscription.gift":
		return m.gift()
	case "channel.cheer":
		return m.cheer()
	case "channel.raid":
		return m.raid()
	case "channel.channel_points_custom_reward_redemption.add":
		return m.redemption()
	case "channel.hype_train.begin", "channel.hype_train.progress", "channel.hype_train.end":
		return m.hypeTrain()
	case "channel.poll.begin", "channel.poll.progress", "channel.poll.end":
		return m.poll()
	case "channel.prediction.begin", "channel.prediction.progress", "channel.prediction.lock", "channel.prediction.end":
		return m.prediction()
	case "stream.online":
		return m.streamOnline()
	case "stream.offline":
		m.ev.Kind, m.ev.Phase, m.ev.At = activity.KindStream, "offline", m.at
		return outcomeEmit
	case "channel.update":
		return m.channelUpdate()
	case "channel.shoutout.create":
		return m.shoutoutCreate()
	case "channel.shoutout.receive":
		return m.shoutoutReceive()
	default:
		return outcomeIgnored
	}
}

// when is the event's own timestamp, or the message timestamp when it carries
// none.
func (m *twitchMapping) when(own string) time.Time {
	if t := eventsub.ParseTime(own); !t.IsZero() {
		return t
	}
	return m.at
}

// decode unmarshals the raw payload into v and reports whether it succeeded.
func (m *twitchMapping) decode(v any) bool {
	return json.Unmarshal(m.raw, v) == nil
}

func (m *twitchMapping) follow() outcome {
	var e eventsub.FollowEvent
	if !m.decode(&e) || e.ID == "" {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Actor, ev.At = activity.KindFollow, user(e.User), m.when(e.FollowedAt)
	return outcomeEmit
}

func (m *twitchMapping) subscribe() outcome {
	var e eventsub.SubscribeEvent
	if !m.decode(&e) || e.ID == "" {
		return outcomeBad
	}
	if e.IsGift {
		// The gift arrives as one channel.subscription.gift (total=N) plus
		// N of these; the gift is the event, its receipts are not.
		return outcomeIgnored
	}
	ev := &m.ev
	ev.Kind, ev.Actor, ev.Tier, ev.Product, ev.At = activity.KindSubscribe, user(e.User), e.Tier, "sub", m.at
	return outcomeEmit
}

func (m *twitchMapping) resub() outcome {
	var e eventsub.SubscriptionMessageEvent
	if !m.decode(&e) || e.ID == "" || e.CumulativeMonths < 1 {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Actor, ev.Tier, ev.Product, ev.At = activity.KindResub, user(e.User), e.Tier, "sub", m.at
	ev.Months, ev.Message = e.CumulativeMonths, e.Message.Text
	if e.StreakMonths != nil {
		ev.Streak = *e.StreakMonths
	}
	return outcomeEmit
}

func (m *twitchMapping) gift() outcome {
	var e eventsub.SubscriptionGiftEvent
	if !m.decode(&e) || e.Total < 1 || (!e.IsAnonymous && e.ID == "") {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Tier, ev.Product, ev.Count, ev.At = activity.KindGift, e.Tier, "sub", e.Total, m.at
	ev.Actor = giftActor(e.User, e.IsAnonymous)
	return outcomeEmit
}

func (m *twitchMapping) cheer() outcome {
	var e eventsub.CheerEvent
	if !m.decode(&e) || e.Bits < 1 || (!e.IsAnonymous && e.ID == "") {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.At, ev.Message = activity.KindTip, m.at, e.Message
	ev.Actor = giftActor(e.User, e.IsAnonymous)
	// Bits are a platform unit and are never converted (design 1).
	ev.Amount = &activity.Amount{Unit: "bits", Units: e.Bits}
	return outcomeEmit
}

func (m *twitchMapping) raid() outcome {
	var e eventsub.RaidEvent
	if !m.decode(&e) {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.At, ev.Viewers = activity.KindRaid, m.at, e.Viewers
	switch {
	case e.ToID != "" && e.ToID == m.selfID:
		ev.Direction = "in"
		ev.Other = &activity.Actor{ID: e.FromID, Login: e.FromLogin, Name: e.FromName}
	case e.FromID != "" && e.FromID == m.selfID:
		ev.Direction = "out"
		ev.Other = &activity.Actor{ID: e.ToID, Login: e.ToLogin, Name: e.ToName}
	default:
		return outcomeIgnored
	}
	return outcomeEmit
}

func (m *twitchMapping) redemption() outcome {
	var e eventsub.RedemptionEvent
	if !m.decode(&e) || e.User.ID == "" || e.Reward.Title == "" {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Actor, ev.At = activity.KindRedemption, user(e.User), m.when(e.RedeemedAt)
	ev.Title, ev.Message, ev.Status = e.Reward.Title, e.UserInput, e.Status
	return outcomeEmit
}

func (m *twitchMapping) hypeTrain() outcome {
	var e eventsub.HypeTrainEvent
	if !m.decode(&e) || e.ID == "" {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Phase, ev.Level, ev.At = activity.KindHypeTrain, phaseOf(m.typ), e.Level, m.at
	p := &activity.Progress{Total: e.Progress, Goal: e.Goal}
	if ends := eventsub.ParseTime(e.ExpiresAt); !ends.IsZero() && ev.Phase != "end" {
		p.EndsAt = &ends
	}
	ev.Progress = p
	return outcomeEmit
}

func (m *twitchMapping) poll() outcome {
	var e eventsub.PollEvent
	if !m.decode(&e) || e.ID == "" || strings.TrimSpace(e.Title) == "" {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Phase, ev.Title, ev.At = activity.KindPoll, phaseOf(m.typ), e.Title, m.at
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
	m.pollID = e.ID
	return outcomeEmit
}

func (m *twitchMapping) prediction() outcome {
	var e eventsub.PredictionEvent
	if !m.decode(&e) || e.ID == "" || strings.TrimSpace(e.Title) == "" {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Phase, ev.Title, ev.At = activity.KindPrediction, phaseOf(m.typ), e.Title, m.at
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
	return outcomeEmit
}

func (m *twitchMapping) streamOnline() outcome {
	var e eventsub.StreamOnlineEvent
	if !m.decode(&e) || e.ID == "" {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Phase, ev.At, ev.Status = activity.KindStream, "online", m.when(e.StartedAt), e.Type
	return outcomeEmit
}

func (m *twitchMapping) channelUpdate() outcome {
	var e eventsub.ChannelUpdateEvent
	if !m.decode(&e) {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Phase, ev.Title, ev.At = activity.KindStream, "update", e.Title, m.at
	return outcomeEmit
}

func (m *twitchMapping) shoutoutCreate() outcome {
	var e eventsub.ShoutoutCreateEvent
	if !m.decode(&e) || e.ToID == "" {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Direction, ev.Viewers, ev.At = activity.KindShoutout, "out", e.ViewerCount, m.when(e.StartedAt)
	ev.Other = &activity.Actor{ID: e.ToID, Login: e.ToLogin, Name: e.ToName}
	return outcomeEmit
}

func (m *twitchMapping) shoutoutReceive() outcome {
	var e eventsub.ShoutoutReceiveEvent
	if !m.decode(&e) || e.FromID == "" {
		return outcomeBad
	}
	ev := &m.ev
	ev.Kind, ev.Direction, ev.Viewers, ev.At = activity.KindShoutout, "in", e.ViewerCount, m.when(e.StartedAt)
	ev.Other = &activity.Actor{ID: e.FromID, Login: e.FromLogin, Name: e.FromName}
	return outcomeEmit
}

// giftActor is the gifter or the cheerer: a named user, or anonymous, in which
// case Twitch sends null for every user field.
func giftActor(u eventsub.User, anonymous bool) *activity.Actor {
	if anonymous {
		return &activity.Actor{Anonymous: true}
	}
	return &activity.Actor{ID: u.ID, Login: u.Login, Name: u.Name}
}

// user is a named Twitch user as an activity actor.
func user(u eventsub.User) *activity.Actor {
	return &activity.Actor{ID: u.ID, Login: u.Login, Name: u.Name}
}

// phaseOf is the last dot-separated word of a begin/progress/lock/end type.
func phaseOf(typ string) string {
	return typ[strings.LastIndex(typ, ".")+1:]
}
