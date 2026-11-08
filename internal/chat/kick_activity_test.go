package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

// actSink is a Sink that CAN take activity. SinkFunc cannot, which makes
// emitActivity a silent no-op: a test using it would pass vacuously.
type actSink struct {
	mu   sync.Mutex
	acts []activity.Event
	msgs []Message
}

func (s *actSink) Deliver(m Message) { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() }
func (s *actSink) Activity(ev activity.Event) {
	s.mu.Lock()
	s.acts = append(s.acts, ev)
	s.mu.Unlock()
}
func (s *actSink) events() []activity.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]activity.Event(nil), s.acts...)
}

// The payloads are the examples on docs.kick.com/events/event-types (checked
// 2026-11-07), with the timestamps moved to just before the test clock
// (2026-03-01T12:00:00Z).
const (
	kickBroadcasterJSON = `{"is_anonymous":false,"user_id":99,"username":"broadcaster_name","is_verified":true,"profile_picture":"https://example.com/broadcaster_avatar.jpg","channel_slug":"broadcaster_channel","identity":null}`

	kickFollowedJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"follower":{"is_anonymous":false,"user_id":987654321,"username":"follower_name","is_verified":false,"profile_picture":"https://example.com/sender_avatar.jpg","channel_slug":"follower_channel","identity":null}}`

	kickSubNewJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"subscriber":{"is_anonymous":false,"user_id":987654321,"username":"subscriber_name","is_verified":false,"profile_picture":"https://example.com/sender_avatar.jpg","channel_slug":"subscriber_channel","identity":null},"duration":1,"created_at":"2026-03-01T11:58:00Z","expires_at":"2026-04-01T11:58:00Z"}`

	kickSubRenewalJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"subscriber":{"is_anonymous":false,"user_id":987654321,"username":"subscriber_name","is_verified":false,"profile_picture":"https://example.com/sender_avatar.jpg","channel_slug":"subscriber_channel","identity":null},"duration":3,"created_at":"2026-03-01T11:58:00Z","expires_at":"2026-04-01T11:58:00Z"}`

	kickSubGiftsJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"gifter":{"is_anonymous":false,"user_id":987654321,"username":"gifter_name","is_verified":false,"profile_picture":"https://example.com/sender_avatar.jpg","channel_slug":"gifter_channel","identity":null},"giftees":[{"is_anonymous":false,"user_id":561654654,"username":"giftee_name","is_verified":true,"profile_picture":"https://example.com/broadcaster_avatar.jpg","channel_slug":"giftee_channel","identity":null},{"is_anonymous":false,"user_id":561654655,"username":"giftee_two","is_verified":true,"profile_picture":"","channel_slug":"giftee_two","identity":null}],"created_at":"2026-03-01T11:58:00Z","expires_at":"2026-04-01T11:58:00Z"}`

	kickAnonGiftJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"gifter":{"is_anonymous":true,"user_id":null,"username":null,"is_verified":null,"profile_picture":null,"channel_slug":null,"identity":null},"giftees":[{"is_anonymous":false,"user_id":561654654,"username":"giftee_name","is_verified":true,"profile_picture":"","channel_slug":"giftee_channel","identity":null}],"created_at":"2026-03-01T11:58:00Z","expires_at":"2026-04-01T11:58:00Z"}`

	kicksGiftedJSON = `{"broadcaster":{"user_id":99,"username":"broadcaster_name","is_verified":true,"profile_picture":"https://example.com/broadcaster_avatar.jpg","channel_slug":"broadcaster_channel"},"sender":{"user_id":987654321,"username":"gift_sender","is_verified":false,"profile_picture":"https://example.com/sender_avatar.jpg","channel_slug":"gift_sender_channel"},"gift":{"amount":500,"name":"Rage Quit","type":"LEVEL_UP","tier":"MID","message":"w","pinned_time_seconds":600},"created_at":"2026-03-01T11:58:00.634Z"}`

	kickLiveJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"is_live":true,"title":"Stream Title","started_at":"2026-03-01T11:00:00+11:00","ended_at":null}`

	kickOfflineJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"is_live":false,"title":"Stream Title","started_at":"2026-03-01T11:00:00+11:00","ended_at":"2026-03-01T11:59:00+00:00"}`

	kickMetadataJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"metadata":{"title":"New Title","language":"en","has_mature_content":true,"category":{"id":123,"name":"Category name","thumbnail":"http://example.com/image123"}}}`

	kickBannedJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"moderator":{"is_anonymous":false,"user_id":987654321,"username":"moderator_name","is_verified":false,"profile_picture":"","channel_slug":"moderator_channel","identity":null},"banned_user":{"is_anonymous":false,"user_id":135790135,"username":"banned_user_name","is_verified":false,"profile_picture":"","channel_slug":"banned_user_channel","identity":null},"metadata":{"reason":"banned reason","created_at":"2026-03-01T11:58:05Z","expires_at":"2026-03-01T12:10:06Z"}}`

	kickBannedPermJSON = `{"broadcaster":` + kickBroadcasterJSON + `,"moderator":{"is_anonymous":false,"user_id":987654321,"username":"moderator_name","channel_slug":"moderator_channel"},"banned_user":{"is_anonymous":false,"user_id":135790135,"username":"banned_user_name","channel_slug":"banned_user_channel"},"metadata":{"reason":"banned reason","created_at":"2026-03-01T11:58:05Z","expires_at":null}}`

	kickRedemptionJSON = `{"id":"01KBHE78QE4HZY1617DK5FC7YD","user_input":"unban me","status":"pending","redeemed_at":"2026-03-01T11:59:19.323Z","reward":{"id":"01KBHE7RZNHB0SKDV1H86CD4F3","title":"Uban Request","cost":1000,"description":"Only good reasons pls"},"redeemer":{"user_id":123,"username":"naughty-user","is_verified":false,"profile_picture":"","channel_slug":"naughty_user"},"broadcaster":{"user_id":99,"username":"gigachad","is_verified":true,"profile_picture":"","channel_slug":"gigachad"}}`
)

// kickActRig is an attached adapter behind its real Handler.
type kickActRig struct {
	t    *testing.T
	a    *KickAdapter
	sink *actSink
	srv  *httptest.Server
	stop func()
}

func newKickActRig(t *testing.T, opts func(*KickConfig)) *kickActRig {
	t.Helper()
	f := &fakeKickEvents{}
	api := httptest.NewServer(f)
	t.Cleanup(api.Close)
	return newKickActRigAgainst(t, f, api.URL, opts)
}

func newKickActRigAgainst(t *testing.T, f *fakeKickEvents, apiURL string, opts func(*KickConfig)) *kickActRig {
	t.Helper()
	a := kickAdapter(t, func(c *KickConfig) {
		c.APIBase = apiURL
		c.Tick = 5 * time.Millisecond
		if opts != nil {
			opts(c)
		}
	})
	r := &kickActRig{t: t, a: a, sink: &actSink{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(ctx, r.sink) }()
	r.stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return")
		}
	}
	t.Cleanup(r.stop)
	waitFor(t, "the sink to register", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.sink != nil })
	r.srv = httptest.NewServer(a.Handler())
	t.Cleanup(r.srv.Close)
	return r
}

func (r *kickActRig) post(eventType, id, ts, body string) int {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL, strings.NewReader(body))
	req.Header.Set("Kick-Event-Type", eventType)
	req.Header.Set("Kick-Event-Version", "1")
	signKickAt(r.t, testKickKey(r.t), req, []byte(body), id, ts)
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

const kickTS = "2026-03-01T12:00:00Z"

func TestKickActivityDecodesEveryDocumentedPayload(t *testing.T) {
	r := newKickActRig(t, nil)
	tests := []struct {
		name, event, body string
		check             func(t *testing.T, e activity.Event)
	}{
		{"follow", "channel.followed", kickFollowedJSON, func(t *testing.T, e activity.Event) {
			if e.Kind != activity.KindFollow || e.Actor.Name != "follower_name" || e.Actor.Login != "follower_channel" || e.Actor.ID != "987654321" {
				t.Errorf("%+v %+v", e, e.Actor)
			}
			// No timestamp in the payload: the signed header's is used.
			if !e.At.Equal(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)) {
				t.Errorf("at = %v", e.At)
			}
		}},
		{"subscribe", "channel.subscription.new", kickSubNewJSON, func(t *testing.T, e activity.Event) {
			if e.Kind != activity.KindSubscribe || e.Actor.Name != "subscriber_name" || e.Product != "sub" || e.Months != 0 {
				t.Errorf("%+v", e)
			}
		}},
		{"resub", "channel.subscription.renewal", kickSubRenewalJSON, func(t *testing.T, e activity.Event) {
			if e.Kind != activity.KindResub || e.Months != 3 {
				t.Errorf("%+v", e)
			}
		}},
		{"gift", "channel.subscription.gifts", kickSubGiftsJSON, func(t *testing.T, e activity.Event) {
			if e.Kind != activity.KindGift || e.Count != 2 || len(e.Recipients) != 2 || e.Recipients[1].Name != "giftee_two" || e.Actor.Name != "gifter_name" {
				t.Errorf("%+v", e)
			}
		}},
		{"anonymous gift", "channel.subscription.gifts", kickAnonGiftJSON, func(t *testing.T, e activity.Event) {
			if !e.Actor.Anonymous || e.Actor.Name != "" || e.Count != 1 {
				t.Errorf("%+v %+v", e, e.Actor)
			}
		}},
		{"kicks", "kicks.gifted", kicksGiftedJSON, func(t *testing.T, e activity.Event) {
			if e.Kind != activity.KindTip || e.Amount == nil || e.Amount.Unit != "kicks" || e.Amount.Units != 500 ||
				e.Amount.Currency != "" || e.Amount.Minor != 0 {
				t.Errorf("a KICK amount must stay a unit, never money: %+v", e.Amount)
			}
			if e.Message != "w" || e.Actor.Name != "gift_sender" || e.Title != "Rage Quit" || e.Tier != "MID" {
				t.Errorf("%+v", e)
			}
		}},
		{"online", "livestream.status.updated", kickLiveJSON, func(t *testing.T, e activity.Event) {
			if e.Kind != activity.KindStream || e.Phase != "online" || e.Title != "Stream Title" {
				t.Errorf("%+v", e)
			}
		}},
		{"offline", "livestream.status.updated", kickOfflineJSON, func(t *testing.T, e activity.Event) {
			if e.Phase != "offline" {
				t.Errorf("%+v", e)
			}
		}},
		{"metadata", "livestream.metadata.updated", kickMetadataJSON, func(t *testing.T, e activity.Event) {
			if e.Phase != "update" || e.Title != "New Title" {
				t.Errorf("%+v", e)
			}
		}},
		{"timeout", "moderation.banned", kickBannedJSON, func(t *testing.T, e activity.Event) {
			if e.Kind != activity.KindModeration || e.Action != "timeout" || e.Other.Name != "banned_user_name" ||
				e.Actor.Name != "moderator_name" || e.Message != "banned reason" {
				t.Errorf("%+v", e)
			}
		}},
		{"permanent ban", "moderation.banned", kickBannedPermJSON, func(t *testing.T, e activity.Event) {
			if e.Action != "ban" {
				t.Errorf("%+v", e)
			}
		}},
		{"redemption", "channel.reward.redemption.updated", kickRedemptionJSON, func(t *testing.T, e activity.Event) {
			if e.Kind != activity.KindRedemption || e.Title != "Uban Request" || e.Status != "pending" ||
				e.Message != "unban me" || e.Actor.Name != "naughty-user" || e.Source.ID != "01KBHE78QE4HZY1617DK5FC7YD" {
				t.Errorf("%+v", e)
			}
		}},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(r.sink.events())
			id := "01MSG" + string(rune('A'+i))
			if code := r.post(tc.event, id, kickTS, tc.body); code != http.StatusOK {
				t.Fatalf("status = %d", code)
			}
			waitFor(t, "the activity", func() bool { return len(r.sink.events()) == before+1 })
			e := r.sink.events()[before]
			if e.Platform != "kick" || e.Account != "99" || e.Source.Type != tc.event {
				t.Errorf("identity: %+v", e)
			}
			if tc.event != "channel.reward.redemption.updated" && e.Source.ID != id {
				t.Errorf("source id = %q, want the Kick-Event-Message-Id %q", e.Source.ID, id)
			}
			if err := e.Validate(); err != nil {
				t.Errorf("not valid: %v", err)
			}
			tc.check(t, e)
		})
	}
	// None of it was counted as an unreadable chat message.
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	if r.a.unparseable != 0 || r.a.delivered != 0 {
		t.Errorf("activity leaked into the chat counters: unparseable=%d delivered=%d", r.a.unparseable, r.a.delivered)
	}
}

// Kick retries a delivery it thinks failed, with the same Message-Id. The
// verifier in this test is the bare signature check, so the adapter's own
// dedupe is what is under test.
func TestKickActivityDedupesOnTheMessageID(t *testing.T) {
	r := newKickActRig(t, nil)
	for i := 0; i < 3; i++ {
		if code := r.post("channel.followed", "01SAME", kickTS, kickFollowedJSON); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
	}
	r.post("channel.followed", "01OTHER", kickTS, kickFollowedJSON)
	waitFor(t, "two distinct deliveries", func() bool { return len(r.sink.events()) == 2 })
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	if r.a.act.deduped != 2 {
		t.Errorf("deduped = %d, want 2", r.a.act.deduped)
	}
}

// One redemption fires .updated again when it is accepted or rejected, each
// time with a new delivery id. It is announced once: the redemption's own id is
// the identity the hub dedupes on.
func TestKickRedemptionStatusChangesShareOneIdentity(t *testing.T) {
	r := newKickActRig(t, nil)
	r.post("channel.reward.redemption.updated", "01A", kickTS, kickRedemptionJSON)
	r.post("channel.reward.redemption.updated", "01B", kickTS, strings.Replace(kickRedemptionJSON, `"pending"`, `"accepted"`, 1))
	waitFor(t, "both deliveries", func() bool { return len(r.sink.events()) == 2 })
	evs := r.sink.events()
	if evs[0].Key() != evs[1].Key() {
		t.Errorf("keys differ, so the hub would announce one redemption twice: %q vs %q", evs[0].Key(), evs[1].Key())
	}
}

func TestKickActivityMalformedPayloadsAreCountedNotEmitted(t *testing.T) {
	r := newKickActRig(t, nil)
	bad := []struct{ event, body string }{
		{"channel.followed", "{{{"},
		{"channel.followed", `{"broadcaster":{"user_id":99},"follower":{}}`},
		{"channel.subscription.renewal", strings.Replace(kickSubRenewalJSON, `"duration":3,`, "", 1)},
		{"channel.subscription.gifts", strings.Replace(kickSubGiftsJSON, "giftees", "gifteez", 1)},
		{"kicks.gifted", strings.Replace(kicksGiftedJSON, `"amount":500`, `"amount":0`, 1)},
		{"livestream.status.updated", `{"broadcaster":{"user_id":99}}`},
		{"channel.reward.redemption.updated", strings.Replace(kickRedemptionJSON, `"id":"01KBHE78QE4HZY1617DK5FC7YD",`, "", 1)},
	}
	for i, b := range bad {
		if code := r.post(b.event, "01BAD"+string(rune('A'+i)), kickTS, b.body); code != http.StatusOK {
			t.Fatalf("%s: status %d; a malformed body must still be acknowledged", b.event, code)
		}
	}
	waitFor(t, "all counted", func() bool { r.a.mu.Lock(); defer r.a.mu.Unlock(); return r.a.act.malformed == int64(len(bad)) })
	if n := len(r.sink.events()); n != 0 {
		t.Fatalf("%d events emitted from malformed payloads", n)
	}
	// The adapter is still healthy: nothing here is chat.
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	if r.a.unparseable != 0 {
		t.Errorf("unparseable = %d", r.a.unparseable)
	}
}

// A restart builds a new adapter with an empty seen-set. It must not fetch or
// announce anything on its own, and a late retry of something that happened
// long ago is acknowledged and dropped.
func TestKickActivityRestartDoesNotReplayHistory(t *testing.T) {
	f := &fakeKickEvents{}
	api := httptest.NewServer(f)
	defer api.Close()

	first := newKickActRigAgainst(t, f, api.URL, nil)
	first.post("channel.followed", "01X", kickTS, kickFollowedJSON)
	waitFor(t, "the first adapter's follow", func() bool { return len(first.sink.events()) == 1 })
	first.stop()

	second := newKickActRigAgainst(t, f, api.URL, nil)
	waitFor(t, "the second adapter to finish its first check", func() bool { ok, _, _ := recorded(second.a); return ok })
	time.Sleep(30 * time.Millisecond)
	if n := len(second.sink.events()); n != 0 {
		t.Fatalf("a restart emitted %d activities by itself", n)
	}
	f.mu.Lock()
	for _, r := range f.requests {
		if !strings.Contains(r, "/events/subscriptions") {
			t.Errorf("a restart called %q; it should only check its subscriptions", r)
		}
	}
	f.mu.Unlock()

	// Hours old by its own timestamp: acknowledged, not announced.
	old := strings.Replace(kickSubNewJSON, "2026-03-01T11:58:00Z", "2026-03-01T08:00:00Z", 1)
	if code := second.post("channel.subscription.new", "01OLD", kickTS, old); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	waitFor(t, "the stale delivery to be ignored", func() bool { second.a.mu.Lock(); defer second.a.mu.Unlock(); return second.a.act.ignored == 1 })
	if n := len(second.sink.events()); n != 0 {
		t.Fatalf("a stale delivery was announced (%d)", n)
	}
}

// A delivery for another channel on the same app is not this account's.
func TestKickActivityForAnotherBroadcasterIsIgnored(t *testing.T) {
	r := newKickActRig(t, nil)
	other := strings.Replace(kickFollowedJSON, `"user_id":99`, `"user_id":12345`, 1)
	r.post("channel.followed", "01Z", kickTS, other)
	waitFor(t, "ignored", func() bool { r.a.mu.Lock(); defer r.a.mu.Unlock(); return r.a.act.ignored == 1 })
	if len(r.sink.events()) != 0 {
		t.Fatal("another channel's follow was announced")
	}
}

// Activity flows only while chat is attached (D4): a delivery with no sink is
// counted as beforeAttach and dropped, and a retry after attach is not deduped
// away by the earlier drop.
func TestKickActivityBeforeAttachIsDroppedNotRemembered(t *testing.T) {
	a := kickAdapter(t, nil)
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	post := func() {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(kickFollowedJSON))
		req.Header.Set("Kick-Event-Type", "channel.followed")
		signKickAt(t, testKickKey(t), req, []byte(kickFollowedJSON), "01Q", kickTS)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	post()
	a.mu.Lock()
	if a.beforeAttach != 1 {
		t.Errorf("beforeAttach = %d", a.beforeAttach)
	}
	a.sink = &actSink{}
	a.mu.Unlock()
	post()
	if n := len(a.sink.(*actSink).events()); n != 1 {
		t.Fatalf("events after attach = %d, want 1", n)
	}
}

// ----------------------------------------------------------- subscribing

func kindState(t *testing.T, a *KickAdapter, k activity.Kind) activity.KindStatus {
	t.Helper()
	for _, ks := range a.ActivityKinds() {
		if ks.Kind == k {
			return ks
		}
	}
	t.Fatalf("no status for %s", k)
	return activity.KindStatus{}
}

func TestKickRunSubscribesToEveryActivityEventOnceAndReportsThemOn(t *testing.T) {
	f := &fakeKickEvents{}
	api := httptest.NewServer(f)
	defer api.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = api.URL; c.Tick = 5 * time.Millisecond })
	if got := kindState(t, a, activity.KindFollow); got.State != activity.KindUnknown {
		t.Errorf("before the first check a kind is %q, want unknown", got.State)
	}
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "the activity POST", func() bool { return f.count("POST-ACT ") == 1 })
	waitFor(t, "all on", func() bool { return kindState(t, a, activity.KindModeration).State == activity.KindOn })

	f.mu.Lock()
	post := f.actPosts[0]
	f.mu.Unlock()
	var names []string
	for _, e := range post["events"].([]any) {
		m := e.(map[string]any)
		names = append(names, m["name"].(string))
		if m["version"].(float64) != 1 {
			t.Errorf("%v is not version 1", m)
		}
	}
	want := []string{"channel.followed", "channel.subscription.new", "channel.subscription.renewal", "channel.subscription.gifts",
		"kicks.gifted", "livestream.status.updated", "livestream.metadata.updated", "moderation.banned", "channel.reward.redemption.updated"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("requested %v", names)
	}
	for _, ks := range a.ActivityKinds() {
		if ks.State != activity.KindOn {
			t.Errorf("%s = %s (%s)", ks.Kind, ks.State, ks.Detail)
		}
	}
	if st := a.Health().State; st != StateLive {
		t.Errorf("chat health = %s", st)
	}
}

// Reconnect (a second Run, here a second adapter) finds the subscriptions in
// Kick's listing and creates nothing.
func TestKickRunAgainDoesNotRePostActivitySubscriptions(t *testing.T) {
	f := &fakeKickEvents{}
	api := httptest.NewServer(f)
	defer api.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = api.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	waitFor(t, "first activity POST", func() bool { return f.count("POST-ACT ") == 1 })
	stop()

	b := kickAdapter(t, func(c *KickConfig) { c.APIBase = api.URL; c.Tick = 5 * time.Millisecond })
	stopB := runKick(t, b)
	defer stopB()
	waitFor(t, "second adapter live", func() bool { return b.Health().State == StateLive })
	waitFor(t, "second adapter reports on", func() bool { return kindState(t, b, activity.KindFollow).State == activity.KindOn })
	if n := f.count("POST-ACT "); n != 1 {
		t.Fatalf("%d activity POSTs after a reconnect, want 1", n)
	}
	if n := f.count("POST "); n != 1 {
		t.Fatalf("%d chat POSTs after a reconnect, want 1", n)
	}
}

// Tier-gated: Kick says no to one event for this account. That kind is
// unavailable in Kick's words; chat stays live and every other kind stays on.
func TestKickTierGatedEventIsUnavailableNotAnError(t *testing.T) {
	f := &fakeKickEvents{refuse: map[string]string{"kicks.gifted": "KICKs are not enabled for this channel"}}
	api := httptest.NewServer(f)
	defer api.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = api.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "tip unavailable", func() bool { return kindState(t, a, activity.KindTip).State == activity.KindUnavailable })
	tip := kindState(t, a, activity.KindTip)
	if !strings.Contains(tip.Detail, "KICKs are not enabled for this channel") {
		t.Errorf("detail %q is not in Kick's words", tip.Detail)
	}
	for _, ks := range a.ActivityKinds() {
		if ks.Kind != activity.KindTip && ks.State != activity.KindOn {
			t.Errorf("%s = %s", ks.Kind, ks.State)
		}
	}
	waitFor(t, "chat live", func() bool { return a.Health().State == StateLive })
	if _, _, p := recorded(a); p != "" {
		t.Errorf("an unavailable activity kind became a chat problem: %q", p)
	}
	// Not asked again at the next half-hourly check.
	posts := f.count("POST-ACT ")
	time.Sleep(40 * time.Millisecond)
	if f.count("POST-ACT ") != posts {
		t.Error("an unavailable event is being re-requested on every tick")
	}
}

// A whole-request 400 caused by one activity event must neither cost chat its
// subscription nor cost the other activity events theirs.
func TestKickRefusedActivityBatchNeverCostsChatOrTheOtherEvents(t *testing.T) {
	f := &fakeKickEvents{reject4xx: map[string]string{"channel.reward.redemption.updated": "event is not available"}}
	api := httptest.NewServer(f)
	defer api.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = api.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "redemption unavailable", func() bool { return kindState(t, a, activity.KindRedemption).State == activity.KindUnavailable })
	waitFor(t, "follow on", func() bool { return kindState(t, a, activity.KindFollow).State == activity.KindOn })
	if got := kindState(t, a, activity.KindRedemption); !strings.Contains(got.Detail, "event is not available") {
		t.Errorf("detail = %q", got.Detail)
	}
	waitFor(t, "chat live", func() bool { return a.Health().State == StateLive })
	if n := f.count("POST "); n != 1 {
		t.Errorf("%d chat POSTs", n)
	}
	for _, ks := range a.ActivityKinds() {
		if ks.Kind != activity.KindRedemption && ks.State != activity.KindOn {
			t.Errorf("%s = %s (%s)", ks.Kind, ks.State, ks.Detail)
		}
	}
}

// Stream is fed by two events: on when either is, with the other named.
func TestKickStreamKindAggregatesItsTwoEvents(t *testing.T) {
	f := &fakeKickEvents{refuse: map[string]string{"livestream.metadata.updated": "nope"}}
	api := httptest.NewServer(f)
	defer api.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = api.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "stream on", func() bool { return kindState(t, a, activity.KindStream).State == activity.KindOn })
	if d := kindState(t, a, activity.KindStream).Detail; !strings.Contains(d, "livestream.metadata.updated") {
		t.Errorf("detail %q does not name the declined event", d)
	}
}

// While chat itself cannot subscribe (webhooks off), the activity POST is not
// even tried and the kinds are unknown, not unavailable.
func TestKickActivityWaitsWhileChatCannotSubscribe(t *testing.T) {
	f := &fakeKickEvents{postStatus: http.StatusForbidden, postBody: `{"message":"webhooks are not enabled for this app"}`}
	api := httptest.NewServer(f)
	defer api.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = api.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "degraded", func() bool { return a.Health().State == StateDegraded })
	if n := f.count("POST-ACT "); n != 0 {
		t.Errorf("%d activity POSTs while chat is refused", n)
	}
	if got := kindState(t, a, activity.KindFollow); got.State != activity.KindUnknown {
		t.Errorf("follow = %s", got.State)
	}
}

// Existing subscriptions from before activity existed (chat only) are topped up
// without touching chat.
func TestKickUpgradeFromChatOnlyAddsActivityWithoutRePostingChat(t *testing.T) {
	f := &fakeKickEvents{subs: []map[string]any{
		{"id": "s1", "event": "chat.message.sent", "version": 1, "method": "webhook", "broadcaster_user_id": 99},
	}}
	api := httptest.NewServer(f)
	defer api.Close()
	a := kickAdapter(t, func(c *KickConfig) { c.APIBase = api.URL; c.Tick = 5 * time.Millisecond })
	stop := runKick(t, a)
	defer stop()
	waitFor(t, "activity POST", func() bool { return f.count("POST-ACT ") == 1 })
	if n := f.count("POST "); n != 0 {
		t.Fatalf("chat re-posted %d times", n)
	}
}

// internal/api's chatActivityStatuses finds an adapter's kinds through this
// method set; losing it would silently drop Kick from GET /api/v1/activity/sources.
var _ interface{ ActivityKinds() []activity.KindStatus } = (*KickAdapter)(nil)
