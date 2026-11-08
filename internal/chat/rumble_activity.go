package chat

// Rumble viewer activity: follows, paid subscriptions, gifted subscriptions and
// rants, read from the get-data poll the chat adapter already makes. No second
// request, no new credential, no scope.
//
// FIELD NAMES ARE FROM RUMBLE'S OWN ARTICLE ("Rumble's Live Stream API",
// rumble.support, last updated 20 Nov 2025), read 2026-11-07:
//
//	followers.recent_followers[]       {username, followed_on}            RFC 3339, offset -04:00 in the example
//	subscribers.recent_subscribers[]   {username, user, amount_cents,     RFC 3339, +00:00
//	                                    amount_dollars, subscribed_on}
//	gifted_subs.recent_gifted_subs[]   {total_gifts, gift_type,           NO TIMESTAMP at all
//	                                    remaining_gifts, video_id,
//	                                    purchased_by}
//	livestreams[].chat.recent_rants[]  {username, badges, text,           amount_cents is USD cents
//	                                    created_on, expires_on,
//	                                    amount_cents, amount_dollars}
//
// Where the design note (alerts-design.md 3.5) differed from the article, the
// article won: a gifted sub has no created_on, and its remaining_gifts counts
// DOWN as the gifts are claimed, so it cannot be part of an identity.
//
// THERE IS NO DELIVERY ID ANYWHERE in this API. Every identity here is
// synthetic: a hash of the fields that do not change between polls. Two items
// with the same identity are told apart by COUNT: the list is a recent-50
// snapshot, so an identical second purchase shows up as a second copy of the
// same entry, and an event is emitted only when a key appears more often than
// it ever has before.
//
// THE FIRST SUCCESSFUL POLL OF A Run EMITS NOTHING for follows, subscriptions
// and gifts: it seeds the seen-set. Rants seed on the first poll that has a
// live stream (and again for the next broadcast). The state lives in the Run's
// own locals, so a restart of the adapter can never replay what was on the
// list. Zero backfill, unlike chat's five minutes.
//
// Amounts are never converted: a rant and a subscription are USD cents, sent
// as {USD, minor, exponent 2}.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// rumbleActivitySeenCap bounds the seen-set. The lists are at most
// max_num_results (50) each, so this is far above anything that can reappear.
const rumbleActivitySeenCap = 4096

type rumbleFollower struct {
	Username   string `json:"username"`
	FollowedOn string `json:"followed_on"`
}

type rumbleSubscriber struct {
	Username     string `json:"username"`
	User         string `json:"user"`
	AmountCents  int    `json:"amount_cents"`
	SubscribedOn string `json:"subscribed_on"`
}

type rumbleGiftedSub struct {
	TotalGifts  int    `json:"total_gifts"`
	GiftType    string `json:"gift_type"`
	VideoID     any    `json:"video_id"`
	PurchasedBy string `json:"purchased_by"`
}

// rumbleActivityLists is what a poll carries for activity. A nil list pointer
// means the key was absent (the account's tier or channel does not report it),
// which is different from an empty list (nothing yet).
//
// The lists arrive as raw JSON and each ELEMENT is decoded on its own. They sit
// in the same response chat decodes, so a type surprise inside an activity
// block (a string where a number was) must cost that one entry, never the
// whole decode and with it the chat.
type rumbleActivityLists struct {
	followers, subscribers, gifted json.RawMessage
}

// decodeRumbleList decodes a raw list element by element. present is false
// when the key was absent, null or not an array; bad counts elements skipped.
func decodeRumbleList[T any](raw json.RawMessage) (items []T, present bool, bad int) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false, 0
	}
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil {
		return nil, false, 1
	}
	for _, e := range elems {
		var it T
		if json.Unmarshal(e, &it) != nil {
			bad++
			continue
		}
		items = append(items, it)
	}
	return items, true, bad
}

// rumbleActivityState is one Run's memory. Not shared and not persisted.
type rumbleActivityState struct {
	seeded      map[string]bool // which lists have a baseline; per list, so one that appears later is baselined too
	rantsSeeded bool            // the current broadcast's rants have a baseline
	seen        map[string]int
	order       []string
}

func newRumbleActivityState() *rumbleActivityState {
	return &rumbleActivityState{seen: map[string]int{}, seeded: map[string]bool{}}
}

// resetRants is called when no broadcast is live: the next one's rants are
// baselined again, so a second stream never replays its first poll.
func (s *rumbleActivityState) resetRants() { s.rantsSeeded = false }

// fresh reports whether the n-th copy of key is new, and records it.
func (s *rumbleActivityState) fresh(key string, n int) bool {
	prev, known := s.seen[key]
	if n <= prev {
		return false
	}
	if !known {
		s.order = append(s.order, key)
		if len(s.order) > rumbleActivitySeenCap {
			delete(s.seen, s.order[0])
			s.order = s.order[1:]
		}
	}
	s.seen[key] = n
	return true
}

func rumbleActivityID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16])
}

func (r *RumbleAdapter) activityTime(raw string) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		return t
	}
	return time.Time{} // Normalise stamps receipt time
}

// rumbleCents is USD cents as an amount. Never converted.
func rumbleCents(cents int) *activity.Amount {
	return &activity.Amount{Currency: "USD", Minor: int64(cents), Exponent: 2}
}

// observe turns one successful poll into events. live is nil when nothing is
// on air. It returns the events to send; the caller sends them.
func (r *RumbleAdapter) observeActivity(st *rumbleActivityState, l rumbleActivityLists, live *rumbleLivestream) []activity.Event {
	var out []activity.Event
	var baseline bool
	counts := map[string]int{}
	// take counts a copy of an identity within THIS poll. It returns the
	// event's Source.ID and whether it is new. A second identical entry gets a
	// distinct id (":2"): the hub dedupes on Source.ID, so without it the
	// second purchase would be dropped there although it is a real event.
	take := func(kind, id string) (string, bool) {
		key := kind + "\x00" + id
		counts[key]++
		n := counts[key]
		fresh := st.fresh(key, n) && !baseline
		if n > 1 {
			id += ":" + itoa(n)
		}
		return id, fresh
	}
	note := func(bad int) { r.activityIgnored.Add(int64(bad)) }
	followers, fOK, fBad := decodeRumbleList[rumbleFollower](l.followers)
	subscribers, sOK, sBad := decodeRumbleList[rumbleSubscriber](l.subscribers)
	gifted, gOK, gBad := decodeRumbleList[rumbleGiftedSub](l.gifted)
	note(fBad + sBad + gBad)
	emit := func(ev activity.Event, ok bool) {
		if ok {
			ev.Platform = string(db.PlatformRumble)
			ev.Account = r.cfg.AccountRef
			out = append(out, ev)
		}
	}

	if fOK {
		baseline = !st.seeded["follow"]
		st.seeded["follow"] = true
		for _, f := range followers {
			name := strings.TrimSpace(f.Username)
			if name == "" {
				r.activityIgnored.Add(1)
				continue
			}
			id := rumbleActivityID("follow", name, f.FollowedOn)
			sid, fresh := take("follow", id)
			emit(activity.Event{
				Kind: activity.KindFollow, At: r.activityTime(f.FollowedOn),
				Actor:  &activity.Actor{ID: name, Name: name},
				Source: activity.Source{Type: "recent_followers", ID: sid},
			}, fresh)
		}
	}
	if sOK {
		baseline = !st.seeded["subscribe"]
		st.seeded["subscribe"] = true
		for _, s := range subscribers {
			name := strings.TrimSpace(firstNonEmpty(s.Username, s.User))
			if name == "" || s.AmountCents < 0 {
				r.activityIgnored.Add(1)
				continue
			}
			id := rumbleActivityID("subscribe", name, s.SubscribedOn, itoa(s.AmountCents))
			sid, fresh := take("subscribe", id)
			ev := activity.Event{
				Kind: activity.KindSubscribe, At: r.activityTime(s.SubscribedOn),
				Actor: &activity.Actor{ID: name, Name: name}, Product: "paid_sub",
				Source: activity.Source{Type: "recent_subscribers", ID: sid},
			}
			if s.AmountCents > 0 {
				ev.Amount = rumbleCents(s.AmountCents)
			}
			emit(ev, fresh)
		}
	}
	if gOK {
		baseline = !st.seeded["gift"]
		st.seeded["gift"] = true
		for _, g := range gifted {
			name := strings.TrimSpace(g.PurchasedBy)
			if name == "" || g.TotalGifts < 1 {
				r.activityIgnored.Add(1)
				continue
			}
			// remaining_gifts is left out of the identity on purpose: it falls
			// as the gifts are claimed, which would re-emit the same purchase.
			id := rumbleActivityID("gift", name, g.GiftType, anyString(g.VideoID), itoa(g.TotalGifts))
			sid, fresh := take("gift", id)
			emit(activity.Event{
				Kind: activity.KindGift, Actor: &activity.Actor{ID: name, Name: name},
				Count: g.TotalGifts, Product: "paid_sub",
				Source: activity.Source{Type: "recent_gifted_subs", ID: sid},
			}, fresh)
		}
	}
	if live != nil {
		baseline = !st.rantsSeeded
		for _, e := range live.Chat.RecentRants {
			name := strings.TrimSpace(e.Username)
			if e.AmountCents == 0 {
				continue // an ordinary message in the rant list
			}
			if name == "" || e.AmountCents < 0 {
				r.activityIgnored.Add(1)
				continue
			}
			id := rumbleActivityID("tip", name, e.CreatedOn, itoa(e.AmountCents), e.Text)
			sid, fresh := take("tip", id)
			emit(activity.Event{
				Kind: activity.KindTip, At: r.activityTime(e.CreatedOn),
				Actor: &activity.Actor{ID: name, Name: name}, Amount: rumbleCents(e.AmountCents),
				Message: e.Text, // viewer text: gated by the overlay, never bypassed
				Source:  activity.Source{Type: "rant", ID: sid},
			}, fresh)
		}
		st.rantsSeeded = true
	}
	r.noteActivityBlocks(fOK, sOK, gOK, live != nil)
	return out
}

// rumbleActivitySeen is the per-adapter record of which blocks the last
// successful poll carried, for ActivityKinds.
type rumbleActivitySeen struct {
	polled, followers, subscribers, gifted, rants bool
}

func (r *RumbleAdapter) noteActivityBlocks(f, sub, g, live bool) {
	r.mu.Lock()
	b := r.activityBlocks
	b.polled = true
	b.followers, b.subscribers, b.gifted = f, sub, g
	if live {
		b.rants = true
	}
	r.activityBlocks = b
	r.mu.Unlock()
}

// ActivityKinds reports which activity kinds this account's Rumble responses
// carry. A block the response does not contain is unavailable (not a failure:
// chat is unaffected); before the first poll everything is unknown, and rants
// stay unknown until a broadcast has been seen.
func (r *RumbleAdapter) ActivityKinds() []activity.KindStatus {
	r.mu.Lock()
	b := r.activityBlocks
	r.mu.Unlock()
	block := func(k activity.Kind, present bool, what string) activity.KindStatus {
		switch {
		case !b.polled:
			return activity.KindStatus{Kind: k, State: activity.KindUnknown, Detail: "Not seen yet: waiting for the first Rumble poll"}
		case present:
			return activity.KindStatus{Kind: k, State: activity.KindOn}
		default:
			return activity.KindStatus{Kind: k, State: activity.KindUnavailable,
				Detail: "Rumble's response has no " + what + " for this account, so there is nothing to report"}
		}
	}
	tip := activity.KindStatus{Kind: activity.KindTip, State: activity.KindOn}
	if !b.rants {
		tip = activity.KindStatus{Kind: activity.KindTip, State: activity.KindUnknown,
			Detail: "Not seen yet: rants are read while a Rumble broadcast is live"}
	}
	return []activity.KindStatus{
		block(activity.KindFollow, b.followers, "follower list"),
		block(activity.KindSubscribe, b.subscribers, "subscriber list"),
		block(activity.KindGift, b.gifted, "gifted-subscription list"),
		tip,
	}
}

// activityIgnoredCount is how many entries were skipped as unusable (no
// username, a negative amount). Counters only: no payload is retained.
func (r *RumbleAdapter) activityIgnoredCount() int64 { return r.activityIgnored.Load() }

func itoa(n int) string { return strconv.Itoa(n) }

// anyString renders video_id, which the article shows as a number but which is
// an identity, not arithmetic, so either spelling must hash the same.
func anyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}
