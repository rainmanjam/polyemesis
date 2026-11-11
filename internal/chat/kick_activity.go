package chat

// Kick activity: follows, subscriptions, gifted KICKs, stream state, bans and
// reward redemptions, over the SAME webhook and signature machinery as chat.
//
// Nothing here is a new route or a new verifier. Kick signs every delivery with
// one key and posts every subscribed event to one callback URL, so the handler
// in kick.go has already authenticated a body by the time it reaches this file.
// This file does three things: it asks Kick to send the extra events
// (syncActivity), decodes each one into an activity.Event (kickActivityFrom) and
// says which kinds are flowing (ActivityKinds).
//
// Event names, versions, payloads and the one scope all come from
// docs.kick.com (events/event-types, events/subscribe-to-events,
// getting-started/scopes), checked 2026-11-07: every event below is version 1,
// and `events:subscribe` ("Subscribe to all channel events") is the only scope
// the subscribe call names. No other scope is documented for any of these
// events (channel:rewards:read and kicks:read describe REST reads, not
// deliveries), so the Kick ScopeVersion is unchanged.
//
// THE RULES THIS FILE KEEPS
//   - Chat is never put at risk by activity. Chat keeps its own subscription
//     request and its own health; an activity event Kick refuses makes that
//     KIND unavailable (in Kick's words), never a chat problem.
//   - Dedupe on Kick-Event-Message-Id (a bounded set here, then the hub's key).
//   - No history: a webhook only delivers what happens next, nothing is fetched
//     at start, and a delivery whose own timestamp is old is acknowledged and
//     dropped rather than announced.
//   - Amounts are never converted: a gifted KICK is {unit:"kicks"}.
//   - Viewer-written text (a gift message, a redemption's input) goes into
//     Event.Message and nowhere else; the overlay gate is the hub's, not ours.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

// Kick's activity event names, all version 1.
const (
	kickEvFollowed     = "channel.followed"
	kickEvSubNew       = "channel.subscription.new"
	kickEvSubRenewal   = "channel.subscription.renewal"
	kickEvSubGifts     = "channel.subscription.gifts"
	kickEvKicksGifted  = "kicks.gifted"
	kickEvStatus       = "livestream.status.updated"
	kickEvMetadata     = "livestream.metadata.updated"
	kickEvBanned       = "moderation.banned"
	kickEvRedemption   = "channel.reward.redemption.updated"
	kickActivityMaxAge = 15 * time.Minute
	// kickActivitySeen bounds the delivery-id set. The signature check already
	// refuses anything older than five minutes, so this only has to span that
	// window at the rate of a busy channel.
	kickActivitySeen = 4096
)

// kickActivityDef is one event polyemesis subscribes to.
type kickActivityDef struct {
	name string
	kind activity.Kind
}

// kickActivityEvents is the desired set, in the order they are requested.
var kickActivityEvents = []kickActivityDef{
	{kickEvFollowed, activity.KindFollow},
	{kickEvSubNew, activity.KindSubscribe},
	{kickEvSubRenewal, activity.KindResub},
	{kickEvSubGifts, activity.KindGift},
	{kickEvKicksGifted, activity.KindTip},
	{kickEvStatus, activity.KindStream},
	{kickEvMetadata, activity.KindStream},
	{kickEvBanned, activity.KindModeration},
	{kickEvRedemption, activity.KindRedemption},
}

// kickActivityKindOrder is the order ActivityKinds lists them in.
var kickActivityKindOrder = []activity.Kind{
	activity.KindFollow, activity.KindSubscribe, activity.KindResub, activity.KindGift,
	activity.KindTip, activity.KindRedemption, activity.KindStream, activity.KindModeration,
}

func kickActivityEventFor(eventType string) (kickActivityDef, bool) {
	t := strings.ToLower(strings.TrimSpace(eventType))
	for _, d := range kickActivityEvents {
		if d.name == t {
			return d, true
		}
	}
	return kickActivityDef{}, false
}

// kickEventState is what is known about one subscription.
type kickEventState struct {
	state  activity.KindState
	detail string
	at     time.Time
}

// kickActivityState is the adapter's activity bookkeeping, guarded by k.mu.
type kickActivityState struct {
	ev     map[string]kickEventState
	posted map[string]time.Time
	// retry is set when a check failed in a way worth trying again soon (a
	// timeout, a 5xx), as opposed to Kick declining an event.
	retry bool

	seen map[string]struct{}
	ring []string
	pos  int

	delivered, deduped, malformed, ignored int64
}

// ------------------------------------------------------------ subscribing

// syncActivity asks Kick for the activity events that are not already listed.
// Never returns a chat problem: its outcomes are per-event states.
func (k *KickAdapter) syncActivity(ctx context.Context, endpoint string, have map[string]string, listed int) {
	now := k.cfg.Now()
	var want []string
	k.mu.Lock()
	if k.act.ev == nil {
		k.act.ev = map[string]kickEventState{}
		k.act.posted = map[string]time.Time{}
	}
	k.act.retry = false
	for _, d := range kickActivityEvents {
		if _, ok := have[d.name]; ok {
			k.act.ev[d.name] = kickEventState{state: activity.KindOn, at: now}
			continue
		}
		// Kick said no to this account; asking again every half hour is noise.
		if st := k.act.ev[d.name]; st.state == activity.KindUnavailable && now.Sub(st.at) < kickSubEmptyBackoff {
			continue
		}
		// Issue #419, per event: an empty listing right after we created it is
		// not evidence it is gone. A NON-empty listing that lacks it is.
		if p, ok := k.act.posted[d.name]; ok && listed == 0 && now.Sub(p) < kickSubEmptyBackoff {
			continue
		}
		want = append(want, d.name)
	}
	k.mu.Unlock()
	if len(want) == 0 {
		return
	}

	err := k.postActivity(ctx, endpoint, want)
	if err == nil || ctx.Err() != nil {
		return
	}
	// The batch was refused as a whole. If it was Kick objecting to the request
	// (not auth, not an outage), one bad event must not cost the rest: ask for
	// each on its own.
	if len(want) > 1 && kickDeclines(err) {
		for _, name := range want {
			if ctx.Err() != nil {
				return
			}
			_ = k.postActivity(ctx, endpoint, []string{name})
		}
		return
	}
	k.mu.Lock()
	k.act.retry = true
	k.mu.Unlock()
}

// kickDeclines reports whether err is Kick saying no to the request itself.
func kickDeclines(err error) bool {
	switch statusOf(err) {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// postActivity creates the named subscriptions in one request and records each
// outcome. It returns the request error, after recording it when the request
// named a single event.
func (k *KickAdapter) postActivity(ctx context.Context, endpoint string, names []string) error {
	evs := make([]map[string]any, 0, len(names))
	for _, n := range names {
		evs = append(evs, map[string]any{"name": n, "version": kickChatEventVersion})
	}
	body := map[string]any{
		"events":              evs,
		"method":              "webhook",
		"broadcaster_user_id": k.cfg.BroadcasterUserID,
	}
	var created struct {
		Data []struct {
			Name           string `json:"name"`
			SubscriptionID string `json:"subscription_id"`
			Error          string `json:"error"`
		} `json:"data"`
	}
	now := k.cfg.Now()
	k.mu.Lock()
	for _, n := range names {
		k.act.posted[n] = now
	}
	k.mu.Unlock()

	if err := k.api(ctx, http.MethodPost, endpoint, body, &created); err != nil {
		if ctx.Err() != nil {
			k.unstampPosted(names)
			return err
		}
		if ae, ok := err.(*apiError); ok && (ae.Status == http.StatusBadRequest || ae.Status == http.StatusConflict) && kickSaysDuplicate(ae.Body) {
			k.setEvents(names, kickEventState{state: activity.KindOn, at: now})
			return nil
		}
		if len(names) == 1 && kickDeclines(err) {
			k.setEvents(names, kickEventState{state: activity.KindUnavailable, at: now,
				detail: "Kick declined the " + names[0] + " subscription for this account: " + kickSaid(err)})
			return nil
		}
		// Transient failure: we tried and failed, we did not create it, so the
		// issue #419 "we just created it" backoff must not apply.
		k.unstampPosted(names)
		if len(names) == 1 {
			k.mu.Lock()
			k.act.retry = true
			k.mu.Unlock()
			return nil
		}
		return err
	}

	answered := map[string]bool{}
	for _, d := range created.Data {
		answered[d.Name] = true
		msg := strings.TrimSpace(d.Error)
		switch {
		case msg == "" || kickSaysDuplicate(msg):
			k.setEvents([]string{d.Name}, kickEventState{state: activity.KindOn, at: now})
		default:
			k.setEvents([]string{d.Name}, kickEventState{state: activity.KindUnavailable, at: now,
				detail: "Kick declined the " + d.Name + " subscription for this account: " + clipRunes(msg, 200)})
		}
	}
	for _, n := range names {
		if !answered[n] {
			k.setEvents([]string{n}, kickEventState{state: activity.KindOn, at: now})
		}
	}
	return nil
}

// unstampPosted forgets the posted stamp for events whose request failed
// transiently, so an empty listing is not read as "we created it".
func (k *KickAdapter) unstampPosted(names []string) {
	k.mu.Lock()
	for _, n := range names {
		delete(k.act.posted, n)
	}
	k.mu.Unlock()
}

func (k *KickAdapter) setEvents(names []string, st kickEventState) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.act.ev == nil {
		k.act.ev = map[string]kickEventState{}
	}
	for _, n := range names {
		prev := k.act.ev[n]
		k.act.ev[n] = st
		if prev.state != st.state {
			if st.state == activity.KindUnavailable {
				k.cfg.Log.Info("kick activity: an event is not available for this account", "account", k.cfg.AccountRef, "event", n)
			}
		}
	}
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ActivityKinds reports which activity kinds are flowing. A kind fed by two
// events (stream) is on when any is on; the declined one is named in Detail. It
// is unavailable only when every event behind it was declined, and unknown until
// Kick has confirmed the subscription.
func (k *KickAdapter) ActivityKinds() []activity.KindStatus {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]activity.KindStatus, 0, len(kickActivityKindOrder))
	for _, kind := range kickActivityKindOrder {
		var on, unavail, total int
		var details []string
		for _, d := range kickActivityEvents {
			if d.kind != kind {
				continue
			}
			total++
			switch st := k.act.ev[d.name]; st.state {
			case activity.KindOn:
				on++
			case activity.KindUnavailable:
				unavail++
				details = append(details, st.detail)
			}
		}
		ks := activity.KindStatus{Kind: kind}
		switch {
		case on > 0:
			ks.State = activity.KindOn
			ks.Detail = strings.Join(details, " ")
		case unavail == total:
			ks.State = activity.KindUnavailable
			ks.Detail = strings.Join(details, " ")
		case k.subProblem != "":
			ks.State = activity.KindUnknown
			ks.Detail = "Waiting on the Kick chat subscription, which is not in place yet."
		default:
			ks.State = activity.KindUnknown
			ks.Detail = "Waiting for Kick to confirm the subscription."
		}
		out = append(out, ks)
	}
	return out
}

// ------------------------------------------------------------- receiving

type kickActUser struct {
	IsAnonymous bool   `json:"is_anonymous"`
	UserID      int    `json:"user_id"`
	Username    string `json:"username"`
	ChannelSlug string `json:"channel_slug"`
}

func (u kickActUser) actor() *activity.Actor {
	a := &activity.Actor{Login: u.ChannelSlug, Name: u.Username}
	if u.UserID > 0 {
		a.ID = strconv.Itoa(u.UserID)
	}
	if u.IsAnonymous {
		a.Anonymous = true
		a.ID, a.Login, a.Name = "", "", ""
	}
	return a
}

// kickActDecoded is one decoded delivery before the checks that need the adapter.
type kickActDecoded struct {
	ev          activity.Event
	broadcaster int
	// at is the delivery-time timestamp the payload carries, when it carries
	// one; guarded says it is safe to refuse as stale when old (a stream's
	// started_at is hours old by design).
	at      time.Time
	guarded bool
}

func parseKickTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// kickActivityFrom decodes one delivery. ok is false for a payload that is not
// the event it says it is.
func kickActivityFrom(def kickActivityDef, body []byte) (d kickActDecoded, ok bool) {
	ev := activity.Event{Kind: def.kind, Platform: "kick", Source: activity.Source{Type: def.name}}
	if !decodeKickPayload(def.name, body, &ev, &d) {
		return d, false
	}
	d.ev = ev
	return d, true
}

// decodeKickPayload routes one delivery to the decoder for its event name. It
// reports false for an unknown name and for any payload that does not decode.
func decodeKickPayload(name string, body []byte, ev *activity.Event, d *kickActDecoded) bool {
	switch name {
	case kickEvFollowed:
		return decodeKickFollowed(body, ev, d)
	case kickEvSubNew, kickEvSubRenewal:
		return decodeKickSub(name, body, ev, d)
	case kickEvSubGifts:
		return decodeKickSubGifts(body, ev, d)
	case kickEvKicksGifted:
		return decodeKickKicksGifted(body, ev, d)
	case kickEvStatus:
		return decodeKickStatus(body, ev, d)
	case kickEvMetadata:
		return decodeKickMetadata(body, ev, d)
	case kickEvBanned:
		return decodeKickBanned(body, ev, d)
	case kickEvRedemption:
		return decodeKickRedemption(body, ev, d)
	default:
		return false
	}
}

func decodeKickFollowed(body []byte, ev *activity.Event, d *kickActDecoded) bool {
	var p struct {
		Broadcaster kickActUser `json:"broadcaster"`
		Follower    kickActUser `json:"follower"`
	}
	if json.Unmarshal(body, &p) != nil {
		return false
	}
	ev.Actor = p.Follower.actor()
	d.broadcaster = p.Broadcaster.UserID
	return true
}

// decodeKickSub covers a new subscription and a renewal, which share a payload.
func decodeKickSub(name string, body []byte, ev *activity.Event, d *kickActDecoded) bool {
	var p struct {
		Broadcaster kickActUser `json:"broadcaster"`
		Subscriber  kickActUser `json:"subscriber"`
		Duration    int         `json:"duration"`
		CreatedAt   string      `json:"created_at"`
	}
	if json.Unmarshal(body, &p) != nil {
		return false
	}
	ev.Actor = p.Subscriber.actor()
	ev.Product = "sub"
	if name == kickEvSubRenewal {
		// Months is the platform's figure; a renewal that does not say is
		// refused by Validate rather than given an invented 1.
		ev.Months = p.Duration
	}
	d.broadcaster = p.Broadcaster.UserID
	d.at, d.guarded = parseKickTime(p.CreatedAt), true
	return true
}

func decodeKickSubGifts(body []byte, ev *activity.Event, d *kickActDecoded) bool {
	var p struct {
		Broadcaster kickActUser   `json:"broadcaster"`
		Gifter      kickActUser   `json:"gifter"`
		Giftees     []kickActUser `json:"giftees"`
		CreatedAt   string        `json:"created_at"`
	}
	if json.Unmarshal(body, &p) != nil {
		return false
	}
	ev.Actor = p.Gifter.actor()
	ev.Product = "sub"
	ev.Count = len(p.Giftees)
	for _, g := range p.Giftees {
		if a := g.actor(); !a.Anonymous {
			ev.Recipients = append(ev.Recipients, activity.Recipient{ID: a.ID, Name: firstNonEmpty(a.Name, a.Login)})
		}
	}
	d.broadcaster = p.Broadcaster.UserID
	d.at, d.guarded = parseKickTime(p.CreatedAt), true
	return true
}

func decodeKickKicksGifted(body []byte, ev *activity.Event, d *kickActDecoded) bool {
	var p struct {
		Broadcaster kickActUser `json:"broadcaster"`
		Sender      kickActUser `json:"sender"`
		Gift        struct {
			Amount  int64  `json:"amount"`
			Name    string `json:"name"`
			Tier    string `json:"tier"`
			Message string `json:"message"`
		} `json:"gift"`
		CreatedAt string `json:"created_at"`
	}
	if json.Unmarshal(body, &p) != nil || p.Gift.Amount <= 0 {
		return false
	}
	ev.Actor = p.Sender.actor()
	// KICKs are a platform unit. There is no published rate to dollars, so
	// none is applied.
	ev.Amount = &activity.Amount{Unit: "kicks", Units: p.Gift.Amount}
	ev.Message = p.Gift.Message
	ev.Title = p.Gift.Name
	ev.Tier = p.Gift.Tier
	d.broadcaster = p.Broadcaster.UserID
	d.at, d.guarded = parseKickTime(p.CreatedAt), true
	return true
}

func decodeKickStatus(body []byte, ev *activity.Event, d *kickActDecoded) bool {
	var p struct {
		Broadcaster kickActUser `json:"broadcaster"`
		IsLive      *bool       `json:"is_live"`
		Title       string      `json:"title"`
		StartedAt   string      `json:"started_at"`
		EndedAt     string      `json:"ended_at"`
	}
	if json.Unmarshal(body, &p) != nil || p.IsLive == nil {
		return false
	}
	ev.Phase, ev.Title = "offline", p.Title
	ev.At = parseKickTime(p.EndedAt)
	if *p.IsLive {
		ev.Phase = "online"
		ev.At = parseKickTime(p.StartedAt)
	}
	d.broadcaster = p.Broadcaster.UserID
	return true
}

func decodeKickMetadata(body []byte, ev *activity.Event, d *kickActDecoded) bool {
	var p struct {
		Broadcaster kickActUser `json:"broadcaster"`
		Metadata    *struct {
			Title string `json:"title"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &p) != nil || p.Metadata == nil {
		return false
	}
	ev.Phase, ev.Title = "update", p.Metadata.Title
	d.broadcaster = p.Broadcaster.UserID
	return true
}

func decodeKickBanned(body []byte, ev *activity.Event, d *kickActDecoded) bool {
	var p struct {
		Broadcaster kickActUser `json:"broadcaster"`
		Moderator   kickActUser `json:"moderator"`
		BannedUser  kickActUser `json:"banned_user"`
		Metadata    struct {
			Reason    string  `json:"reason"`
			CreatedAt string  `json:"created_at"`
			ExpiresAt *string `json:"expires_at"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &p) != nil {
		return false
	}
	ev.Actor = p.Moderator.actor()
	ev.Other = p.BannedUser.actor()
	ev.Message = p.Metadata.Reason
	// expires_at is null for a permanent ban.
	ev.Action = "ban"
	if p.Metadata.ExpiresAt != nil && strings.TrimSpace(*p.Metadata.ExpiresAt) != "" {
		ev.Action = "timeout"
	}
	d.broadcaster = p.Broadcaster.UserID
	d.at, d.guarded = parseKickTime(p.Metadata.CreatedAt), true
	return true
}

func decodeKickRedemption(body []byte, ev *activity.Event, d *kickActDecoded) bool {
	var p struct {
		ID         string `json:"id"`
		UserInput  string `json:"user_input"`
		Status     string `json:"status"`
		RedeemedAt string `json:"redeemed_at"`
		Reward     struct {
			Title string `json:"title"`
		} `json:"reward"`
		Redeemer    kickActUser `json:"redeemer"`
		Broadcaster kickActUser `json:"broadcaster"`
	}
	if json.Unmarshal(body, &p) != nil || strings.TrimSpace(p.ID) == "" {
		return false
	}
	ev.Actor = p.Redeemer.actor()
	ev.Title = p.Reward.Title
	ev.Status = p.Status
	ev.Message = p.UserInput
	// One redemption is announced once. This event fires again as the
	// streamer accepts or rejects it, each time with a new delivery id, so
	// the dedupe identity is the redemption's own id; the later status
	// changes are the same fact.
	ev.Source.ID = p.ID
	d.broadcaster = p.Broadcaster.UserID
	d.at, d.guarded = parseKickTime(p.RedeemedAt), true
	return true
}

// ingestActivity handles one activity delivery that has already passed
// signature verification.
func (k *KickAdapter) ingestActivity(def kickActivityDef, messageID, timestamp string, body []byte) {
	now := k.cfg.Now()
	d, ok := kickActivityFrom(def, body)
	if ok {
		d.ev.Source.ID = firstNonEmpty(d.ev.Source.ID, messageID)
		if d.ev.Source.ID == "" {
			// No header: the same bytes are the same delivery.
			sum := sha256.Sum256(append([]byte(def.name+"\x00"), body...))
			d.ev.Source.ID = "sha-" + hex.EncodeToString(sum[:12])
		}
		d.ev.Account = k.cfg.AccountRef
		hdr := parseKickTime(timestamp)
		if d.ev.At.IsZero() {
			d.ev.At = firstTime(d.at, hdr)
		}
		d.ev = d.ev.Normalise(k.cfg.Now)
		ok = d.ev.Validate() == nil
	}
	if !ok {
		k.mu.Lock()
		k.act.malformed++
		k.mu.Unlock()
		// Type and length only: the body is viewer data and is not kept.
		k.cfg.Log.Debug("kick activity: could not read a delivery", "event", def.name, "bytes", len(body))
		return
	}

	// Which time says "this just happened"? The payload's, when it has one that
	// means that; else the signed header's.
	stamp := parseKickTime(timestamp)
	if d.guarded && !d.at.IsZero() {
		stamp = d.at
	}
	mine := k.cfg.BroadcasterUserID
	if (d.broadcaster != 0 && mine > 0 && d.broadcaster != mine) ||
		(!stamp.IsZero() && now.Sub(stamp) > kickActivityMaxAge) {
		k.mu.Lock()
		k.act.ignored++
		k.mu.Unlock()
		return
	}

	k.mu.Lock()
	sink := k.sink
	if sink == nil {
		// Same rule as chat (D4): activity flows only while this chat is attached.
		k.beforeAttach++
		k.mu.Unlock()
		return
	}
	key := def.name + "\x00" + firstNonEmpty(messageID, d.ev.Source.ID)
	if _, dup := k.act.seen[key]; dup {
		k.act.deduped++
		k.mu.Unlock()
		return
	}
	if k.act.seen == nil {
		k.act.seen = make(map[string]struct{}, kickActivitySeen)
		k.act.ring = make([]string, kickActivitySeen)
	}
	if old := k.act.ring[k.act.pos]; old != "" {
		delete(k.act.seen, old)
	}
	k.act.ring[k.act.pos] = key
	k.act.pos = (k.act.pos + 1) % kickActivitySeen
	k.act.seen[key] = struct{}{}
	k.act.delivered++
	k.mu.Unlock()

	emitActivity(sink, d.ev)
}

func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}
