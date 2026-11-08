package overlay

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/eventstream"
)

// fakeScreen scripts the chat hub's answers.
type fakeScreen struct {
	mu        sync.Mutex
	text      chat.Display
	badNames  map[string]bool
	done      func(chat.Display)
	textCalls []string
}

func (f *fakeScreen) ScreenText(_ db.Platform, text string, done func(chat.Display)) chat.Display {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.textCalls = append(f.textCalls, text)
	f.done = done
	return f.text
}

func (f *fakeScreen) ScreenName(_ db.Platform, name string) chat.Display {
	if f.badNames[name] {
		return chat.DisplayBlocked
	}
	return chat.DisplayClear
}

func (f *fakeScreen) answer(v chat.Display) {
	f.mu.Lock()
	d := f.done
	f.mu.Unlock()
	d(v)
}

func newActivityGate(t *testing.T, scr Screener) (*ActivityGate, *eventstream.Subscription, *clock) {
	t.Helper()
	hub := eventstream.NewHub()
	sub, _, _ := hub.Subscribe(eventstream.Filter{Overlay: &eventstream.OverlayView{}}, "")
	t.Cleanup(sub.Close)
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	return NewActivityGate(hub, scr, WithActivityClock(c.now)), sub, c
}

func act(kind activity.Kind) activity.Event {
	e := activity.Sample(kind, "twitch", activity.Overrides{}, time.Unix(1_700_000_000, 0)).Normalise(nil)
	e.Account = "acct-secret"
	if e.Actor != nil {
		e.Actor.ID = "actor-id-secret"
	}
	return e
}

func nextDisplay(t *testing.T, sub *eventstream.Subscription) (eventstream.ActivityDisplay, bool) {
	t.Helper()
	env, ok := next(t, sub)
	if !ok {
		return eventstream.ActivityDisplay{}, false
	}
	if env.Type != eventstream.TypeActivityDisplay {
		t.Fatalf("published %s", env.Type)
	}
	return env.Data.(eventstream.ActivityDisplay), true
}

func TestAnAlertWithNoMessageIsShownAtOnceWithoutIdentity(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{})
	g.Offer(act(activity.KindFollow))
	d, ok := nextDisplay(t, sub)
	if !ok || d.Kind != "follow" || d.Name != "Test Viewer" || d.Platform != "twitch" || !d.Test {
		t.Fatalf("display: %+v ok=%v", d, ok)
	}
	b, _ := json.Marshal(d)
	for _, secret := range []string{"actor-id-secret", "acct-secret", "polyemesis.test", "test_viewer"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("the overlay projection leaks %q: %s", secret, b)
		}
	}
	if st := g.Stats(); st.Shown != 1 || st.Unworded != 0 {
		t.Errorf("stats %+v", st)
	}
}

func TestNeverOverlayKindsAreNotPublished(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{})
	for _, k := range []activity.Kind{activity.KindModeration, activity.KindStream, activity.KindPoll, activity.KindPrediction, activity.KindReaction} {
		g.Offer(act(k))
	}
	out := act(activity.KindRaid)
	out.Direction = "out"
	g.Offer(out)
	if _, ok := nextDisplay(t, sub); ok {
		t.Fatal("a never-on-air activity was published")
	}
}

func TestACleanMessageIsReleasedWithTheAlert(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{text: chat.DisplayClear})
	g.Offer(act(activity.KindTip))
	d, ok := nextDisplay(t, sub)
	if !ok || d.Message != "Great stream!" || d.Amount == nil || d.Amount.Currency != "USD" {
		t.Fatalf("display: %+v", d)
	}
}

func TestARemovedMessageStillShowsTheAlertWithoutIt(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{text: chat.DisplayBlocked})
	g.Offer(act(activity.KindTip))
	d, ok := nextDisplay(t, sub)
	if !ok || d.Message != "" || d.Amount == nil {
		t.Fatalf("display: %+v ok=%v", d, ok)
	}
	if st := g.Stats(); st.Unworded != 1 {
		t.Errorf("stats %+v", st)
	}
}

func TestAPendingMessageIsHeldUntilTheModelAnswers(t *testing.T) {
	scr := &fakeScreen{text: chat.DisplayPending}
	g, sub, _ := newActivityGate(t, scr)
	g.Offer(act(activity.KindTip))
	if _, ok := nextDisplay(t, sub); ok {
		t.Fatal("an alert whose message is being judged went out early")
	}
	scr.answer(chat.DisplayClear)
	d, ok := nextDisplay(t, sub)
	if !ok || d.Message == "" {
		t.Fatalf("released alert: %+v ok=%v", d, ok)
	}
	// A second answer for the same alert does nothing.
	scr.answer(chat.DisplayClear)
	if _, ok := nextDisplay(t, sub); ok {
		t.Error("an alert was published twice")
	}
}

func TestAModelRemovalShowsTheAlertWithoutTheMessage(t *testing.T) {
	scr := &fakeScreen{text: chat.DisplayPending}
	g, sub, _ := newActivityGate(t, scr)
	g.Offer(act(activity.KindResub))
	scr.answer(chat.DisplayBlocked)
	d, ok := nextDisplay(t, sub)
	if !ok || d.Message != "" || d.Months != 12 {
		t.Fatalf("display: %+v", d)
	}
}

func TestAMessageNotJudgedInTimeIsDroppedButTheAlertShows(t *testing.T) {
	scr := &fakeScreen{text: chat.DisplayPending}
	g, sub, c := newActivityGate(t, scr)
	g.Offer(act(activity.KindTip))
	g.Sweep()
	if _, ok := nextDisplay(t, sub); ok {
		t.Fatal("swept before the deadline")
	}
	c.add(MaxHold + time.Millisecond)
	g.Sweep()
	d, ok := nextDisplay(t, sub)
	if !ok || d.Message != "" {
		t.Fatalf("expired hold: %+v ok=%v", d, ok)
	}
	// A verdict that arrives after the sweep is refused.
	scr.answer(chat.DisplayClear)
	if _, ok := nextDisplay(t, sub); ok {
		t.Error("a late verdict published a second alert")
	}
	if st := g.Stats(); st.Unworded != 1 || st.Shown != 1 {
		t.Errorf("stats %+v", st)
	}
}

func TestALateCleanVerdictBeforeTheSweepStillCountsAsExpired(t *testing.T) {
	scr := &fakeScreen{text: chat.DisplayPending}
	g, sub, c := newActivityGate(t, scr)
	g.Offer(act(activity.KindTip))
	c.add(MaxHold + time.Second)
	scr.answer(chat.DisplayClear)
	d, ok := nextDisplay(t, sub)
	if !ok || d.Message != "" {
		t.Fatalf("a verdict after the deadline released the message: %+v", d)
	}
}

func TestABlockedNameIsBlankedAndTheAlertStillShows(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{badNames: map[string]bool{"Test Viewer": true, "Guest One": true}})
	e := act(activity.KindGift)
	e.Count = 1
	e.Recipients = e.Recipients[:1]
	g.Offer(e)
	d, ok := nextDisplay(t, sub)
	if !ok || d.Name != "" || d.Recipient != "" || d.Kind != "gift" || d.Count != 1 {
		t.Fatalf("display: %+v", d)
	}
	if st := g.Stats(); st.Renamed != 2 {
		t.Errorf("renamed %d", st.Renamed)
	}
}

func TestAGiftBombNamesNoRecipient(t *testing.T) {
	g, sub, _ := newActivityGate(t, nil)
	g.Offer(act(activity.KindGift)) // count 5, five recipients
	d, _ := nextDisplay(t, sub)
	if d.Recipient != "" || d.Count != 5 {
		t.Errorf("display: %+v", d)
	}
}

func TestAnAnonymousActorHasNoName(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{})
	e := act(activity.KindGift)
	e.Actor = &activity.Actor{Anonymous: true}
	g.Offer(e)
	if d, _ := nextDisplay(t, sub); d.Name != "" {
		t.Errorf("name %q", d.Name)
	}
}

func TestARaidIsNamedByTheOtherChannel(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{})
	g.Offer(act(activity.KindRaid))
	d, _ := nextDisplay(t, sub)
	if d.Name != "Friend Channel" || d.Viewers != 42 {
		t.Errorf("display: %+v", d)
	}
}

func TestWithNoScreenerEverythingPasses(t *testing.T) {
	g, sub, _ := newActivityGate(t, nil)
	g.Offer(act(activity.KindTip))
	d, ok := nextDisplay(t, sub)
	if !ok || d.Message == "" {
		t.Fatalf("display: %+v", d)
	}
}

func TestEveryOfferGetsAFreshDisplayID(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{})
	e := act(activity.KindFollow)
	g.Offer(e)
	g.Offer(e)
	a, _ := nextDisplay(t, sub)
	b, _ := nextDisplay(t, sub)
	if a.ID == "" || a.ID == b.ID {
		t.Errorf("ids %q %q: a replay must be a new alert to the overlay's queue", a.ID, b.ID)
	}
}

func TestTheProjectionOwnsACopyOfTheAmount(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{})
	e := act(activity.KindTip)
	g.Offer(e)
	d, _ := nextDisplay(t, sub)
	d.Amount.Minor = 1
	if e.Amount.Minor == 1 {
		t.Error("the display aliases the event's amount")
	}
}

func TestRunSweepsUntilCancelled(t *testing.T) {
	scr := &fakeScreen{text: chat.DisplayPending}
	g, sub, c := newActivityGate(t, scr)
	g.Offer(act(activity.KindTip))
	c.add(MaxHold + time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	deadline := time.After(2 * time.Second)
	for {
		if d, ok := nextDisplay(t, sub); ok {
			if d.Message != "" {
				t.Fatal("the sweep released the message")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run never swept the expired hold")
		default:
		}
	}
	cancel()
	<-done
}

func TestEveryOverlayKindProjects(t *testing.T) {
	g, sub, _ := newActivityGate(t, &fakeScreen{})
	for _, k := range activity.OverlayKinds() {
		g.Offer(act(k))
		d, ok := nextDisplay(t, sub)
		if !ok || d.Kind != string(k) {
			t.Errorf("%s: %+v ok=%v", k, d, ok)
		}
	}
}

func TestAlertStyleDefaultsAndValidation(t *testing.T) {
	d := DefaultAlertStyle()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(d.Kinds) != len(activity.OverlayKinds()) {
		t.Errorf("default kinds: %v", d.Kinds)
	}
	bad := map[string]func(*AlertStyle){
		"layout":    func(a *AlertStyle) { a.Layout = "marquee" },
		"sound":     func(a *AlertStyle) { a.Sound = "https://evil.test/a.mp3" },
		"too short": func(a *AlertStyle) { a.Seconds = 2 },
		"too long":  func(a *AlertStyle) { a.Seconds = 16 },
		"accent":    func(a *AlertStyle) { a.Accent = "red" },
		"upper":     func(a *AlertStyle) { a.Accent = "#FFFFFF" },
		"kind":      func(a *AlertStyle) { a.Kinds = []string{"moderation"} },
		"unknown":   func(a *AlertStyle) { a.Kinds = []string{"sneeze"} },
		"twice":     func(a *AlertStyle) { a.Kinds = []string{"follow", "follow"} },
	}
	for name, mut := range bad {
		a := DefaultAlertStyle()
		mut(&a)
		if a.Validate() == nil {
			t.Errorf("%s validated", name)
		}
	}
}

func TestStyleWithAndWithoutAlertsParses(t *testing.T) {
	s, err := ParseStyle(nil)
	if err != nil || s.Alerts != nil {
		t.Fatalf("default style: %+v %v", s, err)
	}
	if got := s.AlertsOrDefault(); got.Layout != "banner" {
		t.Errorf("effective: %+v", got)
	}
	raw := `{"preset":"classic","font":"sans","fontSize":24,"textColor":"#ffffff","backgroundColor":"#000000",` +
		`"backgroundOpacity":0,"authorColors":true,"fadeSeconds":30,"maxLines":12,"showPlatform":true,"showBadges":true,` +
		`"alerts":{"layout":"card","kinds":["follow","tip"],"seconds":8,"showMessage":false,"showAmount":true,"sound":"chime","accent":"#ff0000"}}`
	s, err = ParseStyle(json.RawMessage(raw))
	if err != nil || s.Alerts == nil || s.Alerts.Layout != "card" || s.AlertsOrDefault().Seconds != 8 {
		t.Fatalf("parsed: %+v %v", s.Alerts, err)
	}
	broken := strings.Replace(raw, `"seconds":8`, `"seconds":99`, 1)
	if _, err := ParseStyle(json.RawMessage(broken)); err == nil {
		t.Error("an out-of-range alert style parsed")
	}
	unknown := strings.Replace(raw, `"sound":"chime"`, `"sound":"chime","url":"x"`, 1)
	if _, err := ParseStyle(json.RawMessage(unknown)); err == nil {
		t.Error("an unknown alert field parsed")
	}
}

func TestAnAlertsStyleWithoutKindsGetsTheDefaultListNotNull(t *testing.T) {
	style := func(kinds string) json.RawMessage {
		return json.RawMessage(`{"preset":"classic","font":"sans","fontSize":24,"textColor":"#ffffff","backgroundColor":"#000000",` +
			`"backgroundOpacity":0,"authorColors":true,"fadeSeconds":30,"maxLines":12,"showPlatform":true,"showBadges":true,` +
			`"alerts":{"layout":"card",` + kinds + `"seconds":8,"showMessage":false,"showAmount":true,"sound":"chime","accent":"#ff0000"}}`)
	}
	for name, kinds := range map[string]string{"absent": "", "null": `"kinds":null,`} {
		s, err := ParseStyle(style(kinds))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(s.Alerts.Kinds) != len(activity.OverlayKinds()) {
			t.Errorf("%s: kinds = %v", name, s.Alerts.Kinds)
		}
		b, _ := json.Marshal(s)
		if strings.Contains(string(b), `"kinds":null`) {
			t.Errorf("%s: stored as null: %s", name, b)
		}
	}
	// An explicit empty list is a choice, and stays empty.
	if s, err := ParseStyle(style(`"kinds":[],`)); err != nil || s.Alerts.Kinds == nil || len(s.Alerts.Kinds) != 0 {
		t.Errorf("empty list: %+v %v", s.Alerts, err)
	}
	// A hand-built style without a list is refused rather than stored as null.
	a := DefaultAlertStyle()
	a.Kinds = nil
	if a.Validate() == nil {
		t.Error("nil kinds validated")
	}
}
