package chat

// X live chat over a webhook.
//
// docs.x.com/livestream-api/chat/accessing-live-chat: live chat is delivered ONLY to a webhook. The steps, as Run
// performs them:
//
//  1. a webhook for <public origin>/api/v1/webhooks/x exists (GET /2/webhooks, then POST /2/webhooks, then
//     PUT /2/webhooks/{id} when X lists it invalid), all with the APP bearer;
//  2. a broadcast.chat and a broadcast.chat.remove subscription exist for the account's user id, pointing at that
//     webhook, created with the USER token (broadcast.read) and tagged "polyemesis";
//  3. X POSTs each event to the webhook, internal/api checks the signature over the raw body, and IngestWebhook
//     queues the verified body. Run's loop turns it into a Message (or a retraction) and drops a replay.
//
// X validates the address with a CRC GET when the webhook is created, every hour, and on PUT. That GET is answered
// by the same process, so the route is up before this registration runs.

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

const (
	// xSubTag marks the subscriptions this adapter owns. Only a tagged subscription is ever removed by polyemesis:
	// one with another tag belongs to something else on the same X app.
	xSubTag = "polyemesis"

	xEventChat       = "broadcast.chat"
	xEventChatRemove = "broadcast.chat.remove"

	// xInboxSize bounds the verified events waiting for the Run loop. X's own retry covers a delivery answered with a
	// failure, but this one is answered 200 at once (the 10 second rule), so a full inbox drops.
	xInboxSize = 256
	// xSeenMax bounds the dedupe memory, in event_uuids.
	xSeenMax = 2048
	// xWebhookURLMax is X's limit on a webhook URL (CreateWebhooksRequest.url maxLength).
	xWebhookURLMax = 200
)

// xRegisterMu serialises the webhook list-then-create. Every connected X account runs Run against the same app and
// the same URL, and without this each would list, find nothing, and create.
var xRegisterMu sync.Mutex

// ------------------------------------------------------------------ the address

// xWebhookURLProblem says why X would refuse raw as a webhook URL, or "" when it would not. X's rules
// (docs.x.com/x-api/webhooks/introduction): HTTPS, publicly reachable, no port, at most 200 characters.
func xWebhookURLProblem(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "no public address is configured"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return fmt.Sprintf("the webhook address %q is not an HTTPS URL, which X requires", raw)
	}
	if u.Port() != "" {
		return fmt.Sprintf("the webhook address %q has a port, which X rejects; serve on 443 and leave the port out", raw)
	}
	if len(raw) > xWebhookURLMax {
		return fmt.Sprintf("the webhook address is %d characters and X accepts at most %d", len(raw), xWebhookURLMax)
	}
	return ""
}

// xAddressError is the operator-facing wording shared by every failure that comes down to the address.
func xAddressError(detail string) error {
	return fmt.Errorf("X chat needs a public HTTPS address X can reach: %s", detail)
}

// xCRCError is the same, for a webhook X registered or tried to and could not validate.
func xCRCError(hookURL, detail string) error {
	return fmt.Errorf("X chat needs a public HTTPS address X can reach (webhook CRC failed for %s): %s", hookURL, detail)
}

// xWhy is X's own words for a failed call, or the status when X gave none. The raw body is never included.
func xWhy(err error) string {
	if r := xProblemMessage(err); r != "" {
		return r
	}
	if s := statusOf(err); s != 0 {
		return "HTTP " + strconv.Itoa(s)
	}
	return err.Error()
}

// ------------------------------------------------------------------ webhooks

type xWebhookRec struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Valid bool   `json:"valid"`
}

func (x *XAdapter) listWebhooks(ctx context.Context) ([]xWebhookRec, error) {
	var out struct {
		Data []xWebhookRec `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodGet, "/2/webhooks", true, nil, &out); err != nil {
		return nil, fmt.Errorf("X would not list webhooks: %s", xWhy(err))
	}
	return out.Data, nil
}

func (x *XAdapter) findWebhook(whs []xWebhookRec) (xWebhookRec, bool) {
	for _, h := range whs {
		if strings.EqualFold(strings.TrimSpace(h.URL), x.cfg.WebhookURL) {
			return h, true
		}
	}
	return xWebhookRec{}, false
}

// ensureWebhook returns the id of a valid webhook for WebhookURL, creating or re-validating it as needed. It holds
// xRegisterMu throughout. A webhook X lists as invalid is NOT deleted: it may be in use, and PUT is the documented
// way to run its CRC again.
func (x *XAdapter) ensureWebhook(ctx context.Context) (string, error) {
	xRegisterMu.Lock()
	defer xRegisterMu.Unlock()

	whs, err := x.listWebhooks(ctx)
	if err != nil {
		return "", err
	}
	if h, ok := x.findWebhook(whs); ok {
		if h.Valid {
			return h.ID, nil
		}
		return x.revalidateWebhook(ctx, h.ID)
	}

	var out struct {
		Data xWebhookRec `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodPost, "/2/webhooks", true, map[string]string{"url": x.cfg.WebhookURL}, &out); err != nil {
		// Another process may have created it between the list and this call.
		if again, lerr := x.listWebhooks(ctx); lerr == nil {
			if h, ok := x.findWebhook(again); ok {
				if h.Valid {
					return h.ID, nil
				}
				return x.revalidateWebhook(ctx, h.ID)
			}
		}
		switch s := statusOf(err); {
		case s == http.StatusUnauthorized || s == http.StatusForbidden:
			return "", fmt.Errorf("X refused the webhook registration, so check POLYEMESIS_X_APP_BEARER and that the app has Webhooks enabled: %s", xWhy(err))
		case s == http.StatusTooManyRequests || s >= 500:
			return "", fmt.Errorf("X would not register the webhook right now: %s", xWhy(err))
		}
		return "", xCRCError(x.cfg.WebhookURL, xWhy(err))
	}
	if strings.TrimSpace(out.Data.ID) == "" {
		return "", errors.New("X created the webhook but returned no id, so chat cannot subscribe to it")
	}
	if !out.Data.Valid {
		// X keeps the webhook, listed invalid. The next attempt finds it and re-validates it.
		return "", xCRCError(x.cfg.WebhookURL, "X reached the address but did not accept its answer")
	}
	return out.Data.ID, nil
}

// revalidateWebhook runs PUT /2/webhooks/{id} and reads the verdict from a fresh list, because the PUT answer's
// data.valid only says the CRC was TRIGGERED (docs.x.com/x-api/webhooks/validate-webhook).
func (x *XAdapter) revalidateWebhook(ctx context.Context, id string) (string, error) {
	if err := x.xCall(ctx, http.MethodPut, "/2/webhooks/"+url.PathEscape(id), true, nil, nil); err != nil {
		return "", xCRCError(x.cfg.WebhookURL, xWhy(err))
	}
	whs, err := x.listWebhooks(ctx)
	if err != nil {
		return "", err
	}
	for _, h := range whs {
		if h.ID == id && h.Valid {
			return id, nil
		}
	}
	return "", xCRCError(x.cfg.WebhookURL, "X still lists the webhook as invalid after re-checking it")
}

// ------------------------------------------------------------- subscriptions

type xSubRec struct {
	ID        string `json:"subscription_id"`
	EventType string `json:"event_type"`
	Filter    struct {
		UserID string `json:"user_id"`
	} `json:"filter"`
	WebhookID string `json:"webhook_id"`
	Tag       string `json:"tag"`
	ExpiresAt string `json:"expires_at"`
}

// listSubscriptions reads the account's subscriptions with the APP bearer: the user token would need tweet.read or
// like.read for this route, which a broadcast.read grant need not hold.
func (x *XAdapter) listSubscriptions(ctx context.Context, userID string) ([]xSubRec, error) {
	var out struct {
		Data []xSubRec `json:"data"`
	}
	path := "/2/activity/subscriptions?" + url.Values{"user_id": {userID}}.Encode()
	if err := x.xCall(ctx, http.MethodGet, path, true, nil, &out); err != nil {
		return nil, fmt.Errorf("X would not list the activity subscriptions: %s", xWhy(err))
	}
	return out.Data, nil
}

func (s xSubRec) expired(now time.Time) bool {
	if s.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, s.ExpiresAt)
	return err == nil && !t.After(now)
}

// ensureSubscriptions makes sure the account has a live subscription for each chat event on webhookID. It returns the
// ids this adapter owns, which Run removes on the way out, and returns them even alongside an error so a half-built
// set is not left behind.
func (x *XAdapter) ensureSubscriptions(ctx context.Context, userID, webhookID string) ([]string, error) {
	existing, err := x.listSubscriptions(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := x.cfg.Now()
	var owned []string
	for _, event := range []string{xEventChat, xEventChatRemove} {
		reuse := false
		for _, s := range existing {
			if s.EventType != event || s.Filter.UserID != userID || s.expired(now) || strings.TrimSpace(s.ID) == "" {
				continue
			}
			if s.WebhookID == webhookID {
				reuse = true
				if s.Tag == xSubTag {
					owned = append(owned, s.ID)
				}
				break
			}
			if s.Tag == xSubTag {
				// Ours, but on a webhook that is no longer the current one. It would keep a new one from being made.
				x.removeSubscription(s.ID)
			}
		}
		if reuse {
			continue
		}
		var sub struct {
			Data struct {
				Subscription struct {
					ID string `json:"subscription_id"`
				} `json:"subscription"`
			} `json:"data"`
		}
		if err := x.xCall(ctx, http.MethodPost, "/2/activity/subscriptions", false, map[string]any{
			"event_type": event,
			"filter":     map[string]string{"user_id": userID},
			"webhook_id": webhookID,
			"tag":        xSubTag,
		}, &sub); err != nil {
			return owned, fmt.Errorf("X refused the %s subscription: %s", event, xWhy(err))
		}
		id := strings.TrimSpace(sub.Data.Subscription.ID)
		if id == "" {
			// X created a subscription this run cannot name, so it can never be removed. Fatal, because a retry would
			// only create, and leak, another one.
			return owned, Fatal(fmt.Errorf("X created the %s subscription but returned no subscription_id, "+
				"so polyemesis could not remove it later; live chat is stopped rather than leaving subscriptions "+
				"running on X's side", event))
		}
		owned = append(owned, id)
	}
	return owned, nil
}

// removeSubscription deletes one activity subscription with the user token. It uses a fresh context because the
// run's context is already done by the time this runs on shutdown.
func (x *XAdapter) removeSubscription(subID string) {
	if subID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = x.xCall(ctx, http.MethodDelete, "/2/activity/subscriptions/"+url.PathEscape(subID), false, nil, nil)
}

// ------------------------------------------------------------------ Run

// Run registers the webhook and the subscriptions, then delivers the events IngestWebhook queues until ctx ends. It
// removes the subscriptions it owns on the way out and leaves the webhook, which every X account shares.
func (x *XAdapter) Run(ctx context.Context, sink Sink) error {
	if strings.TrimSpace(x.cfg.AppBearer) == "" {
		return Fatal(errors.New("X live chat needs an app-only bearer token to register its webhook with X, " +
			"which polyemesis does not have. Set POLYEMESIS_X_APP_BEARER to the app's bearer token " +
			"from the X developer console. Until then the chat pane stays empty, and sending still works"))
	}
	if strings.TrimSpace(x.cfg.WebhookURL) == "" {
		reason := strings.TrimSpace(x.cfg.WebhookProblem)
		if reason == "" {
			reason = "no public address is configured"
		}
		return Fatal(xAddressError(reason))
	}
	if p := xWebhookURLProblem(x.cfg.WebhookURL); p != "" {
		return Fatal(xAddressError(p))
	}

	var me struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := x.xCall(ctx, http.MethodGet, "/2/users/me", false, nil, &me); err != nil {
		return fmt.Errorf("X would not say which account to read chat for: %s", xWhy(err))
	}
	userID := strings.TrimSpace(me.Data.ID)
	if userID == "" {
		return errors.New("X did not name the account to read chat for")
	}

	hookID, err := x.ensureWebhook(ctx)
	if err != nil {
		return err
	}
	owned, err := x.ensureSubscriptions(ctx, userID, hookID)
	defer func() {
		for _, id := range owned {
			x.removeSubscription(id)
		}
	}()
	if err != nil {
		return err
	}

	inbox := make(chan xWebhookEvent, xInboxSize)
	x.goLive(userID, inbox)
	defer x.goDark(inbox)

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-inbox:
			x.deliver(sink, ev)
		}
	}
}

func (x *XAdapter) goLive(userID string, inbox chan xWebhookEvent) {
	x.liveMu.Lock()
	x.liveUser, x.liveInbox = userID, inbox
	x.liveMu.Unlock()
}

func (x *XAdapter) goDark(inbox chan xWebhookEvent) {
	x.liveMu.Lock()
	if x.liveInbox == inbox {
		x.liveUser, x.liveInbox = "", nil
	}
	x.liveMu.Unlock()
}

// WebhookUserID is the X user id this adapter receives events for, or "" while Run is not registered and subscribed.
func (x *XAdapter) WebhookUserID() string {
	x.liveMu.RLock()
	defer x.liveMu.RUnlock()
	if x.liveInbox == nil {
		return ""
	}
	return x.liveUser
}

// ------------------------------------------------------------------ events

// XIngest is what became of one verified delivery.
type XIngest int

const (
	// XIngestQueued: handed to the Run loop.
	XIngestQueued XIngest = iota
	// XIngestNotRunning: this adapter is not registered right now, so the event has nowhere to go.
	XIngestNotRunning
	// XIngestWrongAccount: the event is for another user id.
	XIngestWrongAccount
	// XIngestMalformed: not an event envelope.
	XIngestMalformed
	// XIngestIgnored: an event type chat does not use.
	XIngestIgnored
	// XIngestFull: the inbox is full and the event was dropped.
	XIngestFull
)

func (r XIngest) String() string {
	switch r {
	case XIngestQueued:
		return "queued"
	case XIngestNotRunning:
		return "not-running"
	case XIngestWrongAccount:
		return "wrong-account"
	case XIngestMalformed:
		return "malformed"
	case XIngestIgnored:
		return "ignored"
	case XIngestFull:
		return "full"
	}
	return "unknown"
}

// xWebhookEvent is the decoded XAA envelope: {data:{event_uuid, created_at, filter, event_type, tag, payload}}.
type xWebhookEvent struct {
	UUID      string
	UserID    string
	Type      string
	CreatedAt string
	Payload   json.RawMessage
}

type xEnvelope struct {
	Data *struct {
		EventUUID string `json:"event_uuid"`
		CreatedAt string `json:"created_at"`
		EventType string `json:"event_type"`
		Filter    struct {
			UserID string `json:"user_id"`
		} `json:"filter"`
		Payload json.RawMessage `json:"payload"`
	} `json:"data"`
}

func parseXEnvelope(body []byte) (xWebhookEvent, bool) {
	var env xEnvelope
	if err := json.Unmarshal(body, &env); err != nil || env.Data == nil {
		return xWebhookEvent{}, false
	}
	d := env.Data
	if strings.TrimSpace(d.EventUUID) == "" || strings.TrimSpace(d.Filter.UserID) == "" || d.EventType == "" {
		return xWebhookEvent{}, false
	}
	return xWebhookEvent{UUID: d.EventUUID, UserID: d.Filter.UserID, Type: d.EventType, CreatedAt: d.CreatedAt, Payload: d.Payload}, true
}

// XWebhookUser is the user id an event body is for (data.filter.user_id), or "" when the body is not an envelope. The
// route uses it to pick the adapter.
func XWebhookUser(body []byte) string {
	ev, ok := parseXEnvelope(body)
	if !ok {
		return ""
	}
	return ev.UserID
}

// IngestWebhook accepts one body whose signature internal/api has already verified. It never blocks: the verdict says
// whether the event was queued, and a full inbox drops it, because the route answers X within 10 seconds however slow
// the sink is.
func (x *XAdapter) IngestWebhook(body []byte) XIngest {
	ev, ok := parseXEnvelope(body)
	if !ok {
		return XIngestMalformed
	}
	x.liveMu.RLock()
	inbox, user := x.liveInbox, x.liveUser
	x.liveMu.RUnlock()
	if inbox == nil {
		return XIngestNotRunning
	}
	if ev.UserID != user {
		return XIngestWrongAccount
	}
	if ev.Type != xEventChat && ev.Type != xEventChatRemove {
		return XIngestIgnored
	}
	select {
	case inbox <- ev:
		return XIngestQueued
	default:
		return XIngestFull
	}
}

// xChatPayload is the broadcast.chat payload as docs.x.com/x-api/activity/event-payloads documents it: the text is in
// message, the message id is message_id, and the author sits under author.data.
type xChatPayload struct {
	MessageID   string `json:"message_id"`
	BroadcastID string `json:"broadcast_id"`
	Message     string `json:"message"`
	ReplyTo     string `json:"reply_to"`
	Author      struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"data"`
	} `json:"author"`
}

// deliver turns one queued event into a Message or a retraction. The event_uuid is recorded only once the event has
// been acted on, so a delivery that never got that far is not swallowed when X replays it.
func (x *XAdapter) deliver(sink Sink, ev xWebhookEvent) {
	if x.seen.has(ev.UUID) {
		return
	}
	switch ev.Type {
	case xEventChat:
		if m, ok := x.chatMessage(ev); ok {
			sink.Deliver(m)
		}
	case xEventChatRemove:
		var p struct {
			MessageID string `json:"message_id"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil {
			retract(sink, strings.TrimSpace(p.MessageID))
		}
	}
	x.seen.add(ev.UUID)
}

// chatMessage maps a broadcast.chat event to the Message the chat model uses. An event that carries no text is
// dropped. The documented payload has no created time of its own, so the envelope's created_at stands in for it
// (never later than now), and arrival time when that is absent.
func (x *XAdapter) chatMessage(ev xWebhookEvent) (Message, bool) {
	var p xChatPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil || strings.TrimSpace(p.Message) == "" {
		return Message{}, false
	}
	now := x.cfg.Now()
	at := now
	if t, err := time.Parse(time.RFC3339Nano, ev.CreatedAt); err == nil && t.Before(now) {
		at = t
	}
	name := p.Author.Data.Name
	if name == "" {
		name = p.Author.Data.Username
	}
	id := p.MessageID
	if id == "" {
		id = "syn-x-" + p.Author.Data.ID + "-" + strconv.FormatInt(now.UnixNano(), 10)
	}
	return Message{
		ID:        id,
		Platform:  db.PlatformX,
		Account:   x.cfg.AccountRef,
		Channel:   x.cfg.Channel,
		Author:    Author{ID: p.Author.Data.ID, Name: name},
		Text:      p.Message,
		At:        at,
		ReplyToID: p.ReplyTo,
	}, true
}

// ------------------------------------------------------------------ dedupe

// xSeen is a bounded LRU of event_uuids.
type xSeen struct {
	mu  sync.Mutex
	max int
	ll  *list.List
	m   map[string]*list.Element
}

func newXSeen(max int) *xSeen {
	return &xSeen{max: max, ll: list.New(), m: make(map[string]*list.Element, max)}
}

// has reports whether id was recorded, and counts the lookup as a use.
func (s *xSeen) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[id]; ok {
		s.ll.MoveToFront(e)
		return true
	}
	return false
}

func (s *xSeen) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[id]; ok {
		s.ll.MoveToFront(e)
		return
	}
	s.m[id] = s.ll.PushFront(id)
	for s.ll.Len() > s.max {
		last := s.ll.Back()
		s.ll.Remove(last)
		delete(s.m, last.Value.(string))
	}
}

func (s *xSeen) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ll.Len()
}
