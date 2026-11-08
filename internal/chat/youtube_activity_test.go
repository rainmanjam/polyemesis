package chat

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

// ytActSink collects chat lines AND activities, as runnerSink does.
type ytActSink struct {
	mu   sync.Mutex
	msgs []Message
	acts []activity.Event
}

func (s *ytActSink) Deliver(m Message) { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() }
func (s *ytActSink) Activity(e activity.Event) {
	s.mu.Lock()
	s.acts = append(s.acts, e)
	s.mu.Unlock()
}

var ytNoSleep = func(c *YouTubeConfig) {
	c.Now = fixedClock(time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC))
	c.Sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
}

// Payloads follow the liveChatMessages resource reference. amountMicros is
// sent both as a number and, as Google's JSON mapping does for 64-bit
// integers, as a string.
const ytPaidPage = `{"pollingIntervalMillis":5000,"nextPageToken":"p2","items":[
 {"id":"LCC.sc","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:05Z","hasDisplayContent":true,
   "displayMessage":"$5.00 great stream!","superChatDetails":{"amountMicros":"5000000","currency":"USD","amountDisplayString":"$5.00","userComment":"great stream!","tier":2}},
  "authorDetails":{"channelId":"UCmara","displayName":"Mara"}},
 {"id":"LCC.yen","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:06Z",
   "displayMessage":"JP¥500","superChatDetails":{"amountMicros":500000000,"currency":"JPY","amountDisplayString":"¥500","tier":1}},
  "authorDetails":{"channelId":"UCk","displayName":"Ken"}},
 {"id":"LCC.bhd","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:07Z",
   "displayMessage":"BHD 1.234","superChatDetails":{"amountMicros":"1234000","currency":"BHD","amountDisplayString":"BHD 1.234","tier":1}},
  "authorDetails":{"channelId":"UCb","displayName":"Bo"}},
 {"id":"LCC.zzz","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:08Z",
   "displayMessage":"x","superChatDetails":{"amountMicros":"1234567","currency":"ZZZ","amountDisplayString":"ZZZ 1.23"}},
  "authorDetails":{"channelId":"UCz","displayName":"Zed"}},
 {"id":"LCC.st","snippet":{"type":"superStickerEvent","publishedAt":"2026-03-01T20:00:09Z",
   "displayMessage":"","superStickerDetails":{"amountMicros":"2000000","currency":"EUR","amountDisplayString":"€2.00","tier":1,
   "superStickerMetadata":{"stickerId":"s1","altText":"Waving cat","language":"en"}}},
  "authorDetails":{"channelId":"UCs","displayName":"Sol"}}]}`

func TestYouTubeActivityMapsPaidMessagesWithoutConvertingMoney(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			fmt.Fprint(w, ytPaidPage)
			return
		}
		fmt.Fprint(w, `{"offlineAt":"x","items":[]}`)
	})
	a := ytAdapter(t, stub.URL, ytNoSleep)
	// The adapter began at 20:00; give the clock a start before the items.
	s := &ytActSink{}
	if err := a.pump(context.Background(), "chat-1", s); err != nil {
		t.Fatal(err)
	}
	if len(s.acts) != 5 {
		t.Fatalf("activities = %d, want 5: %+v", len(s.acts), s.acts)
	}
	byID := map[string]activity.Event{}
	for _, e := range s.acts {
		if err := withPlatform(e).Validate(); err != nil {
			t.Errorf("%s: %v", e.Source.ID, err)
		}
		byID[e.Source.ID] = e
	}
	sc := byID["LCC.sc"]
	if sc.Kind != activity.KindTip || sc.Source.Type != "superChatEvent" ||
		*sc.Amount != (activity.Amount{Currency: "USD", Minor: 500, Exponent: 2, Display: "$5.00"}) ||
		sc.Message != "great stream!" || sc.Tier != "2" || sc.Actor.ID != "UCmara" || sc.Actor.Name != "Mara" {
		t.Errorf("super chat = %+v amount %+v", sc, sc.Amount)
	}
	if a := byID["LCC.yen"].Amount; a.Minor != 500 || a.Exponent != 0 || a.Currency != "JPY" {
		t.Errorf("JPY = %+v", a)
	}
	if a := byID["LCC.bhd"].Amount; a.Minor != 1234 || a.Exponent != 3 {
		t.Errorf("BHD = %+v", a)
	}
	// An unlisted currency is carried exactly, not guessed at.
	if a := byID["LCC.zzz"].Amount; a.Minor != 1234567 || a.Exponent != 6 {
		t.Errorf("unlisted currency = %+v", a)
	}
	st := byID["LCC.st"]
	if st.Kind != activity.KindTip || st.Source.Type != "superStickerEvent" || st.Message != "Waving cat" || st.Amount.Minor != 200 {
		t.Errorf("sticker = %+v", st)
	}
	// Per D3 the paid messages stay chat lines too: only those with text.
	if len(s.msgs) != 4 {
		t.Errorf("chat lines = %d, want the 4 with displayMessage", len(s.msgs))
	}
}

const ytMemberPage = `{"nextPageToken":"p2","items":[
 {"id":"m1","snippet":{"type":"newSponsorEvent","publishedAt":"2026-03-01T20:01:00Z","displayMessage":"Welcome Ann!",
   "newSponsorDetails":{"memberLevelName":"Gold","isUpgrade":false}},"authorDetails":{"channelId":"UCa","displayName":"Ann"}},
 {"id":"m2","snippet":{"type":"memberMilestoneChatEvent","publishedAt":"2026-03-01T20:01:01Z","displayMessage":"6 months",
   "memberMilestoneChatDetails":{"memberMonth":6,"memberLevelName":"Gold","userComment":"still here"}},"authorDetails":{"channelId":"UCb","displayName":"Bea"}},
 {"id":"g1","snippet":{"type":"membershipGiftingEvent","publishedAt":"2026-03-01T20:01:02Z","displayMessage":"",
   "membershipGiftingDetails":{"giftMembershipsCount":3,"giftMembershipsLevelName":"Gold"}},"authorDetails":{"channelId":"UCc","displayName":"Cy"}},
 {"id":"r1","snippet":{"type":"giftMembershipReceivedEvent","publishedAt":"2026-03-01T20:01:03Z","displayMessage":"",
   "giftMembershipReceivedDetails":{"memberLevelName":"Gold","gifterChannelId":"UCc","associatedMembershipGiftingMessageId":"g1"}},"authorDetails":{"channelId":"UCd","displayName":"Dee"}},
 {"id":"r2","snippet":{"type":"giftMembershipReceivedEvent","publishedAt":"2026-03-01T20:01:03Z","displayMessage":"",
   "giftMembershipReceivedDetails":{"associatedMembershipGiftingMessageId":"g1"}},"authorDetails":{"channelId":"UCe","displayName":"Eve"}}]}`

func TestYouTubeActivityMembershipsAndGiftCollapse(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			fmt.Fprint(w, ytMemberPage)
			return
		}
		fmt.Fprint(w, `{"offlineAt":"x","items":[]}`)
	})
	a := ytAdapter(t, stub.URL, ytNoSleep)
	s := &ytActSink{}
	if err := a.pump(context.Background(), "c", s); err != nil {
		t.Fatal(err)
	}
	if len(s.acts) != 3 {
		t.Fatalf("activities = %+v, want subscribe, resub, gift (receipts collapse)", s.acts)
	}
	sub, re, gift := s.acts[0], s.acts[1], s.acts[2]
	if sub.Kind != activity.KindSubscribe || sub.Product != "membership" || sub.Tier != "Gold" || sub.Message != "" {
		t.Errorf("subscribe = %+v", sub)
	}
	if re.Kind != activity.KindResub || re.Months != 6 || re.Message != "still here" {
		t.Errorf("resub = %+v", re)
	}
	// The gifting item has no text and is not a chat line, but still emits.
	if gift.Kind != activity.KindGift || gift.Count != 3 || gift.Actor.Name != "Cy" {
		t.Errorf("gift = %+v", gift)
	}
	for _, e := range s.acts {
		if err := withPlatform(e).Validate(); err != nil {
			t.Errorf("%s: %v", e.Source.ID, err)
		}
	}
	if c := a.ActivityCounters(); c.Ignored != 2 || c.Emitted != 3 {
		t.Errorf("counters = %+v", c)
	}
	for _, m := range s.msgs {
		if m.ID == "g1" || m.ID == "r1" {
			t.Errorf("an empty-text item became a chat line: %+v", m)
		}
	}
}

func TestYouTubeActivityDedupesAcrossPagesAndReconnect(t *testing.T) {
	item := `{"id":"LCC.once","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:05Z","displayMessage":"hi",
	  "superChatDetails":{"amountMicros":"1000000","currency":"USD","amountDisplayString":"$1.00","tier":1}},"authorDetails":{"channelId":"U","displayName":"U"}}`
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		// The same item on every list, as a re-listed history after a
		// reconnect is; the third call ends the chat.
		if call >= 3 {
			fmt.Fprintf(w, `{"offlineAt":"x","items":[%s]}`, item)
			return
		}
		fmt.Fprintf(w, `{"items":[%s]}`, item)
	})
	a := ytAdapter(t, stub.URL, ytNoSleep)
	s := &ytActSink{}
	for i := 0; i < 2; i++ { // two pumps on one adapter: a reconnect
		_ = a.pump(context.Background(), "c", s)
	}
	if len(s.acts) != 1 {
		t.Fatalf("emitted %d times, want once", len(s.acts))
	}
	if c := a.ActivityCounters(); c.Deduped < 2 {
		t.Errorf("counters = %+v", c)
	}
}

func TestYouTubeActivityRestartDoesNotReplayHistory(t *testing.T) {
	// A new adapter (a restart) lists history published before it began.
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			fmt.Fprint(w, `{"items":[
			 {"id":"old1","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T19:58:00Z","displayMessage":"old",
			  "superChatDetails":{"amountMicros":"9000000","currency":"USD","amountDisplayString":"$9.00"}},"authorDetails":{"channelId":"U","displayName":"U"}},
			 {"id":"old2","snippet":{"type":"newSponsorEvent","publishedAt":"2026-03-01T19:59:00Z","displayMessage":"Welcome",
			  "newSponsorDetails":{"memberLevelName":"Gold"}},"authorDetails":{"channelId":"V","displayName":"V"}},
			 {"id":"undated","snippet":{"type":"membershipGiftingEvent","displayMessage":"",
			  "membershipGiftingDetails":{"giftMembershipsCount":2}},"authorDetails":{"channelId":"W","displayName":"W"}}]}`)
			return
		}
		fmt.Fprint(w, `{"offlineAt":"x","items":[
		 {"id":"fresh","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:30Z","displayMessage":"new",
		  "superChatDetails":{"amountMicros":"3000000","currency":"USD","amountDisplayString":"$3.00"}},"authorDetails":{"channelId":"U","displayName":"U"}}]}`)
	})
	a := ytAdapter(t, stub.URL, ytNoSleep)
	s := &ytActSink{}
	if err := a.pump(context.Background(), "c", s); err != nil {
		t.Fatal(err)
	}
	if len(s.acts) != 1 || s.acts[0].Source.ID != "fresh" {
		t.Fatalf("activities = %+v, want only the item published after the start", s.acts)
	}
	if c := a.ActivityCounters(); c.Baseline != 3 {
		t.Errorf("counters = %+v", c)
	}
}

func TestYouTubeActivityMalformedPayloadsAreCountedNotFatal(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			fmt.Fprint(w, `{"nextPageToken":"p","items":[
			 {"id":"a","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:05Z","displayMessage":"no details"},"authorDetails":{"displayName":"A"}},
			 {"id":"b","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:05Z","displayMessage":"x","superChatDetails":{"currency":"USD"}},"authorDetails":{"displayName":"B"}},
			 {"id":"c","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:05Z","displayMessage":"x","superChatDetails":{"amountMicros":"1000000","currency":"US"}},"authorDetails":{"displayName":"C"}},
			 {"id":"d","snippet":{"type":"memberMilestoneChatEvent","publishedAt":"2026-03-01T20:00:05Z","displayMessage":"x","memberMilestoneChatDetails":{"memberMonth":0}},"authorDetails":{"displayName":"D"}},
			 {"id":"e","snippet":{"type":"membershipGiftingEvent","publishedAt":"2026-03-01T20:00:05Z","membershipGiftingDetails":{"giftMembershipsCount":0}},"authorDetails":{"displayName":"E"}},
			 {"id":"","snippet":{"type":"newSponsorEvent","publishedAt":"2026-03-01T20:00:05Z","newSponsorDetails":{}},"authorDetails":{"displayName":"F"}},
			 {"id":"g","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:05Z","displayMessage":"x","superChatDetails":{"amountMicros":"-5","currency":"USD"}},"authorDetails":{"displayName":"G"}},
			 {"id":"h","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:05Z","displayMessage":"ok","superChatDetails":{"amountMicros":"1000000","currency":"USD"}},"authorDetails":{"channelId":"UH"}}]}`)
			return
		}
		fmt.Fprint(w, `{"offlineAt":"x","items":[]}`)
	})
	a := ytAdapter(t, stub.URL, ytNoSleep)
	s := &ytActSink{}
	if err := a.pump(context.Background(), "c", s); err != nil {
		t.Fatalf("a malformed item stopped the poll: %v", err)
	}
	// "h" has no display name: an anonymous actor, which Validate accepts.
	if len(s.acts) != 1 || s.acts[0].Source.ID != "h" || !s.acts[0].Actor.Anonymous {
		t.Fatalf("activities = %+v", s.acts)
	}
	if err := withPlatform(s.acts[0]).Validate(); err != nil {
		t.Error(err)
	}
	if c := a.ActivityCounters(); c.Unparseable != 7 {
		t.Errorf("counters = %+v", c)
	}
}

func TestYouTubeActivityKindsAreUnknownUntilSeenAndNeverAnError(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			fmt.Fprint(w, `{"items":[{"id":"t","snippet":{"type":"textMessageEvent","publishedAt":"2026-03-01T20:00:05Z","displayMessage":"hello"},"authorDetails":{"displayName":"T"}}]}`)
			return
		}
		if call == 2 {
			fmt.Fprint(w, `{"items":[{"id":"sc","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:09Z","displayMessage":"hi",
			 "superChatDetails":{"amountMicros":"1000000","currency":"USD"}},"authorDetails":{"displayName":"T"}}]}`)
			return
		}
		fmt.Fprint(w, `{"offlineAt":"x","items":[]}`)
	})
	a := ytAdapter(t, stub.URL, ytNoSleep)
	state := func() map[activity.Kind]activity.KindState {
		m := map[activity.Kind]activity.KindState{}
		for _, k := range a.ActivityKinds() {
			m[k.Kind] = k.State
		}
		return m
	}
	if len(a.ActivityKinds()) != 4 {
		t.Fatal("want tip, subscribe, resub, gift")
	}
	for k, st := range state() {
		if st != activity.KindUnknown {
			t.Errorf("%s = %s before anything was seen", k, st)
		}
	}
	if err := a.pump(context.Background(), "c", &ytActSink{}); err != nil {
		t.Fatal(err)
	}
	got := state()
	if got[activity.KindTip] != activity.KindOn || got[activity.KindGift] != activity.KindUnknown {
		t.Errorf("states = %+v", got)
	}
	// A chat that carried no paid message is still a healthy chat.
	if h := a.Health(); h.State == StateFailed {
		t.Errorf("health = %+v", h)
	}
}

func TestYouTubeActivityScopeRefusalReportsReconnect(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"message":"scope","errors":[{"reason":"insufficientPermissions"}]}}`)
	})
	a := ytAdapter(t, stub.URL, ytNoSleep)
	err := a.pump(context.Background(), "c", &ytActSink{})
	if err == nil || !IsFatal(err) {
		t.Fatalf("err = %v, want fatal", err)
	}
	for _, k := range a.ActivityKinds() {
		if k.State != activity.KindReconnect || strings.Contains(k.Detail, "token") {
			t.Errorf("%+v", k)
		}
	}
}

func TestYouTubeActivityIgnoredByASinkThatCannotTakeIt(t *testing.T) {
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			fmt.Fprint(w, ytPaidPage)
			return
		}
		fmt.Fprint(w, `{"offlineAt":"x","items":[]}`)
	})
	a := ytAdapter(t, stub.URL, ytNoSleep)
	var n int
	if err := a.pump(context.Background(), "c", SinkFunc(func(Message) { n++ })); err != nil || n != 4 {
		t.Fatalf("err %v, lines %d", err, n)
	}
}

// withPlatform stamps what the runner stamps in production.
func withPlatform(e activity.Event) activity.Event {
	e.Platform = "youtube"
	return e.Normalise(nil)
}

func TestYouTubeActivityQuietChatFirstEventAfterEmptyPageEmits(t *testing.T) {
	var late atomic.Bool
	base := time.Date(2026, 3, 1, 20, 0, 0, 0, time.UTC)
	stub := newYTStub(t, func(w http.ResponseWriter, r *http.Request, call int64) {
		if call == 1 {
			late.Store(true) // the clock moves on once the empty page is read
			fmt.Fprint(w, `{"nextPageToken":"p2","items":[]}`)
			return
		}
		// Published at 20:00:03, after watching began and before the poll that
		// reads it (the clock has moved on by then).
		fmt.Fprint(w, `{"offlineAt":"x","items":[
		 {"id":"sc","snippet":{"type":"superChatEvent","publishedAt":"2026-03-01T20:00:03Z","displayMessage":"hi",
		  "superChatDetails":{"amountMicros":"20000000","currency":"USD","amountDisplayString":"$20.00"}},"authorDetails":{"channelId":"U","displayName":"U"}}]}`)
	})
	a := ytAdapter(t, stub.URL, func(c *YouTubeConfig) {
		c.Now = func() time.Time {
			if late.Load() {
				return base.Add(5 * time.Second)
			}
			return base
		}
		c.Sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	})
	s := &ytActSink{}
	if err := a.pump(context.Background(), "c", s); err != nil {
		t.Fatal(err)
	}
	if len(s.acts) != 1 || s.acts[0].Source.ID != "sc" {
		t.Fatalf("activities = %+v, want the Super Chat sent after an empty first page", s.acts)
	}
}

func TestYouTubeActivityModerationScopeRefusalDoesNotFlagKinds(t *testing.T) {
	a := ytAdapter(t, "http://unused.invalid", ytNoSleep)
	_ = a.classify(&apiError{Status: http.StatusForbidden, Reason: "insufficientPermissions"})
	for _, k := range a.ActivityKinds() {
		if k.State == activity.KindReconnect {
			t.Errorf("a refused write flagged %+v", k)
		}
	}
}
