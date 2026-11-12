package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

type ingestRecorder struct {
	mu  sync.Mutex
	got []activity.Event
}

func (r *ingestRecorder) Ingest(ev activity.Event) {
	r.mu.Lock()
	r.got = append(r.got, ev)
	r.mu.Unlock()
}

func (r *ingestRecorder) events() []activity.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]activity.Event(nil), r.got...)
}

// An adapter reaches the activity hub only through emitActivity on the sink it
// is handed; the runner stamps the platform and account it owns.
func TestAnAdapterEmitsActivityThroughItsSinkAndTheRunnerStampsIt(t *testing.T) {
	h := testHub(t)
	rec := &ingestRecorder{}
	h.SetActivity(rec)
	a := &fakeAdapter{platform: db.PlatformYouTube, account: "UC1", runFn: func(ctx context.Context, sink Receiver) error {
		emitActivity(sink, activity.Event{Kind: activity.KindTip, Source: activity.Source{Type: "superChatEvent", ID: "1"}})
		emitActivity(sink, activity.Event{Kind: activity.KindFollow, Platform: "kick", Account: "own", Source: activity.Source{Type: "x", ID: "2"}})
		<-ctx.Done()
		return nil
	}}
	attach(t, h, a)
	waitFor(t, "two activities", func() bool { return len(rec.events()) == 2 })
	got := rec.events()
	if got[0].Platform != "youtube" || got[0].Account != "UC1" {
		t.Errorf("not stamped: %+v", got[0])
	}
	if got[1].Platform != "kick" || got[1].Account != "own" {
		t.Errorf("an adapter's own platform/account were overwritten: %+v", got[1])
	}
}

func TestActivityWithNoIngesterIsDroppedAndASinkWithoutTheCapabilityIsFine(t *testing.T) {
	h := testHub(t)
	done := make(chan struct{})
	a := &fakeAdapter{platform: db.PlatformTwitch, account: "a", runFn: func(ctx context.Context, sink Receiver) error {
		emitActivity(sink, activity.Event{Kind: activity.KindFollow})
		close(done)
		<-ctx.Done()
		return nil
	}}
	attach(t, h, a)
	<-done
	// SinkFunc is not an ActivityReceiver: emitting to it is a silent no-op.
	emitActivity(SinkFunc(func(Message) {}), activity.Event{Kind: activity.KindFollow})

	rec := &ingestRecorder{}
	h.SetActivity(rec)
	h.SetActivity(nil)
	if h.activityIngester() != nil {
		t.Error("SetActivity(nil) did not detach")
	}
}

func screenHub(t *testing.T, mod Moderator) *Hub {
	t.Helper()
	h := testHub(t)
	if mod != nil {
		h.SetModerator(mod)
	}
	return h
}

func TestScreenTextWithNoModeratorIsClear(t *testing.T) {
	h := screenHub(t, nil)
	if v := h.ScreenText(db.PlatformTwitch, "anything", func(Display) { t.Error("done called") }); v != DisplayClear {
		t.Errorf("verdict %v", v)
	}
	if h.ScreenName(db.PlatformTwitch, "anything") != DisplayClear {
		t.Error("name not clear")
	}
}

func TestScreenTextRemovedByTheFastCheckersIsBlockedAndNeverAsksTheModel(t *testing.T) {
	mod := &stubModerator{modelOn: true, verdict: automod.Verdict{Act: []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionDelete}}}}
	h := screenHub(t, mod)
	if v := h.ScreenText(db.PlatformTwitch, "bad", func(Display) {}); v != DisplayBlocked {
		t.Errorf("verdict %v", v)
	}
	if n, _ := mod.modelObserved(); n != 0 {
		t.Errorf("the paid model was asked about text the rules already removed (%d calls)", n)
	}
	// Empty text is nothing to judge.
	if h.ScreenText(db.PlatformTwitch, "", nil) != DisplayClear {
		t.Error("empty text")
	}
}

func TestScreenTextWithTheModelOffIsClearAtOnce(t *testing.T) {
	mod := &stubModerator{}
	h := screenHub(t, mod)
	if v := h.ScreenText(db.PlatformTwitch, "fine", func(Display) { t.Error("done called") }); v != DisplayClear {
		t.Errorf("verdict %v", v)
	}
	// And with no callback there is nobody to answer to.
	mod.mu.Lock()
	mod.modelOn = true
	mod.mu.Unlock()
	if v := h.ScreenText(db.PlatformTwitch, "fine", nil); v != DisplayClear {
		t.Errorf("verdict without a callback %v", v)
	}
}

func awaitVerdict(t *testing.T, ch chan Display) Display {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no verdict")
		return 0
	}
}

func TestScreenTextAsksTheModelOnTheWorkerAndNeverActs(t *testing.T) {
	cases := []struct {
		name  string
		mod   *stubModerator
		want  Display
		calls int
	}{
		{"clean", &stubModerator{modelOn: true}, DisplayClear, 1},
		{"removed", &stubModerator{modelOn: true, modelVerdict: automod.Verdict{Act: []automod.Finding{{Checker: automod.CheckerModel, Action: automod.ActionBan}}}}, DisplayBlocked, 1},
		{"flag only", &stubModerator{modelOn: true, modelVerdict: automod.Verdict{Findings: []automod.Finding{{Checker: automod.CheckerModel, Action: automod.ActionFlag}}}}, DisplayClear, 1},
		// FAILS CLOSED, unlike chat: an alert loses nothing by showing no words.
		{"model error", &stubModerator{modelOn: true, modelErr: errors.New("503")}, DisplayBlocked, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := screenHub(t, c.mod)
			a := &fakeAdapter{platform: db.PlatformTwitch, account: "a"}
			attach(t, h, a)
			ch := make(chan Display, 2)
			if v := h.ScreenText(db.PlatformTwitch, "words", func(d Display) { ch <- d }); v != DisplayPending {
				t.Fatalf("immediate verdict %v, want pending", v)
			}
			if got := awaitVerdict(t, ch); got != c.want {
				t.Errorf("verdict %v, want %v", got, c.want)
			}
			if n, seen := c.mod.modelObserved(); n != c.calls || seen[0] != "words" {
				t.Errorf("model calls %d %v", n, seen)
			}
			// Nothing was moderated: no action, no history entry, no flag.
			if len(h.History(10)) != 0 {
				t.Error("screening wrote to chat history")
			}
		})
	}
}

func TestScreenNameBlocksOnAnyFindingNotOnlyAPermittedRemoval(t *testing.T) {
	flagOnly := &stubModerator{verdict: automod.Verdict{Findings: []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionFlag}}}}
	h := screenHub(t, flagOnly)
	if h.ScreenName(db.PlatformTwitch, "slur") != DisplayBlocked {
		t.Error("a name the blocked terms flagged was allowed on air")
	}
	if h.ScreenName(db.PlatformTwitch, "") != DisplayClear {
		t.Error("empty name")
	}
	clean := screenHub(t, &stubModerator{})
	if clean.ScreenName(db.PlatformTwitch, "Mara") != DisplayClear {
		t.Error("a clean name was blocked")
	}
	if n, _ := flagOnly.modelObserved(); n != 0 {
		t.Error("a name must never cost a model call")
	}
}

func TestScreenWhenTheModeratorIsReplacedMidQuestion(t *testing.T) {
	// A generation replaced while a question is queued abandons it; the asker's
	// own deadline (overlay.MaxHold) is what bounds the wait.
	block := make(chan struct{})
	mod := &stubModerator{modelOn: true, modelBlock: block}
	h := screenHub(t, mod)
	called := make(chan Display, 1)
	if h.ScreenText(db.PlatformTwitch, "slow", func(d Display) { called <- d }) != DisplayPending {
		t.Fatal("not pending")
	}
	waitFor(t, "the model call to start", func() bool { n, _ := mod.modelObserved(); return n == 1 })
	h.SetModerator(nil)
	select {
	case v := <-called:
		// Cancelled mid-call: CheckModel returns ctx.Err(), which fails closed.
		if v != DisplayBlocked {
			t.Errorf("verdict %v", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled question never answered")
	}
	close(block)
}

// The screening queue is its own: filling it makes the alert show without its
// text and does not touch the queue that carries bans and timeouts.
func TestAFullScreeningQueueShowsTheAlertWithoutTextAndLeavesModerationAlone(t *testing.T) {
	block := make(chan struct{})
	mod := &stubModerator{modelOn: true, modelBlock: block}
	h := screenHub(t, mod)
	defer close(block)
	pending, blocked := 0, 0
	for i := 0; i < screenQueueDepth*2+5; i++ {
		switch h.ScreenText(db.PlatformTwitch, "words", func(Display) {}) {
		case DisplayPending:
			pending++
		case DisplayBlocked:
			blocked++
		}
	}
	if pending < screenQueueDepth || blocked == 0 {
		t.Fatalf("pending %d blocked %d: a full screening queue must answer Blocked", pending, blocked)
	}
	h.mu.Lock()
	moderation := len(h.automod.jobs)
	h.mu.Unlock()
	if moderation != 0 {
		t.Errorf("screening put %d jobs on the moderation queue", moderation)
	}
	// And a moderation action still queues, however full the screening queue is.
	h.mu.Lock()
	s := h.automod
	h.mu.Unlock()
	h.enqueueAutomod(s, automodJob{gen: s.gen, platform: db.PlatformTwitch, finding: automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionFlag}})
}

// Alert text is judged by the same shape detectors as chat: a Super Chat message
// chat would hide stays off air, though it has no author to keep a window for.
func TestAlertTextThatChatWouldHideStaysOffAir(t *testing.T) {
	limits := automod.HistoryLimits{
		Window: time.Minute, MaxMentionsPerMessage: 3, MaxLinks: 1, MinLengthForCaps: 10, MaxCapsRatio: 0.6,
		Action: automod.ActionHideLocal, Retain: 10, IdleEviction: time.Hour, MaxAuthors: 100,
	}
	m := automod.DefaultMatrix()
	m.Set(automod.Key{Platform: db.PlatformTwitch, Action: automod.ActionHideLocal, Checker: automod.CheckerHistory}, true)
	engine := automod.New(m, automod.PlatformCaps{}, nil, automod.NewHistory(limits), nil)

	cases := map[string]string{
		"shouting": "THIS IS ALL UPPER CASE TEXT",
		"mentions": "@a @b @c @d hello",
		"links":    "see http://a.example and http://b.example",
	}
	for name, text := range cases {
		// What chat does with it: the message from a real author is removed.
		if v := engine.CheckFast(db.PlatformTwitch, automod.Subject{AuthorID: "u1", Text: text}); !removes(v) {
			t.Fatalf("%s: chat would not hide %q, so this test proves nothing", name, text)
		}
		h := screenHub(t, engine)
		if got := h.ScreenText(db.PlatformTwitch, text, nil); got != DisplayBlocked {
			t.Errorf("%s: alert text %q verdict %v, want blocked", name, text, got)
		}
	}
	h := screenHub(t, engine)
	if got := h.ScreenText(db.PlatformTwitch, "thanks for the stream", nil); got != DisplayClear {
		t.Errorf("clean text: %v", got)
	}
	// Screening records nothing: the same text a hundred times is never a flood.
	for i := 0; i < 100; i++ {
		if h.ScreenText(db.PlatformTwitch, "thanks for the stream", nil) != DisplayClear {
			t.Fatal("repeated alert text was treated as a flood")
		}
	}
}
