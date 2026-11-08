package chat

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
)

// rumbleActSink collects chat and activity from one Run.
type rumbleActSink struct {
	mu   sync.Mutex
	chat []Message
	acts []activity.Event
}

func (s *rumbleActSink) Deliver(m Message) {
	s.mu.Lock()
	s.chat = append(s.chat, m)
	s.mu.Unlock()
}

func (s *rumbleActSink) Activity(ev activity.Event) {
	s.mu.Lock()
	s.acts = append(s.acts, ev)
	s.mu.Unlock()
}

func (s *rumbleActSink) take() []activity.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.acts
	s.acts = nil
	return out
}

// rumbleSeq serves bodies[i] on poll i+1 and repeats the last. Status 500 for
// a body of "500".
func rumbleSeq(t *testing.T, bodies ...string) *rumbleStub {
	return newRumbleStub(t, func(w http.ResponseWriter, _ *http.Request, call int64) {
		i := int(call) - 1
		if i >= len(bodies) {
			i = len(bodies) - 1
		}
		if bodies[i] == "500" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errors":[{"code":"500","message":"boom"}]}`))
			return
		}
		_, _ = w.Write([]byte(bodies[i]))
	})
}

// runPolls drives one Run for n polls and returns each poll's activity.
func runPolls(t *testing.T, a *RumbleAdapter, n int) (perPoll [][]activity.Event, sink *rumbleActSink) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink = &rumbleActSink{}
	polls := 0
	a.cfg.Sleep = func(context.Context, time.Duration) bool {
		perPoll = append(perPoll, sink.take())
		polls++
		if polls >= n {
			cancel()
			return false
		}
		return true
	}
	if err := a.Run(ctx, sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return perPoll, sink
}

// rumbleActBody builds a get-data body in the shape of Rumble's published
// example. Each list is the raw JSON array for that block, or "" to omit the
// block entirely; live toggles the broadcast.
func rumbleActBody(followers, subs, gifts, rants string, live bool) string {
	var b strings.Builder
	b.WriteString(`{"now":1695059500,"type":"user","max_num_results":50,`)
	if followers != "" {
		fmt.Fprintf(&b, `"followers":{"num_followers":165,"num_followers_total":362,"recent_followers":%s},`, followers)
	}
	if subs != "" {
		fmt.Fprintf(&b, `"subscribers":{"num_subscribers":5,"recent_subscribers":%s},`, subs)
	}
	if gifts != "" {
		fmt.Fprintf(&b, `"gifted_subs":{"num_gifted_subs":1,"recent_gifted_subs":%s},`, gifts)
	}
	fmt.Fprintf(&b, `"livestreams":[{"id":"x","title":"T","is_live":%t,"stream_key":"SECRET","watching_now":19,`+
		`"chat":{"recent_messages":[],"recent_rants":%s}}]}`, live, orEmpty(rants))
	return b.String()
}

func orEmpty(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

const (
	fA = `{"username":"UserNameA","followed_on":"2023-09-17T20:43:56-04:00"}`
	fB = `{"username":"UserNameB","followed_on":"2023-09-17T19:10:02-04:00"}`
	fC = `{"username":"UserNameC","followed_on":"2026-08-13T17:59:50-04:00"}`

	sE = `{"user":"UserNameE","username":"UserNameE","amount_cents":500,"amount_dollars":5,"subscribed_on":"2023-09-15T21:03:05+00:00"}`
	sF = `{"user":"UserNameF","username":"UserNameF","amount_cents":1050,"amount_dollars":10,"subscribed_on":"2026-08-13T17:59:00+00:00"}`

	gA = `{"total_gifts":1,"gift_type":"rumble","remaining_gifts":1,"video_id":31313131,"purchased_by":"UsernameA"}`
	gB = `{"total_gifts":5,"gift_type":"rumble","remaining_gifts":5,"video_id":31313131,"purchased_by":"UsernameB"}`

	rB = `{"username":"UserNameB","badges":["admin"],"text":"Rant Message Goes Here","created_on":"2023-09-18T17:51:37+00:00","expires_on":"2023-09-18T17:53:37+00:00","amount_cents":100,"amount_dollars":1}`
	rC = `{"username":"UserNameC","badges":[],"text":"second rant","created_on":"2026-08-13T17:59:40+00:00","expires_on":"2026-08-13T18:01:40+00:00","amount_cents":150,"amount_dollars":1}`
)

func arr(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

func TestRumbleActivityFirstPollIsSilentThenOnlyNewItemsEmit(t *testing.T) {
	first := rumbleActBody(arr(fB, fA), arr(sE), arr(gA), arr(rB), true)
	second := rumbleActBody(arr(fC, fB, fA), arr(sF, sE), arr(gB, gA), arr(rC, rB), true)
	s := rumbleSeq(t, first, second, second)
	a := rumbleAdapter(t, s.URL, nil)

	polls, sink := runPolls(t, a, 3)
	if len(polls[0]) != 0 {
		t.Fatalf("first poll must be a silent baseline, got %d events: %+v", len(polls[0]), polls[0])
	}
	if len(polls[2]) != 0 {
		t.Fatalf("an identical third poll must emit nothing, got %+v", polls[2])
	}
	got := polls[1]
	if len(got) != 4 {
		t.Fatalf("second poll: want follow+subscribe+gift+tip, got %d: %+v", len(got), got)
	}
	by := map[activity.Kind]activity.Event{}
	for _, ev := range got {
		by[ev.Kind] = ev
		if ev.Platform != "rumble" || ev.Account != "rumble-me" {
			t.Errorf("%s: platform/account = %q/%q", ev.Kind, ev.Platform, ev.Account)
		}
		if err := ev.Normalise(a.cfg.Now).Validate(); err != nil {
			t.Errorf("%s does not validate: %v", ev.Kind, err)
		}
	}
	if f := by[activity.KindFollow]; f.Actor.Name != "UserNameC" || f.Source.Type != "recent_followers" ||
		!f.At.Equal(time.Date(2026, 8, 13, 21, 59, 50, 0, time.UTC)) {
		t.Errorf("follow = %+v", f)
	}
	if sub := by[activity.KindSubscribe]; sub.Actor.Name != "UserNameF" || sub.Product != "paid_sub" ||
		sub.Amount == nil || sub.Amount.Currency != "USD" || sub.Amount.Minor != 1050 || sub.Amount.Exponent != 2 {
		t.Errorf("subscribe = %+v amount=%+v", sub, sub.Amount)
	}
	if g := by[activity.KindGift]; g.Actor.Name != "UsernameB" || g.Count != 5 || g.Source.Type != "recent_gifted_subs" || !g.At.IsZero() {
		t.Errorf("gift = %+v", g)
	}
	tip := by[activity.KindTip]
	if tip.Actor.Name != "UserNameC" || tip.Message != "second rant" || tip.Source.Type != "rant" ||
		tip.Amount == nil || tip.Amount.Currency != "USD" || tip.Amount.Minor != 150 || tip.Amount.Exponent != 2 || tip.Amount.Unit != "" {
		t.Errorf("tip = %+v amount=%+v", tip, tip.Amount)
	}
	for _, ev := range got {
		if strings.Contains(fmt.Sprintf("%+v", ev), "SECRET") {
			t.Fatalf("stream key leaked into an activity: %+v", ev)
		}
	}
	// A rant is still a chat line.
	if len(sink.chat) == 0 {
		t.Fatal("rants must still reach chat")
	}
}

func TestRumbleActivityRestartDoesNotReplay(t *testing.T) {
	body := rumbleActBody(arr(fB, fA), arr(sE), arr(gA), arr(rB), true)
	s := rumbleOK(t, body)
	a := rumbleAdapter(t, s.URL, nil)
	for run := 1; run <= 3; run++ { // the Hub calls Run again after a failure
		polls, _ := runPolls(t, a, 2)
		for i, p := range polls {
			if len(p) != 0 {
				t.Fatalf("run %d poll %d replayed history: %+v", run, i, p)
			}
		}
	}
}

func TestRumbleActivityRepeatedIdenticalGiftIsASecondEventAndClaimsAreNot(t *testing.T) {
	base := rumbleActBody(arr(fA), arr(sE), arr(gA), "", false)
	claimed := strings.ReplaceAll(base, `"remaining_gifts":1`, `"remaining_gifts":0`)
	twice := rumbleActBody(arr(fA), arr(sE), arr(gA, gA), "", false)
	s := rumbleSeq(t, base, claimed, twice, twice)
	a := rumbleAdapter(t, s.URL, nil)
	polls, _ := runPolls(t, a, 4)
	if len(polls[1]) != 0 {
		t.Fatalf("remaining_gifts falling is not a new gift: %+v", polls[1])
	}
	if len(polls[2]) != 1 || polls[2][0].Kind != activity.KindGift {
		t.Fatalf("an identical second purchase must emit once: %+v", polls[2])
	}
	// The hub dedupes on Event.Key(); the copy must not collide with the first.
	first := activity.Event{Kind: activity.KindGift, Platform: "rumble", Account: "rumble-me",
		Source: activity.Source{Type: "recent_gifted_subs"}}
	second := first
	second.Source.ID = polls[2][0].Source.ID
	first.Source.ID = strings.TrimSuffix(second.Source.ID, ":2")
	if first.Source.ID == second.Source.ID || first.Key() == second.Key() {
		t.Fatalf("second identical gift would be dropped by the hub: %q vs %q", first.Source.ID, second.Source.ID)
	}
	if len(polls[3]) != 0 {
		t.Fatalf("and not again: %+v", polls[3])
	}
}

func TestRumbleActivityTierGatedKindsAreUnavailableNotErrors(t *testing.T) {
	// A channel with no subscriptions: no subscribers or gifted_subs blocks.
	body := rumbleActBody(arr(fA), "", "", "", false)
	s := rumbleOK(t, body)
	a := rumbleAdapter(t, s.URL, nil)

	for _, k := range a.ActivityKinds() {
		if k.State != activity.KindUnknown {
			t.Fatalf("before any poll %s must be unknown, got %s", k.Kind, k.State)
		}
	}
	polls, _ := runPolls(t, a, 2)
	for _, p := range polls {
		if len(p) != 0 {
			t.Fatalf("unexpected events %+v", p)
		}
	}
	state := map[activity.Kind]activity.KindState{}
	for _, k := range a.ActivityKinds() {
		state[k.Kind] = k.State
		if k.State == activity.KindUnavailable && k.Detail == "" {
			t.Errorf("%s unavailable with no reason", k.Kind)
		}
	}
	if state[activity.KindFollow] != activity.KindOn ||
		state[activity.KindSubscribe] != activity.KindUnavailable ||
		state[activity.KindGift] != activity.KindUnavailable ||
		state[activity.KindTip] != activity.KindUnknown {
		t.Fatalf("kinds = %v", state)
	}
	if h := a.Health(); h.State == StateFailed {
		t.Fatalf("an unavailable kind must not fail the source: %+v", h)
	}
}

func TestRumbleActivityListThatAppearsLaterIsBaselinedNotReplayed(t *testing.T) {
	without := rumbleActBody(arr(fA), "", "", "", false)
	with := rumbleActBody(arr(fA), arr(sE, sF), arr(gA), "", false)
	s := rumbleSeq(t, without, with, with)
	polls, _ := runPolls(t, rumbleAdapter(t, s.URL, nil), 3)
	for i, p := range polls {
		if len(p) != 0 {
			t.Fatalf("poll %d: a list that first appears is a baseline, got %+v", i, p)
		}
	}
}

func TestRumbleActivityMalformedEntriesAreSkippedAndCounted(t *testing.T) {
	first := rumbleActBody(arr(fA), arr(sE), arr(gA), "", true)
	bad := rumbleActBody(
		arr(`{"username":"","followed_on":"2026-08-13T17:59:50Z"}`, `{"username":"Odd","followed_on":"not a date"}`, fA),
		arr(`{"username":"Neg","amount_cents":-5,"subscribed_on":"2026-08-13T17:59:00Z"}`, sE),
		arr(`{"purchased_by":"Zero","total_gifts":0}`, gA),
		arr(`{"username":"NoAmt","text":"hi","created_on":"2026-08-13T17:59:00Z"}`,
			`{"username":"","text":"x","created_on":"2026-08-13T17:59:00Z","amount_cents":100}`), true)
	s := rumbleSeq(t, first, bad, bad)
	a := rumbleAdapter(t, s.URL, nil)
	polls, _ := runPolls(t, a, 3)
	if len(polls[1]) != 1 {
		t.Fatalf("only the unparseable-date follower is real: %+v", polls[1])
	}
	if ev := polls[1][0]; ev.Actor.Name != "Odd" || !ev.At.IsZero() {
		t.Fatalf("a bad timestamp keeps the event and leaves At for receipt time: %+v", ev)
	}
	if len(polls[2]) != 0 {
		t.Fatalf("redelivery of the malformed poll: %+v", polls[2])
	}
	if got := a.activityIgnoredCount(); got < 4 {
		t.Fatalf("ignored = %d, want the skipped entries counted", got)
	}
}

func TestRumbleActivityMalformedBodyAndTransientFailureDoNotResetOrReplay(t *testing.T) {
	good1 := rumbleActBody(arr(fA), arr(sE), arr(gA), arr(rB), true)
	good2 := rumbleActBody(arr(fC, fA), arr(sE), arr(gA), arr(rB), true)
	s := rumbleSeq(t, good1, `{"followers": {`, "500", good2, good2)
	a := rumbleAdapter(t, s.URL, nil)
	polls, _ := runPolls(t, a, 5)
	var total []activity.Event
	for _, p := range polls {
		total = append(total, p...)
	}
	if len(total) != 1 || total[0].Kind != activity.KindFollow || total[0].Actor.Name != "UserNameC" {
		t.Fatalf("after failed polls only the new follower may emit: %+v", total)
	}
}

func TestRumbleActivityNextBroadcastRantsAreBaselinedButFollowsAreNot(t *testing.T) {
	live1 := rumbleActBody(arr(fA), arr(sE), arr(gA), arr(rB), true)
	off := rumbleActBody(arr(fA), arr(sE), arr(gA), "", false)
	// The second broadcast opens with a rant already posted, and a new follower
	// arrived while offline.
	live2 := rumbleActBody(arr(fC, fA), arr(sE), arr(gA), arr(rC), true)
	live3 := rumbleActBody(arr(fC, fA), arr(sE), arr(gA), arr(rC, rB), true)
	s := rumbleSeq(t, live1, off, live2, live3)
	polls, _ := runPolls(t, rumbleAdapter(t, s.URL, nil), 4)
	if len(polls[2]) != 1 || polls[2][0].Kind != activity.KindFollow {
		t.Fatalf("offline follower emits once; the new broadcast's first rants are a baseline: %+v", polls[2])
	}
	if len(polls[3]) != 0 {
		t.Fatalf("rB was seen on the first broadcast and must not replay: %+v", polls[3])
	}
}

func TestRumbleActivityReportsAllFourKindsOnceSeen(t *testing.T) {
	s := rumbleOK(t, rumbleActBody(arr(fA), arr(sE), arr(gA), arr(rB), true))
	a := rumbleAdapter(t, s.URL, nil)
	runPolls(t, a, 1)
	ks := a.ActivityKinds()
	if len(ks) != 4 {
		t.Fatalf("kinds = %+v", ks)
	}
	for _, k := range ks {
		if k.State != activity.KindOn {
			t.Errorf("%s = %s", k.Kind, k.State)
		}
	}
}

func TestRumbleActivityWrongTypedBlockCostsOneEntryNeverTheChat(t *testing.T) {
	first := rumbleActBody(arr(fA), arr(sE), arr(gA), arr(rB), true)
	// total_gifts as a string, recent_followers as an object, one good new sub.
	broken := rumbleActBody(`{"oops":1}`, arr(sF, `{"username":"X","amount_cents":"five"}`), arr(`{"total_gifts":"1","purchased_by":"Z"}`, gA), arr(rB, rC), true)
	s := rumbleSeq(t, first, broken, broken)
	a := rumbleAdapter(t, s.URL, nil)
	polls, sink := runPolls(t, a, 3)
	if h := a.Health(); h.State != StateLive {
		t.Fatalf("a type surprise in an activity block must not touch chat health: %+v", h)
	}
	kinds := map[activity.Kind]int{}
	for _, ev := range polls[1] {
		kinds[ev.Kind]++
	}
	if kinds[activity.KindSubscribe] != 1 || kinds[activity.KindTip] != 1 || len(polls[1]) != 2 {
		t.Fatalf("the readable entries still emit: %+v", polls[1])
	}
	if len(polls[2]) != 0 {
		t.Fatalf("redelivery: %+v", polls[2])
	}
	if a.activityIgnoredCount() == 0 {
		t.Fatal("skipped entries must be counted")
	}
	if len(sink.chat) == 0 {
		t.Fatal("chat must still be delivered")
	}
}
