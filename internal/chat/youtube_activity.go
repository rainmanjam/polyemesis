package chat

import (
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

// YouTube activity: the paid and membership message types that arrive in the
// ordinary liveChatMessages.list poll, turned into activity events. No new
// transport, no new scope and no new quota: the poll is already being made.
//
// Names are those of the liveChatMessages resource (checked against
// https://developers.google.com/youtube/v3/live/docs/liveChatMessages):
//
//	superChatEvent             snippet.superChatDetails{amountMicros,currency,amountDisplayString,userComment,tier}
//	superStickerEvent          snippet.superStickerDetails{same, superStickerMetadata{stickerId,altText,language}}
//	newSponsorEvent            snippet.newSponsorDetails{memberLevelName,isUpgrade}
//	memberMilestoneChatEvent   snippet.memberMilestoneChatDetails{memberMonth,memberLevelName,userComment}
//	membershipGiftingEvent     snippet.membershipGiftingDetails{giftMembershipsCount,giftMembershipsLevelName}
//	giftMembershipReceivedEvent snippet.giftMembershipReceivedDetails{memberLevelName,gifterChannelId,associatedMembershipGiftingMessageId}
//
// These are Super Chat and membership features of a monetised channel. The API
// gives no way to ask whether a channel is, so a kind is "unknown" until one
// has been seen, and nothing here treats its absence as a failure.

// ytActivitySeenCap bounds the seen-set. A live chat produces a few thousand
// items an hour at most; the hub dedupes again behind this.
const ytActivitySeenCap = 4096

// ytUint is an unsigned integer the API may send as a JSON number or, as the
// Google JSON mapping does for 64-bit integers, as a decimal string.
type ytUint struct {
	V  uint64
	OK bool
}

func (u *ytUint) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	// Never an error: a malformed number must not make the whole page unreadable
	// and so cost the chat its messages. It is simply not OK.
	if v, err := strconv.ParseUint(s, 10, 64); err == nil {
		u.V, u.OK = v, true
	}
	return nil
}

type ytPaidDetails struct {
	AmountMicros        ytUint `json:"amountMicros"`
	Currency            string `json:"currency"`
	AmountDisplayString string `json:"amountDisplayString"`
	UserComment         string `json:"userComment"`
	Tier                ytUint `json:"tier"`
	StickerMetadata     *struct {
		AltText string `json:"altText"`
	} `json:"superStickerMetadata"`
}

type ytNewSponsor struct {
	MemberLevelName string `json:"memberLevelName"`
	IsUpgrade       bool   `json:"isUpgrade"`
}

type ytMilestone struct {
	MemberMonth     ytUint `json:"memberMonth"`
	MemberLevelName string `json:"memberLevelName"`
	UserComment     string `json:"userComment"`
}

type ytGifting struct {
	GiftMembershipsCount     ytUint `json:"giftMembershipsCount"`
	GiftMembershipsLevelName string `json:"giftMembershipsLevelName"`
}

type ytGiftRecv struct {
	MemberLevelName                      string `json:"memberLevelName"`
	GifterChannelID                      string `json:"gifterChannelId"`
	AssociatedMembershipGiftingMessageID string `json:"associatedMembershipGiftingMessageId"`
}

// ytActivity is the decoder's state. Guarded by YouTubeAdapter.mu.
type ytActivity struct {
	started time.Time
	seen    map[string]struct{}
	order   []string
	kinds   map[activity.Kind]bool
	scope   bool
	count   ActivityCounters
}

// ActivityCounters are the only record kept of what the decoder saw: counts,
// never payloads.
type ActivityCounters struct {
	Received, Emitted, Deduped, Baseline, Unparseable, Ignored int
}

// beginActivity fixes the moment this adapter began watching (pump calls it
// before the first list). Nothing published before it is ever emitted, so a
// restart replays no history. Set once
// per adapter, so a reconnect (a second Run or pump) keeps the same baseline and
// the same seen-set.
func (y *YouTubeAdapter) beginActivity() {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.act.started.IsZero() {
		y.act.started = y.cfg.Now()
		y.act.seen = make(map[string]struct{})
		y.act.kinds = make(map[activity.Kind]bool)
	}
}

// markSeen reports whether the id was new. Caller holds y.mu.
func (a *ytActivity) markSeen(id string) bool {
	if _, dup := a.seen[id]; dup {
		return false
	}
	a.seen[id] = struct{}{}
	a.order = append(a.order, id)
	if len(a.order) > ytActivitySeenCap {
		delete(a.seen, a.order[0])
		a.order = a.order[1:]
	}
	return true
}

// ytActivityTypes are the message types that can be an activity.
func ytActivityType(t string) bool {
	switch t {
	case "superChatEvent", "superStickerEvent", "newSponsorEvent",
		"memberMilestoneChatEvent", "membershipGiftingEvent", "giftMembershipReceivedEvent":
		return true
	}
	return false
}

// activityFrom turns one list item into an activity, when it is one.
func (y *YouTubeAdapter) activityFrom(it ytChatItem, sink Sink) {
	if !ytActivityType(it.Snippet.Type) {
		return
	}
	y.mu.Lock()
	if y.act.seen == nil { // deliverPage called without a page: be safe
		y.mu.Unlock()
		return
	}
	y.act.count.Received++
	if it.ID == "" {
		y.act.count.Unparseable++
		y.mu.Unlock()
		return
	}
	if !y.act.markSeen(it.ID) {
		y.act.count.Deduped++
		y.mu.Unlock()
		return
	}
	at, err := time.Parse(time.RFC3339, it.Snippet.PublishedAt)
	if err != nil || at.Before(y.act.started) {
		// Older than this adapter, or undated and so not provably new: the
		// baseline. Seen, counted, never emitted.
		y.act.count.Baseline++
		y.mu.Unlock()
		return
	}
	y.mu.Unlock()

	ev, ok := ytActivityEvent(it, at)
	y.mu.Lock()
	switch {
	case !ok && it.Snippet.Type == "giftMembershipReceivedEvent":
		// A receipt collapses into its gifting event (design 2.2).
		y.act.count.Ignored++
	case !ok:
		y.act.count.Unparseable++
	default:
		y.act.count.Emitted++
		y.act.kinds[ev.Kind] = true
	}
	y.mu.Unlock()
	if ok {
		emitActivity(sink, ev)
	}
}

// ytMoney converts the API's micros to minor units WITHOUT converting
// currency. A currency whose exponent is known gets it; one that is not is
// carried exactly at exponent 6 (the micros themselves) rather than guessed at.
func ytMoney(micros uint64, currency, display string) (*activity.Amount, bool) {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if len(cur) != 3 {
		return nil, false
	}
	exp, known := activity.ExponentFor(cur)
	if !known {
		exp = activity.MaxExponent
	}
	div := uint64(1)
	for i := 0; i < activity.MaxExponent-exp; i++ {
		div *= 10
	}
	minor := micros / div
	if minor > 1<<62 {
		return nil, false
	}
	return &activity.Amount{Currency: cur, Minor: int64(minor), Exponent: exp, Display: display}, true
}

func ytActor(it ytChatItem) *activity.Actor {
	a := it.AuthorDetails
	name := strings.TrimSpace(a.DisplayName)
	return &activity.Actor{ID: a.ChannelID, Name: name, Anonymous: name == ""}
}

// ytActivityEvent maps one item. false means it is malformed, or a receipt.
func ytActivityEvent(it ytChatItem, at time.Time) (activity.Event, bool) {
	sn := it.Snippet
	ev := activity.Event{At: at, Actor: ytActor(it), Source: activity.Source{Type: sn.Type, ID: it.ID}}
	switch sn.Type {
	case "superChatEvent", "superStickerEvent":
		d := sn.SuperChatDetails
		if sn.Type == "superStickerEvent" {
			d = sn.SuperStickerDetails
		}
		if d == nil || !d.AmountMicros.OK {
			return ev, false
		}
		amt, ok := ytMoney(d.AmountMicros.V, d.Currency, strings.TrimSpace(d.AmountDisplayString))
		if !ok {
			return ev, false
		}
		ev.Kind, ev.Amount, ev.Message = activity.KindTip, amt, d.UserComment
		if d.StickerMetadata != nil && ev.Message == "" {
			ev.Message = d.StickerMetadata.AltText
		}
		if d.Tier.OK {
			ev.Tier = strconv.FormatUint(d.Tier.V, 10)
		}
	case "newSponsorEvent":
		d := sn.NewSponsorDetails
		if d == nil {
			return ev, false
		}
		ev.Kind, ev.Product, ev.Tier = activity.KindSubscribe, "membership", d.MemberLevelName
	case "memberMilestoneChatEvent":
		d := sn.MemberMilestoneChatDetails
		if d == nil || !d.MemberMonth.OK || d.MemberMonth.V < 1 || d.MemberMonth.V > 1_000_000 {
			return ev, false
		}
		ev.Kind, ev.Product, ev.Tier = activity.KindResub, "membership", d.MemberLevelName
		ev.Months, ev.Message = int(d.MemberMonth.V), d.UserComment
	case "membershipGiftingEvent":
		d := sn.MembershipGiftingDetails
		if d == nil || !d.GiftMembershipsCount.OK || d.GiftMembershipsCount.V < 1 || d.GiftMembershipsCount.V > activity.MaxCount {
			return ev, false
		}
		ev.Kind, ev.Product, ev.Tier = activity.KindGift, "membership", d.GiftMembershipsLevelName
		ev.Count = int(d.GiftMembershipsCount.V)
	default:
		return ev, false
	}
	return ev, true
}

// ActivityCounters reports what the decoder has seen.
func (y *YouTubeAdapter) ActivityCounters() ActivityCounters {
	y.mu.Lock()
	defer y.mu.Unlock()
	return y.act.count
}

// ActivityKinds says which kinds this adapter decodes. Every one is "unknown"
// until one has been seen, because the API cannot be asked whether the channel
// is monetised or has memberships; a channel that is not simply never sends them,
// which is not a failure. "reconnect" when the account's token was refused for
// lacking scope, since nothing at all can then be read.
func (y *YouTubeAdapter) ActivityKinds() []activity.KindStatus {
	y.mu.Lock()
	defer y.mu.Unlock()
	out := make([]activity.KindStatus, 0, 4)
	for _, k := range []activity.Kind{activity.KindTip, activity.KindSubscribe, activity.KindResub, activity.KindGift} {
		st := activity.KindStatus{Kind: k, State: activity.KindUnknown,
			Detail: "Needs a monetised channel; shown once the first one is seen."}
		switch {
		case y.act.scope:
			st.State = activity.KindReconnect
			st.Detail = "Reconnect the YouTube account in Settings and approve every permission."
		case y.act.kinds[k]:
			st.State, st.Detail = activity.KindOn, ""
		}
		out = append(out, st)
	}
	return out
}
