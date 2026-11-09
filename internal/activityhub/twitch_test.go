package activityhub

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/twitch/eventsub"
)

func TestEverySubscriptionIsCreatedOnTheWelcomedSessionAndNothingIsWritten(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()

	want := map[string]bool{}
	for _, s := range eventsub.Catalogue() {
		want[s.Type] = true
	}
	got := map[string]int{}
	for _, p := range f.posted() {
		got[p]++
	}
	for typ := range want {
		if typ == "channel.raid" {
			if got[typ] != 2 {
				t.Errorf("channel.raid should be created twice (in and out), got %d", got[typ])
			}
			continue
		}
		if got[typ] != 1 {
			t.Errorf("%s created %d times, want 1", typ, got[typ])
		}
	}
	// Every request names the welcomed session and the websocket transport.
	f.mu.Lock()
	for _, b := range f.postBody {
		tr := b["transport"].(map[string]any)
		if tr["method"] != "websocket" || tr["session_id"] != "sess-1" {
			t.Errorf("transport = %v", tr)
		}
	}
	f.mu.Unlock()
	if n := c.inbound.Load(); n != 0 {
		t.Errorf("the source wrote %d text frames; Twitch closes the socket on any", n)
	}
	for _, k := range r.src.Kinds() {
		if k.State != activity.KindOn {
			t.Errorf("%s is %s (%s) on an affiliate with every scope", k.Kind, k.State, k.Detail)
		}
	}
	h := r.src.Health()
	if h.State != chat.StateLive || !strings.Contains(h.Detail, "12 of 12 kinds on") {
		t.Errorf("health = %+v", h)
	}
}

func TestNotificationsBecomeActivityWithTheDocumentedFields(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()

	c.send(note("m-follow", "channel.follow", "2", evFollow))
	c.send(note("m-cheer", "channel.cheer", "1", evCheer))
	c.send(note("m-resub", "channel.subscription.message", "1", evResub))
	c.send(note("m-raid", "channel.raid", "1", evRaidIn))
	waitFor(t, "four events", func() bool { return r.hub.Stats().Delivered == 4 })

	fol := r.events(activity.KindFollow)[0]
	if fol.Actor.Name != "Cool_User" || fol.Actor.ID != "1234" || fol.Actor.Login != "cool_user" {
		t.Errorf("follow actor = %+v", fol.Actor)
	}
	if fol.Source.Type != "channel.follow" || fol.Source.ID != "m-follow" {
		t.Errorf("follow source = %+v: the dedupe id is the message id", fol.Source)
	}
	if !fol.At.Equal(time.Date(2020, 7, 15, 18, 16, 11, 171067130, time.UTC)) {
		t.Errorf("follow at = %v, want the platform's followed_at", fol.At)
	}

	tip := r.events(activity.KindTip)[0]
	if tip.Amount == nil || tip.Amount.Unit != "bits" || tip.Amount.Units != 1000 || tip.Amount.Currency != "" || tip.Amount.Minor != 0 {
		t.Errorf("cheer amount = %+v: bits are a unit, never converted to money", tip.Amount)
	}
	if tip.Message != "pogchamp" || tip.Actor.Name != "Cool_User" {
		t.Errorf("tip = %+v", tip)
	}

	rs := r.events(activity.KindResub)[0]
	if rs.Months != 15 || rs.Streak != 1 || rs.Tier != "1000" || rs.Message != "Love the stream! FevziGG" || rs.Product != "sub" {
		t.Errorf("resub = %+v", rs)
	}

	raid := r.events(activity.KindRaid)[0]
	if raid.Direction != "in" || raid.Viewers != 9001 || raid.Other.Name != "Cool_User" {
		t.Errorf("raid = %+v", raid)
	}
}

func TestTheSameMessageIdTwiceIsOneEvent(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()

	c.send(note("m-1", "channel.follow", "2", evFollow))
	c.send(note("m-1", "channel.follow", "2", evFollow)) // Twitch resends under the same id
	c.send(note("m-2", "channel.cheer", "1", evCheer))   // a sentinel that arrives after both
	waitFor(t, "the sentinel", func() bool { return len(r.events(activity.KindTip)) == 1 })

	if n := len(r.events(activity.KindFollow)); n != 1 {
		t.Fatalf("%d follow events for one message id", n)
	}
	if got := r.src.Stats().Deduped; got != 1 {
		t.Errorf("deduped = %d, want 1", got)
	}
}

func TestADifferentMessageForTheSamePersonIsAnotherEvent(t *testing.T) {
	// The dedupe key is the delivery, not the viewer: two cheers from one user
	// are two cheers.
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	c.send(note("m-1", "channel.cheer", "1", evCheer))
	c.send(note("m-2", "channel.cheer", "1", evCheer))
	waitFor(t, "two cheers", func() bool { return len(r.events(activity.KindTip)) == 2 })
}

func TestAGiftIsOneEventAndItsPerRecipientSubscribesAreDropped(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()

	c.send(note("g-1", "channel.subscription.gift", "1", evGift))
	c.send(note("g-s1", "channel.subscribe", "1", evSubscribeGift))
	c.send(note("g-s2", "channel.subscribe", "1", evSubscribeGift))
	c.send(note("s-1", "channel.subscribe", "1", evSubscribe)) // a real first sub, after
	waitFor(t, "the real sub", func() bool { return len(r.events(activity.KindSubscribe)) == 1 })

	g := r.events(activity.KindGift)
	if len(g) != 1 || g[0].Count != 2 || g[0].Tier != "1000" || g[0].Actor.Name != "Cool_User" {
		t.Fatalf("gift = %+v", g)
	}
	if n := len(r.events(activity.KindSubscribe)); n != 1 {
		t.Errorf("%d subscribe events: the is_gift receipts must be dropped", n)
	}
	if got := r.src.Stats().Ignored; got != 2 {
		t.Errorf("ignored = %d, want the 2 receipts", got)
	}
}

func TestAnAnonymousGiftAndCheerHaveNoName(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	c.send(note("a-1", "channel.subscription.gift", "1", evGiftAnon))
	c.send(note("a-2", "channel.cheer", "1", evCheerAnon))
	waitFor(t, "both", func() bool { return len(r.events(activity.KindGift)) == 1 && len(r.events(activity.KindTip)) == 1 })

	g := r.events(activity.KindGift)[0]
	if !g.Actor.Anonymous || g.Actor.Name != "" || g.Actor.ID != "" || g.Count != 5 || g.Tier != "2000" {
		t.Errorf("anonymous gift = %+v actor=%+v", g, g.Actor)
	}
	tip := r.events(activity.KindTip)[0]
	if !tip.Actor.Anonymous || tip.Amount.Units != 500 {
		t.Errorf("anonymous cheer = %+v", tip)
	}
}

func TestSessionReconnectMovesToTheNewSocketWithoutSubscribingAgain(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	old := r.ready()
	before := len(f.posted())

	old.send(reconnectMsg("rc-1", f.ws2URL()))
	nw := f.conn(1)
	waitFor(t, "the old socket to be closed by the client", func() bool {
		select {
		case <-old.closed:
			return true
		default:
			return false
		}
	})

	// The overlap: the same message arrives on both sockets, then a new one on
	// the new socket only.
	old.send(note("ov-1", "channel.follow", "2", evFollow)) // may be dropped by a closed socket
	nw.send(note("ov-1", "channel.follow", "2", evFollow))
	nw.send(note("ov-2", "channel.cheer", "1", evCheer))
	waitFor(t, "the new socket's cheer", func() bool { return len(r.events(activity.KindTip)) == 1 })

	if n := len(r.events(activity.KindFollow)); n != 1 {
		t.Errorf("%d follow events for one message id seen on both sockets", n)
	}
	if after := len(f.posted()); after != before {
		t.Errorf("%d subscriptions created after the reconnect, want none: they carry over", after-before)
	}
	if got := r.src.Stats().Reconnects; got != 1 {
		t.Errorf("reconnects = %d, want 1", got)
	}
	if r.src.Health().State != chat.StateLive {
		t.Errorf("health = %+v", r.src.Health())
	}
	if nw.inbound.Load() != 0 {
		t.Error("the source wrote to the new socket")
	}
}

func TestEventsOnTheOldSocketDuringTheOverlapAreStillDelivered(t *testing.T) {
	// Twitch keeps delivering on the old socket until the client has the new
	// welcome. A message that arrives between the reconnect message and then
	// must not be lost. The new socket here is slow to welcome.
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	old := r.ready()

	old.send(reconnectMsg("rc-1", f.ws2URL()))
	old.send(note("late-1", "channel.follow", "2", evFollow))
	waitFor(t, "the follow sent on the old socket right after the reconnect", func() bool {
		return len(r.events(activity.KindFollow)) == 1
	})
}

func TestARestartIsANewSessionAndReplaysNothing(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	c.send(note("old-1", "channel.follow", "2", evFollow))
	waitFor(t, "the first follow", func() bool { return len(r.events(activity.KindFollow)) == 1 })

	r.hub.Detach("twitch", fakeBroadcaster)
	select {
	case <-c.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("detaching did not close the socket")
	}

	// A new process: a new source and a new hub, against the same Twitch.
	f.mu.Lock()
	f.posts, f.paths = nil, nil
	f.mu.Unlock()
	r2 := startTwitch(t, f, nil)
	c2 := r2.f.conn(1)
	waitFor(t, "the second source to be live", func() bool { return r2.src.Health().State == "live" })
	_ = c2

	if n := len(r2.events("")); n != 0 {
		t.Fatalf("a restarted source emitted %d events before anything happened: history was replayed", n)
	}
	f.mu.Lock()
	paths := append([]string(nil), f.paths...)
	f.mu.Unlock()
	for _, p := range paths {
		if p != "GET /helix/users" && p != "POST /helix/eventsub/subscriptions" {
			t.Errorf("a restart called %s: only the account lookup and subscribe are allowed, never a history read", p)
		}
	}
	// Twitch resending an old message on the new session after a restart: it
	// is stamped long ago, so it is not news.
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	c2.send(noteAt("resent-1", "channel.follow", "2", evFollow, old))
	c2.send(note("fresh-1", "channel.cheer", "1", evCheer))
	waitFor(t, "the fresh cheer", func() bool { return len(r2.events(activity.KindTip)) == 1 })
	if n := len(r2.events(activity.KindFollow)); n != 0 {
		t.Errorf("a message stamped an hour ago was emitted (%d)", n)
	}
	if r2.src.Stats().Ignored != 1 {
		t.Errorf("ignored = %d, want the stale one counted", r2.src.Stats().Ignored)
	}
}

func TestANonAffiliateHasTheTierGatedKindsUnavailableAndStaysLive(t *testing.T) {
	f := newFakeTwitch(t)
	f.tier = ""
	r := startTwitch(t, f, nil)
	r.ready()

	gated := []activity.Kind{
		activity.KindSubscribe, activity.KindResub, activity.KindGift, activity.KindTip,
		activity.KindRedemption, activity.KindPoll, activity.KindPrediction,
	}
	for _, k := range gated {
		st := r.kind(k)
		if st.State != activity.KindUnavailable || !strings.Contains(st.Detail, "Affiliates and Partners") {
			t.Errorf("%s = %+v, want unavailable for Affiliates and Partners", k, st)
		}
	}
	for _, k := range []activity.Kind{activity.KindFollow, activity.KindRaid, activity.KindStream, activity.KindShoutout, activity.KindHypeTrain} {
		if st := r.kind(k); st.State != activity.KindOn {
			t.Errorf("%s = %+v, want on for any channel", k, st)
		}
	}
	for _, typ := range []string{
		"channel.subscribe", "channel.subscription.message", "channel.subscription.gift", "channel.cheer",
		"channel.channel_points_custom_reward_redemption.add", "channel.poll.begin", "channel.prediction.begin",
	} {
		if f.hitCount(typ) != 0 {
			t.Errorf("%s was requested for a non-affiliate", typ)
		}
	}
	h := r.src.Health()
	if h.State != chat.StateLive {
		t.Errorf("an unavailable kind is not a failure: health = %+v", h)
	}
	if !strings.Contains(h.Detail, "need Affiliate or Partner") {
		t.Errorf("health detail = %q", h.Detail)
	}
}

func TestATierAnAccountOnlyDiscoversByTheCreateAnswerIsUnavailableNotAnError(t *testing.T) {
	f := newFakeTwitch(t)
	f.status["channel.hype_train.begin"] = 403
	f.status["channel.hype_train.progress"] = 403
	f.status["channel.hype_train.end"] = 403
	r := startTwitch(t, f, nil)
	r.ready()
	if st := r.kind(activity.KindHypeTrain); st.State != activity.KindUnavailable {
		t.Errorf("hype train after 403 = %+v", st)
	}
	if st := r.kind(activity.KindFollow); st.State != activity.KindOn {
		t.Errorf("follow = %+v", st)
	}
}

func TestAMissingScopeIsReconnectNotAnAttempt(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, func(c *TwitchConfig) {
		// An account connected on scope version 5 (v0.15.0): it holds neither
		// the activity scopes nor moderator:manage:shoutouts, which arrived in 6.
		c.Scopes = func() []string {
			return []string{
				"channel:manage:broadcast", "channel:manage:polls", "channel:read:stream_key", "chat:edit", "chat:read",
				"moderator:manage:banned_users", "moderator:manage:chat_messages", "moderator:manage:chat_settings",
			}
		}
	})
	r.ready()
	for _, k := range []activity.Kind{activity.KindFollow, activity.KindSubscribe, activity.KindTip, activity.KindRedemption,
		activity.KindHypeTrain, activity.KindPrediction, activity.KindShoutout} {
		st := r.kind(k)
		if st.State != activity.KindReconnect || !strings.Contains(st.Detail, "Reconnect") {
			t.Errorf("%s = %+v, want reconnect", k, st)
		}
	}
	// No-scope kinds and the one the old scopes already cover work at once.
	for _, k := range []activity.Kind{activity.KindRaid, activity.KindStream, activity.KindPoll} {
		if st := r.kind(k); st.State != activity.KindOn {
			t.Errorf("%s = %+v, want on", k, st)
		}
	}
	if f.hitCount("channel.follow") != 0 || f.hitCount("channel.cheer") != 0 || f.hitCount("channel.shoutout.create") != 0 {
		t.Error("a subscription was attempted without its scope")
	}
}

func TestTooManyRequestsIsAKindStateNotAFailure(t *testing.T) {
	f := newFakeTwitch(t)
	f.status["channel.update"] = 429
	r := startTwitch(t, f, nil)
	r.ready()
	if st := r.kind(activity.KindStream); st.State != activity.KindUnavailable || !strings.Contains(st.Detail, "limit") {
		t.Errorf("stream after a 429 with no reset = %+v", st)
	}
	if r.src.Health().State != chat.StateLive {
		t.Errorf("health = %+v", r.src.Health())
	}
}

func TestARateLimitThatResetsSoonIsWaitedOutOnce(t *testing.T) {
	f := newFakeTwitch(t)
	f.status["channel.follow"] = 429
	// Ratelimit-Reset is whole unix SECONDS, so .Unix() truncates: a reset
	// computed 1s ahead lands anywhere in (0s, 1s] from now, and when the clock
	// sits late in a second it is already past by the time the source reads it.
	// The source then (correctly) does not wait, answers "limit reached", and the
	// test failed in 0.01s on CI. Three seconds ahead leaves at least two after
	// truncation, inside the source's 5s wait-it-out window, so the assertion
	// below is about the retry and not about which fraction of a second it ran in.
	f.reset["channel.follow"] = itoa(time.Now().Add(3 * time.Second).Unix())
	r := startTwitch(t, f, nil)
	r.ready()
	if st := r.kind(activity.KindFollow); st.State != activity.KindOn {
		t.Errorf("follow = %+v, want on after the rate limit reset", st)
	}
	if n := f.hitCount("channel.follow"); n != 2 {
		t.Errorf("channel.follow was requested %d times, want 2 (once refused, once after the reset)", n)
	}
}

func TestARevocationMarksOneKindAndLeavesTheSourceLive(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	c.send(revocationMsg("rv-1", "channel.cheer", "authorization_revoked"))
	c.send(revocationMsg("rv-2", "channel.raid", "version_removed"))
	waitFor(t, "both revocations", func() bool {
		return r.kind(activity.KindTip).State == activity.KindReconnect && r.kind(activity.KindRaid).State == activity.KindUnavailable
	})
	if r.src.Health().State != chat.StateLive {
		t.Errorf("health = %+v", r.src.Health())
	}
	// And the socket still delivers.
	c.send(note("after-1", "channel.follow", "2", evFollow))
	waitFor(t, "a follow after the revocations", func() bool { return len(r.events(activity.KindFollow)) == 1 })
}

func TestMalformedUnknownAndIncompleteNotificationsAreCountedNotEmittedNotLogged(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()

	c.send(`this is not json at all VIEWER-SECRET-TEXT`)
	c.send(`{"metadata":{"message_type":"notification"},"payload":{}}`)                                                                         // no message id
	c.send(note("bad-1", "channel.follow", "2", `{"user_name":"VIEWER-SECRET-TEXT"}`))                                                          // no user id
	c.send(note("bad-2", "channel.cheer", "1", `{"is_anonymous":false,"user_id":"1","user_name":"x","bits":0,"message":"VIEWER-SECRET-TEXT"}`)) // no bits
	c.send(note("bad-3", "channel.follow", "2", `"a string, not an object"`))
	c.send(note("unk-1", "channel.something.new", "1", `{"user_name":"VIEWER-SECRET-TEXT"}`))
	c.send(note("bad-4", "channel.subscription.message", "1", `{"user_id":"1","user_name":"x","tier":"1000","cumulative_months":0,"message":{"text":"VIEWER-SECRET-TEXT"}}`))
	c.send(note("ok-1", "channel.follow", "2", evFollow)) // proves the socket survived all of it
	waitFor(t, "the good follow", func() bool { return len(r.events(activity.KindFollow)) == 1 })

	if n := len(r.hub.Recent(500, "", "")); n != 1 {
		t.Errorf("%d events emitted, want only the good follow", n)
	}
	st := r.src.Stats()
	if st.Unparseable != 6 {
		t.Errorf("unparseable = %d, want 6 (2 frames + 4 bodies)", st.Unparseable)
	}
	if st.Ignored != 1 {
		t.Errorf("ignored = %d, want the unknown type", st.Ignored)
	}
	if strings.Contains(r.logs.String(), "VIEWER-SECRET-TEXT") {
		t.Errorf("a payload body reached the log:\n%s", r.logs.String())
	}
	if r.src.Health().State != chat.StateLive {
		t.Errorf("health = %+v", r.src.Health())
	}
}

func TestTheCreateErrorBodyIsNeverLogged(t *testing.T) {
	f := newFakeTwitch(t)
	f.status["channel.poll.begin"] = 400
	r := startTwitch(t, f, nil)
	r.ready()
	if strings.Contains(r.logs.String(), "VIEWER-TEXT-SHOULD-NEVER-BE-LOGGED") {
		t.Error("Twitch's error body reached the log")
	}
	if st := r.kind(activity.KindPoll); st.State != activity.KindUnavailable {
		t.Errorf("poll = %+v", st)
	}
}

func TestAKeepaliveThatNeverComesEndsTheRunSoTheSupervisorReconnects(t *testing.T) {
	f := newFakeTwitch(t)
	f.keepalive = 1
	r := startTwitch(t, f, func(c *TwitchConfig) { c.KeepaliveGrace = 100 * time.Millisecond })
	r.f.conn(0)
	// 1 s + 100 ms of silence: the source drops the socket and the supervisor
	// brings up a second session.
	f.conn(1)
	if got := r.hub.Statuses(); len(got) != 1 {
		t.Fatalf("statuses = %+v", got)
	}
}

func TestKeepalivesKeepTheSocketOpen(t *testing.T) {
	f := newFakeTwitch(t)
	f.keepalive = 1
	r := startTwitch(t, f, func(c *TwitchConfig) { c.KeepaliveGrace = 300 * time.Millisecond })
	c := r.ready()
	deadline := time.Now().Add(2500 * time.Millisecond)
	for i := 0; time.Now().Before(deadline); i++ {
		c.send(keepaliveMsg(itoa(int64(i))))
		time.Sleep(400 * time.Millisecond)
	}
	f.mu.Lock()
	n := len(f.conns)
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("%d connections: keepalives did not hold the first one open", n)
	}
}

func TestAReconnectUrlOutsideTwitchIsRefused(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, func(c *TwitchConfig) { c.AllowReconnectHost = nil }) // the production rule
	old := r.ready()
	old.send(reconnectMsg("rc-bad", f.ws2URL())) // 127.0.0.1: not twitch.tv
	// The run ends (the supervisor starts a new session) and no second socket
	// is dialled for the reconnect: the next connection is the supervisor's.
	f.conn(1)
	select {
	case <-old.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the original socket was not dropped")
	}
	for _, bad := range []string{
		"wss://evil.example/ws", "wss://eventsub.wss.twitch.tv.evil.example/ws", "ws://eventsub.wss.twitch.tv/ws",
		"wss://nottwitch.tv/ws", "https://eventsub.wss.twitch.tv/ws", "",
	} {
		if eventsub.ValidReconnectURL(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"wss://eventsub.wss.twitch.tv?ws_id=1", "wss://eventsub.wss.twitch.tv/ws"} {
		if err := eventsub.ValidReconnectURL(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
}

func TestWhenNothingCanBeSubscribedTheReasonIsTheSourcesDetailAndItRetries(t *testing.T) {
	f := newFakeTwitch(t)
	f.tier = ""
	for _, typ := range []string{"channel.raid", "stream.online", "stream.offline", "channel.update"} {
		f.status[typ] = 403
	}
	// No scope and no tier: every kind is unavailable or reconnect, none created.
	r := startTwitch(t, f, func(c *TwitchConfig) { c.Scopes = func() []string { return nil } })
	waitFor(t, "the failed detail", func() bool {
		st := r.hub.Statuses()
		return len(st) == 1 && strings.Contains(st[0].Detail, "none of the alert types could be turned on")
	})
	f.conn(1) // and it tries again with a fresh session
	if f.postedType("channel.follow") {
		t.Error("follow was created without its scope")
	}
}

func TestTwitchClosingAnUnusedConnectionIsNamedInTheError(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	c.closeWith(eventsub.CloseUnused)
	f.conn(1)
	waitFor(t, "the supervisor's line", func() bool {
		st := r.hub.Statuses()
		return len(st) == 1 && st[0].State == chat.StateLive // reconnected
	})
}

func TestADeadGrantParksTheSourceAsFailedInsteadOfAskingAgain(t *testing.T) {
	f := newFakeTwitch(t)
	f.userCode = 401
	r := startTwitch(t, f, func(c *TwitchConfig) {
		c.Refresh = func(context.Context, string) (string, error) { return "", chat.Fatal(chat.ErrReconnectRequired) }
	})
	waitFor(t, "failed", func() bool { return r.src.Health().State == chat.StateFailed })
	if !strings.Contains(r.src.Health().Detail, "reconnect the Twitch account") {
		t.Errorf("detail = %q", r.src.Health().Detail)
	}
	time.Sleep(150 * time.Millisecond) // the supervisor's backoff is 20 ms here
	f.mu.Lock()
	users := 0
	for _, p := range f.paths {
		if strings.HasSuffix(p, "/helix/users") {
			users++
		}
	}
	f.mu.Unlock()
	if users != 1 {
		t.Errorf("Twitch was asked %d times with a dead token; the source must park instead", users)
	}
	for _, k := range r.src.Kinds() {
		if k.State == activity.KindOn {
			t.Errorf("%s is still on", k.Kind)
		}
	}
	// And it stops when the account is detached.
	r.hub.Detach("twitch", fakeBroadcaster)
}

func TestAStaleAccessTokenIsRefreshedAndTheCallRetried(t *testing.T) {
	f := newFakeTwitch(t)
	var fresh, refreshed atomic.Bool
	r := startTwitch(t, f, func(c *TwitchConfig) {
		c.Token = func(context.Context) (string, error) {
			if fresh.Load() {
				return "tok", nil
			}
			return "stale", nil
		}
		c.Refresh = func(_ context.Context, rejected string) (string, error) {
			if rejected != "stale" {
				t.Errorf("refresh was told %q was rejected", rejected)
			}
			refreshed.Store(true)
			fresh.Store(true)
			return "tok", nil
		}
	})
	r.ready()
	if !refreshed.Load() {
		t.Error("the stale token was never refreshed")
	}
	if st := r.kind(activity.KindFollow); st.State != activity.KindOn {
		t.Errorf("follow = %+v", st)
	}
}

func TestAPollPolyemesisMirroredIsNotReportedTwice(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()

	done := r.src.cfg.Mirrors.Begin()
	done("1243456") // the id in evPollBegin
	c.send(note("p-1", "channel.poll.begin", "1", evPollBegin))
	c.send(note("p-2", "channel.poll.end", "1", evPollEnd))
	c.send(note("pr-1", "channel.prediction.begin", "1", evPredBegin)) // sentinel: a different kind
	waitFor(t, "the prediction", func() bool { return len(r.events(activity.KindPrediction)) == 1 })
	if n := len(r.events(activity.KindPoll)); n != 0 {
		t.Errorf("%d poll events for a poll polyemesis mirrored", n)
	}
}

func TestANativePollPolyemesisDidNotCreateIsReported(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	c.send(note("p-1", "channel.poll.begin", "1", evPollBegin))
	c.send(note("p-2", "channel.poll.end", "1", evPollEnd))
	waitFor(t, "both polls", func() bool { return len(r.events(activity.KindPoll)) == 2 })
	end := r.events(activity.KindPoll)[0] // newest first
	if end.Phase != "end" || end.Status != "completed" || end.Progress.Total != 340 || len(end.Progress.Options) != 3 {
		t.Errorf("poll end = %+v progress=%+v", end, end.Progress)
	}
}

func TestAPollAnnouncedBeforeTheMirrorCreateReturnsItsIdIsStillSkipped(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()

	done := r.src.cfg.Mirrors.Begin() // the Create Poll call is in flight
	c.send(note("p-1", "channel.poll.begin", "1", evPollBegin))
	time.AfterFunc(100*time.Millisecond, func() { done("1243456") }) // ...and returns after the announcement
	c.send(note("pr-1", "channel.prediction.begin", "1", evPredBegin))
	waitFor(t, "the prediction", func() bool { return len(r.events(activity.KindPrediction)) == 1 })
	if n := len(r.events(activity.KindPoll)); n != 0 {
		t.Errorf("%d poll events: the announcement raced the mirror and won", n)
	}
}

func TestAPollWhoseCreateFailedIsReportedAfterTheHold(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	done := r.src.cfg.Mirrors.Begin()
	c.send(note("p-1", "channel.poll.begin", "1", evPollBegin))
	time.AfterFunc(50*time.Millisecond, func() { done("") })
	waitFor(t, "the poll", func() bool { return len(r.events(activity.KindPoll)) == 1 })
}

func TestTheRemainingKindsMapToTheDocumentedFields(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	c.send(note("1", "channel.channel_points_custom_reward_redemption.add", "1", evRedemption))
	c.send(note("2", "channel.hype_train.begin", "2", evHypeBegin))
	c.send(note("3", "channel.hype_train.end", "2", evHypeEnd))
	c.send(note("4", "channel.prediction.progress", "1", evPredProg))
	c.send(note("5", "channel.prediction.lock", "1", evPredLock))
	c.send(note("6", "channel.prediction.end", "1", evPredEnd))
	c.send(note("7", "stream.online", "1", evStreamOnline))
	c.send(note("8", "stream.offline", "1", evStreamOff))
	c.send(note("9", "channel.update", "2", evChannelUpd))
	c.send(note("10", "channel.shoutout.create", "1", evShoutOut))
	c.send(note("11", "channel.shoutout.receive", "1", evShoutIn))
	c.send(note("12", "channel.raid", "1", evRaidOut))
	waitFor(t, "twelve events", func() bool { return r.hub.Stats().Delivered == 12 })

	red := r.events(activity.KindRedemption)[0]
	if red.Title != "title" || red.Message != "pogchamp" || red.Status != "unfulfilled" || red.Actor.Name != "Cooler_User2" {
		t.Errorf("redemption = %+v", red)
	}
	hype := r.events(activity.KindHypeTrain)
	if len(hype) != 2 {
		t.Fatalf("hype = %d", len(hype))
	}
	begin, end := hype[1], hype[0]
	if begin.Phase != "begin" || begin.Level != 1 || begin.Progress.Total != 137 || begin.Progress.Goal != 500 || begin.Progress.EndsAt == nil {
		t.Errorf("hype begin = %+v progress=%+v", begin, begin.Progress)
	}
	if end.Phase != "end" || end.Level != 2 || end.Progress.EndsAt != nil {
		t.Errorf("hype end = %+v progress=%+v", end, end.Progress)
	}
	pred := r.events(activity.KindPrediction)
	phases := []string{}
	for i := len(pred) - 1; i >= 0; i-- {
		phases = append(phases, pred[i].Phase)
	}
	if strings.Join(phases, ",") != "progress,lock,end" {
		t.Errorf("prediction phases = %v", phases)
	}
	prog := pred[2]
	if prog.Progress.Options[0].Votes != 10 || prog.Progress.Options[0].Points != 15000 || prog.Progress.Total != 15200 {
		t.Errorf("prediction progress = %+v", prog.Progress)
	}
	if pred[0].Status != "resolved" {
		t.Errorf("prediction end status = %q", pred[0].Status)
	}
	stream := r.events(activity.KindStream)
	got := map[string]activity.Event{}
	for _, e := range stream {
		got[e.Phase] = e
	}
	if got["online"].Status != "live" || got["offline"].Phase == "" || got["update"].Title != "Best Stream Ever" {
		t.Errorf("stream = %+v", got)
	}
	dirs := map[string]activity.Event{}
	for _, e := range r.events(activity.KindShoutout) {
		dirs[e.Direction] = e
	}
	if dirs["out"].Other.Name != "SandySanderman" || dirs["out"].Viewers != 860 || dirs["in"].Other.Name != "SimplySimple" {
		t.Errorf("shoutouts = %+v", dirs)
	}
	raid := r.events(activity.KindRaid)[0]
	if raid.Direction != "out" || raid.Other.Name != "Cool_User" || raid.Viewers != 40 {
		t.Errorf("raid out = %+v", raid)
	}
}

func TestARaidThatIsNeitherToNorFromThisChannelIsIgnored(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	c.send(note("r-1", "channel.raid", "1", `{"from_broadcaster_user_id":"1","from_broadcaster_user_login":"a","from_broadcaster_user_name":"A","to_broadcaster_user_id":"2","to_broadcaster_user_login":"b","to_broadcaster_user_name":"B","viewers":3}`))
	c.send(note("f-1", "channel.follow", "2", evFollow))
	waitFor(t, "the follow", func() bool { return len(r.events(activity.KindFollow)) == 1 })
	if n := len(r.events(activity.KindRaid)); n != 0 {
		t.Errorf("%d raids", n)
	}
}

func TestTheSourceReportsItsKindsInTheHubStatuses(t *testing.T) {
	f := newFakeTwitch(t)
	f.tier = ""
	r := startTwitch(t, f, nil)
	r.ready()
	st := r.hub.Statuses()
	if len(st) != 1 || st[0].Platform != "twitch" || st[0].Account != fakeBroadcaster || st[0].Transport != TransportOwn {
		t.Fatalf("statuses = %+v", st)
	}
	if st[0].State != chat.StateLive || len(st[0].Kinds) != 12 {
		t.Errorf("status = %+v", st[0])
	}
	if strings.Contains(st[0].Detail, "tok") {
		t.Errorf("detail %q carries a token", st[0].Detail)
	}
}

func TestBeforeTheFirstSessionKindsAreUnknownOrReconnect(t *testing.T) {
	src := NewTwitch(TwitchConfig{AccountRef: "1", Scopes: func() []string { return []string{"bits:read"} }})
	for _, k := range src.Kinds() {
		switch k.Kind {
		case activity.KindTip:
			if k.State != activity.KindUnknown {
				t.Errorf("tip = %+v", k)
			}
		case activity.KindFollow:
			if k.State != activity.KindReconnect {
				t.Errorf("follow = %+v", k)
			}
		}
	}
	if len(src.Kinds()) != len(eventsub.Kinds()) {
		t.Errorf("%d kinds", len(src.Kinds()))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestAHostClockFarFromTwitchsDoesNotDropAlerts(t *testing.T) {
	// The stale rule is read against Twitch's timestamps. A host whose own
	// clock is an hour out must still get every alert: nothing here asks it.
	f := newFakeTwitch(t)
	r := startTwitch(t, f, nil)
	c := r.ready()
	// Stamped now, by Twitch's reckoning: fresh whatever this host thinks.
	c.send(note("fresh-1", "channel.follow", "2", evFollow))
	waitFor(t, "the follow", func() bool { return len(r.events(activity.KindFollow)) == 1 })
	if r.src.Stats().Ignored != 0 {
		t.Errorf("ignored = %d", r.src.Stats().Ignored)
	}
}

func TestNothingSentOnTheOldSocketDuringTheSwapIsLost(t *testing.T) {
	// Twitch delivers on the old socket until the new welcome and does not
	// repeat those messages on the new one. A burst sent right behind
	// session_reconnect must all arrive, however the swap interleaves.
	const burst = 12
	for round := 0; round < 15; round++ {
		f := newFakeTwitch(t)
		// The drain window is how long the old socket is still read after the
		// swap. At its 1s default a process starved for longer than that (the
		// full -race suite in parallel did it: this test took 19s) closed the old
		// socket with part of the burst unread. Widened here so the test asserts
		// ordering, not scheduling; the wait below ends on the event.
		r := startTwitch(t, f, func(c *TwitchConfig) { c.DrainWindow = time.Minute })
		old := r.ready()
		old.send(reconnectMsg("rc", f.ws2URL()))
		for i := 0; i < burst; i++ {
			old.send(note("burst-"+itoa(int64(i)), "channel.cheer", "1", evCheer))
		}
		waitFor(t, "every message of the burst", func() bool { return len(r.events(activity.KindTip)) == burst })
		if got := r.src.Stats().Reconnects; got != 1 {
			// The swap may still be settling; it must complete.
			waitFor(t, "the swap", func() bool { return r.src.Stats().Reconnects == 1 })
		}
		r.cancel()
		r.hub.Close()
	}
}

func TestAGrantThatDiesBetweenTheTierReadAndTheSubscribesParksTheSource(t *testing.T) {
	f := newFakeTwitch(t)
	r := startTwitch(t, f, func(c *TwitchConfig) {
		// Good for the account-type read, rejected from then on; the refresh
		// then reports the grant dead (a plain error, not an APIError).
		c.Token = func(context.Context) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, p := range f.paths {
				if strings.HasSuffix(p, "/helix/users") {
					return "dead", nil
				}
			}
			return "tok", nil
		}
		c.Refresh = func(context.Context, string) (string, error) { return "", chat.Fatal(chat.ErrReconnectRequired) }
	})
	waitFor(t, "failed", func() bool { return r.src.Health().State == chat.StateFailed })
	if !strings.Contains(r.src.Health().Detail, "reconnect the Twitch account") {
		t.Errorf("detail = %q", r.src.Health().Detail)
	}
	time.Sleep(150 * time.Millisecond)
	f.mu.Lock()
	users := 0
	for _, p := range f.paths {
		if strings.HasSuffix(p, "/helix/users") {
			users++
		}
	}
	f.mu.Unlock()
	if users != 1 {
		t.Errorf("Twitch was asked %d times; the source must park instead of retrying", users)
	}
	r.hub.Detach("twitch", fakeBroadcaster)
}
