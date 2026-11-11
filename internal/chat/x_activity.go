package chat

// X viewer activity over the same Activity API webhook chat uses.
//
// docs.x.com/x-api/activity/event-payloads and /introduction, checked 2026-11-07:
//
//	follow.follow            a follow involving the filtered user, BOTH directions unless filter.direction is set.
//	                         The payload is {source, target}: the follower and the user followed. Needs tweet.read.
//	subscriptions.subscribe  a paid (Premium) subscription involving the filtered user; {source, target} the same
//	                         way. PRIVATE: it needs the follows.read scope, and filter.user_id must be the
//	                         authenticated user. Supports filter.direction.
//	broadcast.start / .end   public; the payload carries the host `user`, broadcast_id, title and broadcast_state.
//
// Only INBOUND follows and subscriptions are alerts, so the subscription asks for filter.direction "inbound" AND the
// delivery is checked again against payload.target, because the docs' own follow.follow example puts the source in
// filter.user_id and a mis-filtered delivery must not be shown as a follow of the broadcaster.
//
// The dedupe id is the delivery's event_uuid. Nothing is replayed: X may redeliver an event after a restart, and an
// event created before this adapter was built, or more than xActivityMaxAge ago, is dropped rather than shown.
// Neither event carries a price, so no amount is invented. Viewer text does not appear in any of them; the names are
// viewer-chosen and are judged by the overlay gate like every other name.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/db"
)

const (
	xEventFollow         = "follow.follow"
	xEventSubscribe      = "subscriptions.subscribe"
	xEventBroadcastStart = "broadcast.start"
	xEventBroadcastEnd   = "broadcast.end"

	xDirInbound = "inbound"

	// xActivityMaxAge is how old an activity event may be and still be shown. An alert for something that
	// happened a quarter of an hour ago is history, not an alert.
	xActivityMaxAge = 15 * time.Minute
)

// xActivitySpec is one Activity API event type the activity source subscribes to.
type xActivitySpec struct {
	event     string
	kind      activity.Kind
	direction string // "" for none
}

var xActivitySpecs = []xActivitySpec{
	{xEventFollow, activity.KindFollow, xDirInbound},
	{xEventSubscribe, activity.KindSubscribe, xDirInbound},
	{xEventBroadcastStart, activity.KindStream, ""},
	{xEventBroadcastEnd, activity.KindStream, ""},
}

func xIsActivityEvent(t string) bool {
	for _, s := range xActivitySpecs {
		if s.event == t {
			return true
		}
	}
	return false
}

// xActivityState is the activity half of the adapter, under its own lock.
type xActivityState struct {
	mu sync.Mutex
	// wanted is set by Run when its sink can take activity; without it nothing is subscribed and the events are
	// ignored, exactly as before.
	wanted bool
	// floor is when this adapter was built: an event created before it is a replay.
	floor time.Time
	// events is the per-event-type outcome of the last subscription attempt.
	events map[string]xActivityEvent
	// seen is the kinds an event has been delivered for since Run started (Premium subscriptions are unknowable
	// until one arrives).
	seen map[activity.Kind]bool
	// counters, for tests and logs. Never the payload.
	emitted, malformed, stale, notInbound int
}

type xActivityEvent struct {
	state  activity.KindState // on | unavailable | reconnect | off
	detail string
	// final: do not try this event again in this run (X refused it for a reason a retry does not change).
	final bool
}

func (a *xActivityState) want() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.wanted
}

func (a *xActivityState) setWanted(v bool) {
	a.mu.Lock()
	a.wanted = v
	a.mu.Unlock()
}

func (a *xActivityState) set(event string, st xActivityEvent) {
	a.mu.Lock()
	if a.events == nil {
		a.events = map[string]xActivityEvent{}
	}
	a.events[event] = st
	a.mu.Unlock()
}

func (a *xActivityState) get(event string) (xActivityEvent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.events[event]
	return st, ok
}

func (a *xActivityState) reset() {
	a.mu.Lock()
	a.events, a.seen = nil, nil
	a.mu.Unlock()
}

// ActivityKinds reports the activity kinds this account's X connection can deliver, for GET /api/v1/activity/sources.
// A kind X refused for this account is `unavailable`, which is not a failure: chat keeps working. A kind that needs
// a permission the stored token lacks is `reconnect`.
func (x *XAdapter) ActivityKinds() []activity.KindStatus {
	a := &x.act
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []activity.KindStatus
	for _, kind := range []activity.Kind{activity.KindFollow, activity.KindSubscribe, activity.KindStream} {
		out = append(out, a.kindStatus(kind))
	}
	return out
}

// kindStatus folds the per-event outcomes of a kind into one word. Caller holds a.mu.
func (a *xActivityState) kindStatus(kind activity.Kind) activity.KindStatus {
	if !a.wanted {
		return activity.KindStatus{Kind: kind, State: activity.KindOff, Detail: "X activity is not running for this account"}
	}
	var on, total int
	var worst xActivityEvent
	var haveWorst bool
	for _, s := range xActivitySpecs {
		if s.kind != kind {
			continue
		}
		total++
		st, ok := a.events[s.event]
		if !ok {
			st = xActivityEvent{state: activity.KindOff, detail: "waiting for X to register the subscription"}
		}
		if st.state == activity.KindOn {
			on++
			continue
		}
		if !haveWorst || xKindRank(st.state) > xKindRank(worst.state) {
			worst, haveWorst = st, true
		}
	}
	switch {
	case total > 0 && on == total:
		if kind == activity.KindSubscribe && !a.seen[kind] {
			return activity.KindStatus{Kind: kind, State: activity.KindUnknown,
				Detail: "Subscribed. X sends Premium subscriptions only when Creator Subscriptions are on for this account, which polyemesis cannot read, so this shows as on once one arrives."}
		}
		return activity.KindStatus{Kind: kind, State: activity.KindOn}
	case haveWorst:
		return activity.KindStatus{Kind: kind, State: worst.state, Detail: worst.detail}
	}
	return activity.KindStatus{Kind: kind, State: activity.KindOff}
}

func xKindRank(s activity.KindState) int {
	switch s {
	case activity.KindReconnect:
		return 3
	case activity.KindUnavailable:
		return 2
	case activity.KindOff:
		return 1
	}
	return 0
}

// activityNote is the activity remark Health adds to the chat detail: plain words, only when something needs the
// operator, and never a failure of the connection.
func (x *XAdapter) activityNote() string {
	a := &x.act
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.wanted {
		return ""
	}
	var parts []string
	for _, s := range xActivitySpecs {
		st, ok := a.events[s.event]
		if !ok || (st.state != activity.KindReconnect && st.state != activity.KindUnavailable) {
			continue
		}
		parts = append(parts, fmt.Sprintf("X %s alerts are off: %s", xAlertWord(s.event), st.detail))
	}
	return strings.Join(parts, ". ")
}

func xAlertWord(event string) string {
	switch event {
	case xEventFollow:
		return "follow"
	case xEventSubscribe:
		return "Premium subscription"
	}
	return "broadcast start/end"
}

// ------------------------------------------------------------ subscribing

// ensureActivity makes sure each activity event has a live subscription on webhookID, reusing what exists. It never
// fails the run for a refusal of an activity event: chat is the job, activity is additive, so an event X will not
// give this account is reported through ActivityKinds and the rest carry on. What it does return is what chat would
// stop for too: a refused credential. A rate limit is not one of them; it leaves the kind off to be retried.
func (x *XAdapter) ensureActivity(ctx context.Context, userID, webhookID string, existing []xSubRec) error {
	now := x.cfg.Now()
	hosts := map[string]string{}
	for _, sp := range xActivitySpecs {
		if cur, ok := x.act.get(sp.event); ok && cur.final {
			continue
		}
		reuse := false
		for _, s := range existing {
			if s.EventType != sp.event || s.Filter.UserID != userID || s.expired(now) || strings.TrimSpace(s.ID) == "" {
				continue
			}
			if s.WebhookID == webhookID && (sp.direction == "" || s.direction() == sp.direction) {
				reuse = true
				break
			}
			if s.Tag == xSubTag {
				// Ours by tag but on another webhook, or without the direction we need: either would keep the
				// right one from being made.
				if s.WebhookID != webhookID {
					x.noteReplaced(ctx, s.WebhookID, hosts)
				}
				x.removeSubscription(s.ID)
			}
		}
		if reuse {
			x.act.set(sp.event, xActivityEvent{state: activity.KindOn})
			continue
		}
		filter := map[string]string{"user_id": userID}
		if sp.direction != "" {
			filter["direction"] = sp.direction
		}
		var sub xSubscribeResponse
		err := x.xCall(ctx, http.MethodPost, "/2/activity/subscriptions", false, map[string]any{
			"event_type": sp.event,
			"filter":     filter,
			"webhook_id": webhookID,
			"tag":        xSubTag,
		}, &sub)
		if err != nil {
			if stop := x.activityRefusal(sp, err); stop != nil {
				return stop
			}
			continue
		}
		if strings.TrimSpace(sub.Data.Subscription.ID) == "" {
			// A subscription this run cannot name can never be removed, and a retry would leak another.
			x.act.set(sp.event, xActivityEvent{state: activity.KindUnavailable, final: true,
				detail: "X created the " + sp.event + " subscription but did not name it, so polyemesis left it alone"})
			continue
		}
		x.act.set(sp.event, xActivityEvent{state: activity.KindOn})
	}
	return nil
}

// activityRefusal records why X would not create sp and says whether the run must stop for it (nil: carry on). Only
// a refused credential stops it, because chat could not run either.
func (x *XAdapter) activityRefusal(sp xActivitySpec, err error) error {
	var rl *xChatRateLimited
	var cred *xCredentialError
	switch {
	case errors.As(err, &rl):
		// Chat is already registered when this runs, and an additive kind must never take it down: ending the run
		// for a rate limit would close the inbox until the reset. The hold book makes the remaining creates in this
		// pass fail fast the same way, and the periodic check asks again.
		x.act.set(sp.event, xActivityEvent{state: activity.KindOff, detail: "X asked polyemesis to slow down, so this is retried later"})
		return nil
	case errors.As(err, &cred):
		if cred.kind == xCredScope {
			x.act.set(sp.event, xActivityEvent{state: activity.KindReconnect, final: true,
				detail: "the connected X account has not granted the permission this needs; reconnect X in Settings → Platforms"})
			return nil
		}
		return err // a refused user token or app bearer ends chat too
	}
	if s := statusOf(err); s >= 400 && s < 500 && s != http.StatusRequestTimeout {
		x.act.set(sp.event, xActivityEvent{state: activity.KindUnavailable, final: true,
			detail: "X would not subscribe this account: " + xWhy(err)})
		if x.cfg.Log != nil {
			x.cfg.Log.Info("x activity: subscription refused; that kind is unavailable", "event", sp.event,
				"account", x.cfg.AccountRef, "status", s)
		}
		return nil
	}
	// A 5xx or a transport failure says nothing about this account: off, and tried again at the next check.
	x.act.set(sp.event, xActivityEvent{state: activity.KindOff, detail: "X did not answer the subscription (" + xWhy(err) + "); trying again later"})
	return nil
}

// recheckActivity re-runs ensureActivity at the periodic check, so an event whose subscription expired or whose
// creation failed transiently is made again. Errors are ignored: the check says nothing about chat.
func (x *XAdapter) recheckActivity(ctx context.Context, userID, webhookID string) {
	if !x.act.want() {
		return
	}
	subs, err := x.listSubscriptions(ctx, userID)
	if err != nil {
		return
	}
	_ = x.ensureActivity(ctx, userID, webhookID, subs)
}

// ------------------------------------------------------------ delivering

type xUserRef struct {
	Data struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"data"`
}

func (u xUserRef) actor() *activity.Actor {
	name := strings.TrimSpace(u.Data.Name)
	if name == "" {
		name = strings.TrimSpace(u.Data.Username)
	}
	return &activity.Actor{ID: u.Data.ID, Login: u.Data.Username, Name: name}
}

// activityEvent maps one verified delivery to an activity, or says why it is not one: ok false with the reason
// counted. Whether it is shown at all is the hub's decision (settings, dedupe).
func (x *XAdapter) activityEvent(ev xWebhookEvent) (activity.Event, bool) {
	a := &x.act
	now := x.cfg.Now()
	at := now
	t, err := time.Parse(time.RFC3339Nano, ev.CreatedAt)
	if err != nil {
		// Without a creation time the replay floor and age limit cannot be applied, and a
		// restart clears the in-memory dedupe, so an activity delivery is treated as stale.
		switch ev.Type {
		case xEventFollow, xEventSubscribe, xEventBroadcastStart, xEventBroadcastEnd:
			a.bump(&a.stale)
			return activity.Event{}, false
		}
	} else {
		if t.Before(a.floor) || now.Sub(t) > xActivityMaxAge {
			a.bump(&a.stale)
			return activity.Event{}, false
		}
		if t.Before(now) {
			at = t
		}
	}
	out := activity.Event{
		Platform: string(db.PlatformX), Account: x.cfg.AccountRef, At: at,
		Source: activity.Source{Type: ev.Type, ID: ev.UUID},
	}
	switch ev.Type {
	case xEventFollow, xEventSubscribe:
		var p struct {
			Source xUserRef `json:"source"`
			Target xUserRef `json:"target"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || strings.TrimSpace(p.Source.Data.ID) == "" || strings.TrimSpace(p.Target.Data.ID) == "" ||
			(strings.TrimSpace(p.Source.Data.Name) == "" && strings.TrimSpace(p.Source.Data.Username) == "") {
			x.malformed(ev)
			return activity.Event{}, false
		}
		if p.Target.Data.ID != x.cfg.AccountRef {
			// The broadcaster followed or subscribed to someone else: not an alert.
			a.bump(&a.notInbound)
			return activity.Event{}, false
		}
		out.Actor = p.Source.actor()
		if ev.Type == xEventFollow {
			out.Kind = activity.KindFollow
		} else {
			out.Kind, out.Product = activity.KindSubscribe, "premium"
		}
	case xEventBroadcastStart, xEventBroadcastEnd:
		var p struct {
			Title string   `json:"title"`
			State string   `json:"broadcast_state"`
			User  xUserRef `json:"user"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil {
			x.malformed(ev)
			return activity.Event{}, false
		}
		if id := strings.TrimSpace(p.User.Data.ID); id != "" && id != x.cfg.AccountRef {
			a.bump(&a.notInbound)
			return activity.Event{}, false
		}
		out.Kind, out.Title, out.Phase = activity.KindStream, p.Title, "online"
		if ev.Type == xEventBroadcastEnd {
			out.Phase = "offline"
		}
	default:
		return activity.Event{}, false
	}
	if err := out.Normalise(x.cfg.Now).Validate(); err != nil {
		x.malformed(ev)
		return activity.Event{}, false
	}
	a.mu.Lock()
	a.emitted++
	if a.seen == nil {
		a.seen = map[activity.Kind]bool{}
	}
	a.seen[out.Kind] = true
	a.mu.Unlock()
	return out, true
}

func (a *xActivityState) bump(c *int) {
	a.mu.Lock()
	*c++
	a.mu.Unlock()
}

// malformed counts a payload that could not be read and logs its type and size, never its body.
func (x *XAdapter) malformed(ev xWebhookEvent) {
	x.act.bump(&x.act.malformed)
	if x.cfg.Log != nil {
		x.cfg.Log.Warn("x activity: an event could not be read and was dropped",
			"event", ev.Type, "bytes", len(ev.Payload), "account", x.cfg.AccountRef)
	}
}
