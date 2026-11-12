package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

// The payloads below are the examples docs.x.com/x-api/activity/event-payloads gives, trimmed of the profile fields
// the adapter does not read, with the ids made consistent: the broadcaster is 1234567890, the stub's account.

const (
	xActMe     = "1234567890"
	xActViewer = "9876543210"
)

// xActNow is the clock the activity tests start the adapter at; events are created a little after it.
var xActNow = time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)

const xActCreated = "2026-10-07T19:17:36.304Z"

func followEnvelope(uuid, createdAt, filterUser, sourceID, targetID string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"event_uuid":%q,"created_at":%q,"filter":{"user_id":%q},"event_type":"follow.follow","tag":"follows","payload":{"source":{"data":{"id":%q,"username":"ExampleUser","name":"Example User","protected":false,"verified":true,"public_metrics":{"followers_count":5502}}},"target":{"data":{"id":%q,"username":"creator","name":"Creator"}}}}}`,
		uuid, createdAt, filterUser, sourceID, targetID))
}

func subscribeEnvelope(uuid, createdAt, sourceID, targetID string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"event_uuid":%q,"created_at":%q,"filter":{"user_id":%q},"event_type":"subscriptions.subscribe","payload":{"source":{"data":{"id":%q,"username":"subscriber_user","name":"Subscriber User"}},"target":{"data":{"id":%q,"username":"creator_user","name":"Creator User"}}}}}`,
		uuid, createdAt, xActMe, sourceID, targetID))
}

func broadcastEnvelope(uuid, createdAt, typ, state, userID string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"event_uuid":%q,"created_at":%q,"filter":{"user_id":%q},"event_type":%q,"tag":"example","payload":{"user":{"data":{"id":%q,"name":"Example Broadcaster","username":"example_broadcaster"}},"broadcast_id":"1a2b3c4d5e6fG","event_timestamp_ms":1720000003500,"title":"Example broadcast","broadcast_state":%q,"language":"en","available_for_replay":false}}}`,
		uuid, createdAt, xActMe, typ, userID, state))
}

// xActSink is a Receiver that also takes activity, as the runner's sink does.
type xActSink struct {
	recordingSink
	amu    sync.Mutex
	events []activity.Event
}

func (s *xActSink) Activity(ev activity.Event) {
	s.amu.Lock()
	s.events = append(s.events, ev)
	s.amu.Unlock()
}

func (s *xActSink) got() []activity.Event {
	s.amu.Lock()
	defer s.amu.Unlock()
	return append([]activity.Event(nil), s.events...)
}

func (s *xActSink) wait(t *testing.T, n int) []activity.Event {
	t.Helper()
	xWaitFor(t, fmt.Sprintf("%d activity event(s)", n), func() bool { return len(s.got()) >= n })
	return s.got()
}

// settle gives a delivery that should NOT produce an event time to be wrongly produced, by feeding a chat line
// behind it: the inbox is FIFO and one goroutine drains it, so once the chat line is out, everything before it has
// been handled.
func settle(t *testing.T, a *XAdapter, sink *xActSink, uuid string) {
	t.Helper()
	before := len(sink.recordingSink.messages)
	if got := a.IngestWebhook(chatEnvelope(uuid, xActMe, "B", "m-"+uuid, "marker")); got != XIngestQueued {
		t.Fatalf("marker chat = %v", got)
	}
	xWaitFor(t, "the marker chat line", func() bool {
		sink.recordingSink.mu.Lock()
		defer sink.recordingSink.mu.Unlock()
		return len(sink.recordingSink.messages) > before
	})
}

// actAdapter builds the stub's adapter on a clock the test moves.
func actAdapter(t *testing.T, st *xChatStub, start time.Time, log *slog.Logger) (*XAdapter, *atomic.Int64) {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(start.UnixNano())
	a, err := NewX(XConfig{
		AccountRef: xActMe, Channel: "creator", Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, Log: log, WebhookRecheck: time.Hour,
		Now: func() time.Time { return time.Unix(0, clock.Load()).UTC() },
	})
	if err != nil {
		t.Fatalf("NewX: %v", err)
	}
	return a, clock
}

func xKindState(t *testing.T, a *XAdapter, k activity.Kind) activity.KindStatus {
	t.Helper()
	for _, ks := range a.ActivityKinds() {
		if ks.Kind == k {
			return ks
		}
	}
	t.Fatalf("no status for kind %s in %+v", k, a.ActivityKinds())
	return activity.KindStatus{}
}

func TestXActivitySubscribesToTheFourEventsOnlyWhenTheSinkTakesActivity(t *testing.T) {
	// A plain chat sink: exactly the two chat subscriptions, as before.
	st := newXChatStub(t)
	xRun(t, st.adapter(t, xActNow), SinkFunc(func(Message) {}))
	if n := len(st.subList()); n != 2 {
		t.Fatalf("chat-only sink made %d subscriptions, want 2", n)
	}

	st = newXChatStub(t)
	a, _ := actAdapter(t, st, xActNow, nil)
	xRun(t, a, &xActSink{})
	subs := st.subList()
	if len(subs) != 6 {
		t.Fatalf("subscriptions = %+v, want chat, chat.remove and the four activity events", subs)
	}
	dir := map[string]string{}
	for _, s := range subs {
		f := s["filter"].(map[string]any)
		d, _ := f["direction"].(string)
		dir[fmt.Sprint(s["event_type"])] = d
		if s["tag"] != "polyemesis" || f["user_id"] != xActMe {
			t.Errorf("subscription %+v lacks the tag or the user id", s)
		}
	}
	want := map[string]string{
		"broadcast.chat": "", "broadcast.chat.remove": "",
		"follow.follow": "inbound", "subscriptions.subscribe": "inbound",
		"broadcast.start": "", "broadcast.end": "",
	}
	for ev, d := range want {
		if got, ok := dir[ev]; !ok || got != d {
			t.Errorf("event %s direction = %q (present %v), want %q", ev, got, ok, d)
		}
	}
	if k := xKindState(t, a, activity.KindFollow); k.State != activity.KindOn {
		t.Errorf("follow = %+v, want on", k)
	}
	if k := xKindState(t, a, activity.KindStream); k.State != activity.KindOn {
		t.Errorf("stream = %+v, want on", k)
	}
}

func TestXActivityFollowReachesTheSinkAndDedupesOnTheDeliveryID(t *testing.T) {
	st := newXChatStub(t)
	a, clock := actAdapter(t, st, xActNow, nil)
	sink := &xActSink{}
	xRun(t, a, sink)
	clock.Store(time.Date(2026, 10, 7, 19, 17, 40, 0, time.UTC).UnixNano())

	body := followEnvelope("-7953119945885597316", xActCreated, xActMe, xActViewer, xActMe)
	if got := a.IngestWebhook(body); got != XIngestQueued {
		t.Fatalf("IngestWebhook = %v, want queued", got)
	}
	evs := sink.wait(t, 1)
	e := evs[0]
	if e.Kind != activity.KindFollow || e.Platform != "x" || e.Account != xActMe {
		t.Errorf("event = %+v", e)
	}
	if e.Actor == nil || e.Actor.ID != xActViewer || e.Actor.Login != "ExampleUser" || e.Actor.Name != "Example User" {
		t.Errorf("actor = %+v", e.Actor)
	}
	if e.Source.Type != "follow.follow" || e.Source.ID != "-7953119945885597316" {
		t.Errorf("source = %+v, want the event_uuid as the dedupe id", e.Source)
	}
	if want := time.Date(2026, 10, 7, 19, 17, 36, 304000000, time.UTC); !e.At.Equal(want) {
		t.Errorf("At = %v, want created_at", e.At)
	}
	if e.Message != "" || e.Amount != nil {
		t.Errorf("a follow carries no text and no amount: %+v", e)
	}
	if err := e.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	// X retries the same delivery: after it was delivered, and again as a fresh queue entry.
	if got := a.IngestWebhook(body); got != XIngestDuplicate {
		t.Errorf("redelivery = %v, want duplicate", got)
	}
	settle(t, a, sink, "marker-1")
	if n := len(sink.got()); n != 1 {
		t.Errorf("%d activity events after a redelivery, want 1", n)
	}
}

func TestXActivityOnlyInboundFollowsAndSubscriptionsAreAlerts(t *testing.T) {
	st := newXChatStub(t)
	a, clock := actAdapter(t, st, xActNow, nil)
	sink := &xActSink{}
	xRun(t, a, sink)
	clock.Store(time.Date(2026, 10, 7, 19, 17, 40, 0, time.UTC).UnixNano())

	// The broadcaster follows someone: source is them, target is another user. Even if X delivered it (the
	// direction filter is X's to honour), it is not an alert.
	a.IngestWebhook(followEnvelope("out-1", xActCreated, xActMe, xActMe, xActViewer))
	a.IngestWebhook(subscribeEnvelope("out-2", xActCreated, xActMe, xActViewer))
	settle(t, a, sink, "m1")
	if n := len(sink.got()); n != 0 {
		t.Fatalf("outbound deliveries became %d alert(s): %+v", n, sink.got())
	}
	a.act.mu.Lock()
	n := a.act.notInbound
	a.act.mu.Unlock()
	if n != 2 {
		t.Errorf("notInbound = %d, want 2", n)
	}

	// An inbound one right after still goes through.
	a.IngestWebhook(followEnvelope("in-1", xActCreated, xActMe, xActViewer, xActMe))
	sink.wait(t, 1)
}

func TestXActivitySubscriptionIsPremiumWithNoInventedAmount(t *testing.T) {
	st := newXChatStub(t)
	a, clock := actAdapter(t, st, xActNow, nil)
	sink := &xActSink{}
	xRun(t, a, sink)
	clock.Store(time.Date(2026, 10, 7, 19, 17, 40, 0, time.UTC).UnixNano())

	k := xKindState(t, a, activity.KindSubscribe)
	if k.State != activity.KindUnknown || k.Detail == "" {
		t.Fatalf("subscribe before any event = %+v, want unknown with the reason (Creator Subscriptions cannot be read)", k)
	}
	a.IngestWebhook(subscribeEnvelope("1093427633808192000", xActCreated, xActViewer, xActMe))
	e := sink.wait(t, 1)[0]
	if e.Kind != activity.KindSubscribe || e.Product != "premium" || e.Amount != nil || e.Tier != "" {
		t.Errorf("event = %+v, want a premium subscribe with no amount and no tier (X sends neither)", e)
	}
	if e.Actor == nil || e.Actor.Name != "Subscriber User" || e.Actor.Login != "subscriber_user" {
		t.Errorf("actor = %+v", e.Actor)
	}
	if err := e.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if k := xKindState(t, a, activity.KindSubscribe); k.State != activity.KindOn {
		t.Errorf("subscribe after one arrived = %+v, want on", k)
	}
}

func TestXActivityBroadcastStartAndEndAreStreamPhases(t *testing.T) {
	st := newXChatStub(t)
	a, clock := actAdapter(t, st, xActNow, nil)
	sink := &xActSink{}
	xRun(t, a, sink)
	clock.Store(time.Date(2026, 10, 7, 19, 17, 40, 0, time.UTC).UnixNano())

	a.IngestWebhook(broadcastEnvelope("b-1", xActCreated, "broadcast.start", "Running", xActMe))
	a.IngestWebhook(broadcastEnvelope("b-2", "2026-10-07T19:17:38.000Z", "broadcast.end", "Ended", xActMe))
	evs := sink.wait(t, 2)
	if evs[0].Kind != activity.KindStream || evs[0].Phase != "online" || evs[0].Title != "Example broadcast" || evs[0].Actor != nil {
		t.Errorf("start = %+v", evs[0])
	}
	if evs[1].Phase != "offline" || evs[1].Source.Type != "broadcast.end" {
		t.Errorf("end = %+v", evs[1])
	}
	for _, e := range evs {
		if err := e.Validate(); err != nil {
			t.Errorf("Validate: %v", err)
		}
	}
	// Another user's broadcast on the same webhook is not this account's.
	a.IngestWebhook(broadcastEnvelope("b-3", xActCreated, "broadcast.start", "Running", "555"))
	settle(t, a, sink, "m")
	if n := len(sink.got()); n != 2 {
		t.Errorf("a stranger's broadcast became an activity: %d events", n)
	}
}

// TestXActivityNeverReplaysHistory: a restart builds a new adapter whose memory is empty, and X redelivers what it
// could not deliver while the process was down. Nothing created before the adapter existed is shown, and nothing
// older than the age limit either.
func TestXActivityNeverReplaysHistory(t *testing.T) {
	st := newXChatStub(t)
	restart := time.Date(2026, 10, 7, 19, 30, 0, 0, time.UTC)
	a, clock := actAdapter(t, st, restart, nil)
	sink := &xActSink{}
	xRun(t, a, sink)

	// Delivered right after the restart, but created at 19:17: before this adapter existed.
	clock.Store(restart.Add(time.Second).UnixNano())
	a.IngestWebhook(followEnvelope("replayed-1", xActCreated, xActMe, xActViewer, xActMe))
	a.IngestWebhook(broadcastEnvelope("replayed-2", xActCreated, "broadcast.start", "Running", xActMe))
	settle(t, a, sink, "m1")
	if n := len(sink.got()); n != 0 {
		t.Fatalf("a restart replayed %d event(s): %+v", n, sink.got())
	}
	// Created after the start: a live event.
	a.IngestWebhook(followEnvelope("live-1", "2026-10-07T19:30:00.500Z", xActMe, xActViewer, xActMe))
	sink.wait(t, 1)

	// Older than the age limit, though after the start: an hour into the run, a redelivery of a 20 minute old event.
	clock.Store(restart.Add(time.Hour).UnixNano())
	a.IngestWebhook(followEnvelope("late-1", restart.Add(40*time.Minute).Format(time.RFC3339Nano), xActMe, xActViewer, xActMe))
	settle(t, a, sink, "m2")
	// No usable created_at: the floor and age limit cannot be checked, so it is stale, not stamped now.
	a.IngestWebhook(followEnvelope("nodate-1", "", xActMe, xActViewer, xActMe))
	a.IngestWebhook(followEnvelope("baddate-1", "not-a-time", xActMe, xActViewer, xActMe))
	settle(t, a, sink, "m3")
	if n := len(sink.got()); n != 1 {
		t.Errorf("events = %d, want only the live one", n)
	}
	a.act.mu.Lock()
	stale := a.act.stale
	a.act.mu.Unlock()
	if stale != 5 {
		t.Errorf("stale = %d, want 5", stale)
	}
}

// TestXActivityTierGatedKindsAreUnavailableNotErrors: X refuses an event for this account. Chat still registers, Run
// stays live and does not error, and the kind says why.
func TestXActivityTierGatedKindsAreUnavailableNotErrors(t *testing.T) {
	st := newXChatStub(t)
	st.refuseEvent = map[string]xStubRefusal{
		"subscriptions.subscribe": {status: 403, body: `{"title":"Forbidden","detail":"This account is not eligible for subscription events","type":"about:blank","status":403}`},
	}
	a, clock := actAdapter(t, st, xActNow, nil)
	sink := &xActSink{}
	stop := xRun(t, a, sink)
	clock.Store(time.Date(2026, 10, 7, 19, 17, 40, 0, time.UTC).UnixNano())

	k := xKindState(t, a, activity.KindSubscribe)
	if k.State != activity.KindUnavailable || !strings.Contains(k.Detail, "not eligible") {
		t.Errorf("subscribe = %+v, want unavailable with X's words", k)
	}
	if k := xKindState(t, a, activity.KindFollow); k.State != activity.KindOn {
		t.Errorf("follow = %+v, want on: one refused kind does not take the others down", k)
	}
	if h := a.Health(); h.State != StateLive || !strings.Contains(h.Detail, "Premium subscription alerts are off") {
		t.Errorf("Health = %+v, want live with the remark", h)
	}
	// Chat still flows.
	a.IngestWebhook(chatEnvelope("c1", xActMe, "B", "2090000000000000004", "hello"))
	xWaitFor(t, "chat", func() bool {
		sink.recordingSink.mu.Lock()
		defer sink.recordingSink.mu.Unlock()
		return len(sink.recordingSink.messages) == 1
	})
	if err := stop(); err != nil {
		t.Errorf("Run = %v, want nil: an unavailable kind is not a failure", err)
	}
}

// TestXActivityMissingScopeAsksForAReconnect: a token minted before follows.read was added (scope version 2) is good
// for follows and broadcasts but not for Premium subscriptions.
func TestXActivityMissingScopeAsksForAReconnect(t *testing.T) {
	st := newXChatStub(t)
	st.refuseEvent = map[string]xStubRefusal{
		"subscriptions.subscribe": {status: 403, body: `{"title":"Forbidden","detail":"Token is missing the follows.read scope","type":"https://api.x.com/2/problems/missing-scopes","status":403}`},
	}
	a, _ := actAdapter(t, st, xActNow, nil)
	stop := xRun(t, a, &xActSink{})
	k := xKindState(t, a, activity.KindSubscribe)
	if k.State != activity.KindReconnect || !strings.Contains(k.Detail, "reconnect") {
		t.Errorf("subscribe = %+v, want reconnect", k)
	}
	if k := xKindState(t, a, activity.KindFollow); k.State != activity.KindOn {
		t.Errorf("follow = %+v, want on", k)
	}
	if err := stop(); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

// TestXActivityTransientRefusalIsRetriedAtTheNextCheck: a 5xx says nothing about the account, so the kind is off
// for now and made again at the periodic check.
func TestXActivityTransientRefusalIsRetriedAtTheNextCheck(t *testing.T) {
	st := newXChatStub(t)
	st.refuseEvent = map[string]xStubRefusal{"broadcast.start": {status: 503, body: `{"title":"Service Unavailable"}`, times: 1}}
	a, err := NewX(XConfig{AccountRef: xActMe, Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, WebhookRecheck: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	xRun(t, a, &xActSink{})
	xWaitFor(t, "stream to come on after the retry", func() bool {
		return xKindState(t, a, activity.KindStream).State == activity.KindOn
	})
	if n := st.countCalls("POST /2/activity/subscriptions"); n != 7 {
		t.Errorf("creates = %d, want 7 (six, and the one that failed made again)", n)
	}
}

// TestXActivityMalformedPayloadIsDroppedAndNeverLogged: an event that cannot be read is counted and its body stays
// out of the log. Run goes on.
func TestXActivityMalformedPayloadIsDroppedAndNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := newXChatStub(t)
	a, clock := actAdapter(t, st, xActNow, log)
	sink := &xActSink{}
	xRun(t, a, sink)
	clock.Store(time.Date(2026, 10, 7, 19, 17, 40, 0, time.UTC).UnixNano())

	for i, payload := range []string{
		`"a string, not an object"`,
		`{"source":{"data":{"id":"","name":"SECRET-VIEWER-NAME"}},"target":{"data":{"id":"` + xActMe + `"}}}`,
		`{"source":{"data":{"id":"5"}},"target":{"data":{"id":"` + xActMe + `"}}}`, // no name at all
		`{}`,
	} {
		body := fmt.Sprintf(`{"data":{"event_uuid":"bad-%d","created_at":%q,"filter":{"user_id":%q},"event_type":"follow.follow","payload":%s}}`, i, xActCreated, xActMe, payload)
		if got := a.IngestWebhook([]byte(body)); got != XIngestQueued {
			t.Fatalf("payload %d = %v, want queued (it is a well-formed envelope)", i, got)
		}
	}
	settle(t, a, sink, "m")
	if n := len(sink.got()); n != 0 {
		t.Fatalf("malformed payloads produced %d event(s): %+v", n, sink.got())
	}
	a.act.mu.Lock()
	n := a.act.malformed
	a.act.mu.Unlock()
	if n != 4 {
		t.Errorf("malformed = %d, want 4", n)
	}
	if strings.Contains(logs.String(), "SECRET-VIEWER-NAME") || strings.Contains(logs.String(), "a string, not") {
		t.Errorf("the log carries payload text: %s", logs.String())
	}
	// And a good one right after is unaffected.
	a.IngestWebhook(followEnvelope("good", xActCreated, xActMe, xActViewer, xActMe))
	sink.wait(t, 1)
}

// TestXActivityReconnectKeepsSubscriptionsAndDedupe: a Run that ends and starts again reuses the subscriptions X
// still holds, makes no new ones, and a delivery it already showed is not shown again.
func TestXActivityReconnectKeepsSubscriptionsAndDedupe(t *testing.T) {
	st := newXChatStub(t)
	a, clock := actAdapter(t, st, xActNow, nil)
	sink := &xActSink{}
	ctxStop := xRun(t, a, sink)
	clock.Store(time.Date(2026, 10, 7, 19, 17, 40, 0, time.UTC).UnixNano())
	a.IngestWebhook(followEnvelope("f-1", xActCreated, xActMe, xActViewer, xActMe))
	sink.wait(t, 1)
	if err := ctxStop(); err != nil {
		t.Fatal(err)
	}
	if len(st.subList()) != 6 {
		t.Fatalf("subscriptions after stop = %d, want 6 left for the next run", len(st.subList()))
	}
	if got := a.ActivityKinds()[0].State; got != activity.KindOff {
		t.Errorf("follow after the run ended = %s, want off", got)
	}
	creates := st.countCalls("POST /2/activity/subscriptions")

	xRun(t, a, sink)
	if n := st.countCalls("POST /2/activity/subscriptions"); n != creates {
		t.Errorf("the reconnect created %d new subscription(s)", n-creates)
	}
	if k := xKindState(t, a, activity.KindFollow); k.State != activity.KindOn {
		t.Errorf("follow after the reconnect = %+v, want on", k)
	}
	// X redelivers f-1 after the reconnect (a retry that crossed the restart): known, so not shown twice.
	if got := a.IngestWebhook(followEnvelope("f-1", xActCreated, xActMe, xActViewer, xActMe)); got != XIngestDuplicate {
		t.Errorf("redelivery after reconnect = %v, want duplicate", got)
	}
	a.IngestWebhook(followEnvelope("f-2", xActCreated, xActMe, "111", xActMe))
	sink.wait(t, 2)
	settle(t, a, sink, "m")
	if n := len(sink.got()); n != 2 {
		t.Errorf("events = %d, want 2", n)
	}
}

// TestXActivityReplacesASubscriptionWithoutTheInboundFilter: ours by tag, same event, but made without the direction
// the alert needs: it would deliver outbound follows too, and would keep the right one from being made.
func TestXActivityReplacesASubscriptionWithoutTheInboundFilter(t *testing.T) {
	st := newXChatStub(t)
	st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: true}}
	st.subs = []map[string]any{
		{"subscription_id": "8001", "event_type": "follow.follow", "filter": map[string]any{"user_id": xActMe}, "webhook_id": "56", "tag": "polyemesis"},
		{"subscription_id": "8002", "event_type": "broadcast.start", "filter": map[string]any{"user_id": xActMe}, "webhook_id": "56", "tag": "polyemesis"},
	}
	a, _ := actAdapter(t, st, xActNow, nil)
	xRun(t, a, &xActSink{})
	if n := st.countCalls("DELETE /2/activity/subscriptions/8001"); n != 1 {
		t.Errorf("the direction-less follow subscription was deleted %d time(s), want 1", n)
	}
	if n := st.countCalls("DELETE /2/activity/subscriptions/8002"); n != 0 {
		t.Errorf("a matching broadcast.start subscription was deleted")
	}
	found := false
	for _, s := range st.subList() {
		if s["event_type"] == "follow.follow" && s["filter"].(map[string]any)["direction"] == "inbound" {
			found = true
		}
	}
	if !found {
		t.Errorf("no inbound follow subscription afterwards: %+v", st.subList())
	}
}

func TestXActivityIsIgnoredWhenTheSinkTakesNone(t *testing.T) {
	st := newXChatStub(t)
	a, _ := actAdapter(t, st, xActNow, nil)
	xRun(t, a, SinkFunc(func(Message) {}))
	if got := a.IngestWebhook(followEnvelope("f", xActCreated, xActMe, xActViewer, xActMe)); got != XIngestIgnored {
		t.Errorf("follow into a chat-only run = %v, want ignored", got)
	}
	if k := xKindState(t, a, activity.KindFollow); k.State != activity.KindOff {
		t.Errorf("follow = %+v, want off", k)
	}
}

func TestXActivityPayloadsAreJSONTheDocsShow(t *testing.T) {
	// Guards the fixtures above: each must be valid JSON in the documented envelope.
	for name, b := range map[string][]byte{
		"follow":    followEnvelope("1", xActCreated, xActMe, "2", xActMe),
		"subscribe": subscribeEnvelope("1", xActCreated, "2", xActMe),
		"start":     broadcastEnvelope("1", xActCreated, "broadcast.start", "Running", xActMe),
	} {
		if !json.Valid(b) {
			t.Errorf("%s fixture is not JSON", name)
		}
		if _, ok := parseXEnvelope(b); !ok {
			t.Errorf("%s fixture is not an envelope", name)
		}
	}
}

// TestXActivityRefusedKindIsNotAskedAgainAtEveryCheck: X's refusal for this account does not change in an hour, so
// the periodic check leaves it alone instead of asking X again every time.
func TestXActivityRefusedKindIsNotAskedAgainAtEveryCheck(t *testing.T) {
	st := newXChatStub(t)
	st.refuseEvent = map[string]xStubRefusal{
		"subscriptions.subscribe": {status: 403, body: `{"title":"Forbidden","detail":"not eligible","type":"about:blank","status":403}`},
	}
	a, err := NewX(XConfig{AccountRef: xActMe, Token: StaticToken("user-token"), AppBearer: "app-bearer",
		APIBase: st.srv.URL, WebhookURL: xStubHookURL, WebhookRecheck: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	xRun(t, a, &xActSink{})
	xWaitFor(t, "several checks", func() bool { return st.countCalls("GET /2/activity/subscriptions") >= 6 })
	n := 0
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, "POST /2/activity/subscriptions") && strings.Contains(c, "subscriptions.subscribe") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("subscriptions.subscribe was asked for %d times, want once", n)
	}
}

// TestXActivityRateLimitedCreateNeverStopsChat: chat is registered before the activity events are asked for, so a 429
// on one of them must leave chat running, not end the run until the reset.
func TestXActivityRateLimitedCreateNeverStopsChat(t *testing.T) {
	st := newXChatStub(t)
	st.refuseEvent = map[string]xStubRefusal{
		"follow.follow": {status: 429, body: `{"title":"Too Many Requests","detail":"Too Many Requests","type":"about:blank","status":429}`},
	}
	a, clock := actAdapter(t, st, xActNow, nil)
	sink := &xActSink{}
	stop := xRun(t, a, sink)
	clock.Store(time.Date(2026, 10, 7, 19, 17, 40, 0, time.UTC).UnixNano())

	if k := xKindState(t, a, activity.KindFollow); k.State != activity.KindOff || k.Detail == "" {
		t.Errorf("follow = %+v, want off with a reason", k)
	}
	if h := a.Health(); h.State != StateLive {
		t.Errorf("Health = %+v, want live", h)
	}
	if got := a.IngestWebhook(chatEnvelope("c1", xActMe, "B", "2090000000000000004", "still here")); got != XIngestQueued {
		t.Fatalf("chat after the rate limit = %v, want queued", got)
	}
	xWaitFor(t, "chat", func() bool {
		sink.recordingSink.mu.Lock()
		defer sink.recordingSink.mu.Unlock()
		return len(sink.recordingSink.messages) == 1
	})
	if err := stop(); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

// TestXActivityListedDirectionMayBeUnderQualifiers: X documents the qualifier as equivalent to filter.direction, so a
// listed subscription is recognised by either and is not deleted and made again at every run.
func TestXActivityListedDirectionMayBeUnderQualifiers(t *testing.T) {
	st := newXChatStub(t)
	st.webhooks = []xStubWebhook{{ID: "56", URL: xStubHookURL, Valid: true}}
	q := func(id, ev string) map[string]any {
		return map[string]any{"subscription_id": id, "event_type": ev, "webhook_id": "56", "tag": "polyemesis",
			"filter": map[string]any{"user_id": xActMe, "qualifiers": map[string]any{"direction": "inbound"}}}
	}
	st.subs = []map[string]any{q("1", "follow.follow"), q("2", "subscriptions.subscribe")}
	a, _ := actAdapter(t, st, xActNow, nil)
	xRun(t, a, &xActSink{})
	if n := st.countCalls("DELETE /2/activity/subscriptions"); n != 0 {
		t.Errorf("%d subscription(s) deleted although they carry the inbound qualifier", n)
	}
	for _, c := range st.recorded() {
		if strings.HasPrefix(c, "POST /2/activity/subscriptions") && (strings.Contains(c, "follow.follow") || strings.Contains(c, "subscriptions.subscribe")) {
			t.Errorf("recreated an existing subscription: %s", c)
		}
	}
}

// TestXActivityReachesAHubIngesterThroughTheRealRunnerSink is the one end-to-end check: the sink Run is handed in
// production is the runner's, and the activity it takes must land in the hub's ingester, stamped.
func TestXActivityReachesAHubIngesterThroughTheRealRunnerSink(t *testing.T) {
	st := newXChatStub(t)
	a, clock := actAdapter(t, st, xActNow, nil)
	h := testHub(t)
	rec := &ingestRecorder{}
	h.SetActivity(rec)
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	xWaitFor(t, "the adapter to register", a.registered)
	if n := len(st.subList()); n != 6 {
		t.Fatalf("subscriptions through the hub = %d, want 6: the runner's sink must carry the activity capability", n)
	}
	clock.Store(time.Date(2026, 10, 7, 19, 17, 40, 0, time.UTC).UnixNano())
	a.IngestWebhook(followEnvelope("hub-1", xActCreated, xActMe, xActViewer, xActMe))
	xWaitFor(t, "the ingester to receive the follow", func() bool { return len(rec.events()) == 1 })
	if e := rec.events()[0]; e.Kind != activity.KindFollow || e.Platform != "x" || e.Account != xActMe {
		t.Errorf("event = %+v", e)
	}
}
