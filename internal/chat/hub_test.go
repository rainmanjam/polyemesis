package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
)

// ---------------------------------------------------------------- fake parts

// fakeAdapter is a programmable adapter. Every failure mode the Hub is
// supposed to survive is expressed by setting one field on it.
type fakeAdapter struct {
	platform db.Platform
	account  string

	// runFn replaces the default "deliver everything in messages, then block".
	runFn    func(ctx context.Context, sink Sink) error
	messages []Message

	panicOnRun  bool
	panicOnSend bool
	failWith    error
	sendErr     error
	sendEcho    Message
	noSend      bool
	health      *Health

	mu    sync.Mutex
	runs  int
	sends int
	sent  []string
}

func (f *fakeAdapter) Platform() db.Platform { return f.platform }
func (f *fakeAdapter) Account() string       { return f.account }

func (f *fakeAdapter) Run(ctx context.Context, sink Sink) error {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()

	if f.panicOnRun {
		panic("adapter exploded")
	}
	if f.runFn != nil {
		return f.runFn(ctx, sink)
	}
	for _, m := range f.messages {
		sink.Deliver(m)
	}
	if f.failWith != nil {
		return f.failWith
	}
	<-ctx.Done()
	return nil
}

func (f *fakeAdapter) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

// sendingAdapter wraps fakeAdapter with a Send, so an adapter without one can
// be built simply by not wrapping.
type sendingAdapter struct{ *fakeAdapter }

func (s sendingAdapter) Send(ctx context.Context, text string) (Message, error) {
	s.mu.Lock()
	s.sends++
	s.sent = append(s.sent, text)
	s.mu.Unlock()

	if s.panicOnSend {
		panic("send exploded")
	}
	if s.sendErr != nil {
		return Message{}, s.sendErr
	}
	return s.sendEcho, nil
}

// healthyAdapter reports its own Health, like YouTube and Kick do.
type healthyAdapter struct{ *fakeAdapter }

func (h healthyAdapter) Health() Health { return *h.health }

type recordingStore struct {
	mu     sync.Mutex
	rows   []db.ChatMessage
	err    error
	purges int
}

func (s *recordingStore) AppendChatMessages(msgs []db.ChatMessage) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	s.rows = append(s.rows, msgs...)
	return len(msgs), nil
}

func (s *recordingStore) PurgeChatMessages(time.Time, int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purges++
	return 0, nil
}

func (s *recordingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testHub(t *testing.T, opts ...Option) *Hub {
	t.Helper()
	base := []Option{
		WithLogger(quietLogger()),
		// Instant backoff: the reconnection schedule is tested by its own
		// function, and every other test would otherwise pay for it.
		WithBackoff(func(int) time.Duration { return 0 }),
		WithSleep(func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }),
	}
	h := New(append(base, opts...)...)
	t.Cleanup(h.Close)
	return h
}

// waitFor polls until cond holds, so a test never sleeps a fixed amount.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ------------------------------------------------------------------- tests

func TestOneAdapterFailingDoesNotStopAnother(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// YouTube dies immediately and keeps dying, exactly as it would at 4pm on
	// an exhausted quota.
	broken := &fakeAdapter{platform: db.PlatformYouTube, account: "yt",
		failWith: errors.New("quota exceeded")}

	delivered := make(chan Message, 8)
	healthy := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		runFn: func(ctx context.Context, sink Sink) error {
			for i := 0; ctx.Err() == nil; i++ {
				sink.Deliver(Message{ID: fmt.Sprintf("m%d", i), Text: "still here"})
				time.Sleep(time.Millisecond)
			}
			return nil
		}}

	if err := h.Attach(ctx, broken); err != nil {
		t.Fatal(err)
	}
	if err := h.Attach(ctx, healthy); err != nil {
		t.Fatal(err)
	}
	go func() {
		for _, m := range h.History(0) {
			delivered <- m
		}
	}()

	waitFor(t, "the failing adapter to have restarted several times", func() bool {
		return broken.runCount() >= 3
	})
	waitFor(t, "the healthy adapter to keep delivering", func() bool {
		return len(h.History(0)) >= 3
	})

	for _, st := range h.Statuses() {
		if st.Platform == db.PlatformTwitch && st.State == StateFailed {
			t.Fatalf("twitch was dragged down with youtube: %+v", st)
		}
	}
}

func TestAPanickingAdapterRestartsAndIsIsolated(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	boom := &fakeAdapter{platform: db.PlatformKick, account: "kk", panicOnRun: true}
	healthy := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		messages: []Message{{ID: "1", Text: "hello"}}}

	if err := h.Attach(ctx, boom); err != nil {
		t.Fatal(err)
	}
	if err := h.Attach(ctx, healthy); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the panicking adapter to be restarted", func() bool { return boom.runCount() >= 2 })
	waitFor(t, "the healthy adapter's message", func() bool { return len(h.History(0)) == 1 })

	var kick Status
	for _, st := range h.Statuses() {
		if st.Platform == db.PlatformKick {
			kick = st
		}
	}
	if kick.LastError == "" {
		t.Fatal("the panic was not recorded on the status")
	}
	if kick.Restarts == 0 {
		t.Fatal("restarts were not counted, so a flapping adapter would look healthy")
	}
}

func TestAFatalErrorStopsRetrying(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		failWith: Fatal(errors.New("twitch rejected the chat login"))}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the adapter to run once", func() bool { return a.runCount() >= 1 })
	// Give the loop every chance to retry if it were going to.
	time.Sleep(50 * time.Millisecond)
	if n := a.runCount(); n != 1 {
		t.Fatalf("ran %d times; a rejected token must not be retried in a loop", n)
	}

	st := h.Statuses()[0]
	if st.State != StateFailed || st.Detail == "" {
		t.Fatalf("status = %+v, want failed with a reason", st)
	}
}

func TestDuplicateMessagesAreDeliveredOnce(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	same := Message{ID: "abc", Text: "hello", At: time.Unix(1700000000, 0)}
	a := &fakeAdapter{platform: db.PlatformKick, account: "kk",
		messages: []Message{same, same, same}}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the first delivery", func() bool { return len(h.History(0)) >= 1 })
	time.Sleep(20 * time.Millisecond)

	if n := len(h.History(0)); n != 1 {
		t.Fatalf("history holds %d copies of one message; a webhook retry would duplicate chat", n)
	}
	if got := h.Stats().Deduped; got != 2 {
		t.Fatalf("deduped = %d, want 2", got)
	}
}

// A message the platform says is gone must leave the pane.
//
// The history ring is what the REST scrollback and every late-joining browser
// read, so a retraction that only fired an event would leave the message to
// reappear on the next reload -- which is the same bug in a slower costume.
func TestARetractedMessageLeavesTheHistory(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		runFn: func(ctx context.Context, sink Sink) error {
			sink.Deliver(Message{ID: "keep-1", Text: "fine", Author: Author{ID: "7"}})
			sink.Deliver(Message{ID: "gone", Text: "deleted later", Author: Author{ID: "9"}})
			sink.Deliver(Message{ID: "keep-2", Text: "also fine", Author: Author{ID: "7"}})
			// Only reachable because the Hub hands adapters a sink that is a
			// Retractor. A SinkFunc would drop this silently.
			retract(sink, "gone")
			<-ctx.Done()
			return nil
		}}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the retraction to apply", func() bool { return len(h.History(0)) == 2 })

	var ids []string
	for _, m := range h.History(0) {
		ids = append(ids, m.ID)
	}
	// Order matters: the ring is rebuilt compactly rather than tombstoned, and
	// a rebuild that reorders the conversation would be worse than the leak.
	if len(ids) != 2 || ids[0] != "keep-1" || ids[1] != "keep-2" {
		t.Fatalf("history = %v, want [keep-1 keep-2] in order", ids)
	}
	if got := h.Stats().Retracted; got != 1 {
		t.Fatalf("Retracted = %d, want 1", got)
	}
}

// A timeout removes everything one author said, and the platform names the
// AUTHOR rather than the messages.
func TestRetractingAUserClearsOnlyThatAuthor(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		runFn: func(ctx context.Context, sink Sink) error {
			sink.Deliver(Message{ID: "a1", Text: "hello", Author: Author{ID: "7"}})
			sink.Deliver(Message{ID: "b1", Text: "spam", Author: Author{ID: "99"}})
			sink.Deliver(Message{ID: "a2", Text: "how are you", Author: Author{ID: "7"}})
			sink.Deliver(Message{ID: "b2", Text: "more spam", Author: Author{ID: "99"}})
			retractUser(sink, "99")
			<-ctx.Done()
			return nil
		}}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the timeout to apply", func() bool { return len(h.History(0)) == 2 })
	time.Sleep(20 * time.Millisecond)

	for _, m := range h.History(0) {
		if m.Author.ID == "99" {
			t.Fatalf("history still holds %q from the timed-out author", m.ID)
		}
	}
	if n := len(h.History(0)); n != 2 {
		t.Fatalf("history holds %d messages, want the 2 from the untouched author", n)
	}
	if got := h.Stats().Retracted; got != 2 {
		t.Fatalf("Retracted = %d, want 2 (both of that author's messages)", got)
	}
}

// An empty author id is the platform clearing the whole room, not a no-op.
func TestRetractingAnEmptyUserClearsTheRoom(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		runFn: func(ctx context.Context, sink Sink) error {
			sink.Deliver(Message{ID: "a1", Text: "one", Author: Author{ID: "7"}})
			sink.Deliver(Message{ID: "b1", Text: "two", Author: Author{ID: "99"}})
			retractUser(sink, "")
			<-ctx.Done()
			return nil
		}}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the room to clear", func() bool { return len(h.History(0)) == 0 })
}

// A retraction naming a message from a DIFFERENT platform must not touch ours.
//
// Message ids are platform-scoped and nothing guarantees they are unique across
// platforms, so an id-only match would let a Twitch deletion silently remove a
// Kick message that happened to share a string.
func TestARetractionIsScopedToItsOwnPlatform(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kick := &fakeAdapter{platform: db.PlatformKick, account: "kk",
		messages: []Message{{ID: "shared-id", Text: "kick message"}}}
	if err := h.Attach(ctx, kick); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the kick message", func() bool { return len(h.History(0)) == 1 })

	tw := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		runFn: func(ctx context.Context, sink Sink) error {
			sink.Deliver(Message{ID: "tw-1", Text: "twitch message"})
			retract(sink, "shared-id") // same id, different platform
			<-ctx.Done()
			return nil
		}}
	if err := h.Attach(ctx, tw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the twitch message", func() bool { return len(h.History(0)) == 2 })
	time.Sleep(20 * time.Millisecond)

	var found bool
	for _, m := range h.History(0) {
		if m.Platform == db.PlatformKick && m.ID == "shared-id" {
			found = true
		}
	}
	if !found {
		t.Fatal("a Twitch retraction deleted a Kick message that merely shared an id")
	}
}

func TestMessagesInheritTheAdaptersPlatformAndAccount(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "acct-7",
		messages: []Message{{ID: "1", Text: "hi"}}}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the message", func() bool { return len(h.History(0)) == 1 })

	got := h.History(0)[0]
	if got.Platform != db.PlatformTwitch || got.Account != "acct-7" {
		t.Fatalf("message = %+v, want the adapter's platform and account", got)
	}
}

func TestHistoryIsBoundedAndOrderedOldestFirst(t *testing.T) {
	h := testHub(t, WithHistory(3))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var msgs []Message
	for i := 0; i < 6; i++ {
		msgs = append(msgs, Message{ID: fmt.Sprintf("%d", i), Text: fmt.Sprintf("m%d", i)})
	}
	if err := h.Attach(ctx, &fakeAdapter{platform: db.PlatformTwitch, account: "tw", messages: msgs}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all six messages", func() bool { return h.Stats().Received == 6 })

	got := h.History(0)
	if len(got) != 3 {
		t.Fatalf("history holds %d, want the ring size of 3", len(got))
	}
	if got[0].Text != "m3" || got[2].Text != "m5" {
		t.Fatalf("history = %q..%q, want m3..m5 oldest first", got[0].Text, got[2].Text)
	}
}

func TestPartialSendFailureReportsEveryPlatformSeparately(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	good := sendingAdapter{&fakeAdapter{platform: db.PlatformTwitch, account: "tw"}}
	bad := sendingAdapter{&fakeAdapter{platform: db.PlatformKick, account: "kk",
		sendErr: errors.New("kick said no")}}
	exploding := sendingAdapter{&fakeAdapter{platform: db.PlatformYouTube, account: "yt", panicOnSend: true}}
	readOnly := &fakeAdapter{platform: db.PlatformFacebook, account: "fb"}

	for _, a := range []Adapter{good, bad, exploding, readOnly} {
		if err := h.Attach(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	results, err := h.Send(ctx, "hello everyone")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("got %d results, want one per platform", len(results))
	}

	by := map[db.Platform]SendResult{}
	for _, r := range results {
		by[r.Platform] = r
	}
	if !by[db.PlatformTwitch].OK {
		t.Fatalf("twitch = %+v, want ok", by[db.PlatformTwitch])
	}
	if by[db.PlatformKick].OK || by[db.PlatformKick].Detail == "" {
		t.Fatalf("kick = %+v, want a failure with a reason", by[db.PlatformKick])
	}
	if by[db.PlatformYouTube].OK {
		t.Fatalf("youtube = %+v, want the panic reported as a failure", by[db.PlatformYouTube])
	}
	if !by[db.PlatformFacebook].Skipped {
		t.Fatalf("facebook = %+v, want skipped rather than failed", by[db.PlatformFacebook])
	}
	if by[db.PlatformFacebook].Detail == "" {
		t.Fatal("a receive-only platform must say why it was skipped")
	}
}

func TestSendEchoIsShownLocallyWhenThePlatformWillNotEchoIt(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	withEcho := sendingAdapter{&fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		sendEcho: Message{ID: "sent-1", Text: "hello"}}}
	silent := sendingAdapter{&fakeAdapter{platform: db.PlatformKick, account: "kk"}}

	if err := h.Attach(ctx, withEcho); err != nil {
		t.Fatal(err)
	}
	if err := h.Attach(ctx, silent); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Send(ctx, "hello"); err != nil {
		t.Fatal(err)
	}

	hist := h.History(0)
	if len(hist) != 1 {
		t.Fatalf("history holds %d messages, want only the one echo", len(hist))
	}
	if !hist[0].Echo {
		t.Fatal("the locally rendered copy was not marked as an echo")
	}
}

func TestSendWithNothingConnectedIsAnErrorNotAnEmptySuccess(t *testing.T) {
	h := testHub(t)
	if _, err := h.Send(context.Background(), "hello"); err == nil {
		t.Fatal("sending with no platforms connected reported success")
	}
	if _, err := h.Send(context.Background(), "   "); err == nil {
		t.Fatal("sending whitespace reported success")
	}
}

func TestAttachRefusesADuplicateAccount(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw"}
	b := &fakeAdapter{platform: db.PlatformTwitch, account: "tw"}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := h.Attach(ctx, b); err == nil {
		t.Fatal("attaching the same account twice was allowed; every message would appear twice")
	}

	// A second account on the same platform is the supported case and must
	// still work.
	if err := h.Attach(ctx, &fakeAdapter{platform: db.PlatformTwitch, account: "other"}); err != nil {
		t.Fatalf("a second Twitch account was refused: %v", err)
	}
}

func TestDetachStopsOnlyThatAdapter(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw"}
	b := &fakeAdapter{platform: db.PlatformKick, account: "kk"}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := h.Attach(ctx, b); err != nil {
		t.Fatal(err)
	}

	h.Detach(db.PlatformTwitch, "tw")
	if got := h.Statuses(); len(got) != 1 || got[0].Platform != db.PlatformKick {
		t.Fatalf("statuses = %+v, want only kick", got)
	}
	// Detaching something absent is a no-op, not a panic.
	h.Detach(db.PlatformYouTube, "nope")
}

func TestAdapterHealthOverridesTheHubsView(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := QuotaStatus{Used: 9900, Limit: 10000, Remaining: 100, Paused: true, Estimated: true}
	hl := Health{State: StateDegraded, Detail: "quota spent until midnight", Quota: &q}
	a := healthyAdapter{&fakeAdapter{platform: db.PlatformYouTube, account: "yt", health: &hl}}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the adapter's own health to surface", func() bool {
		st := h.Statuses()
		return len(st) == 1 && st[0].State == StateDegraded
	})
	st := h.Statuses()[0]
	if st.Detail != "quota spent until midnight" {
		t.Fatalf("detail = %q, want the adapter's own words", st.Detail)
	}
	if st.Quota == nil || !st.Quota.Paused {
		t.Fatal("the quota report did not reach the status")
	}
}

func TestMessagesArePersistedAndAStoreFailureDoesNotStopChat(t *testing.T) {
	store := &recordingStore{}
	h := testHub(t, WithStore(store), WithFlush(5*time.Millisecond, 1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		messages: []Message{{ID: "1", Text: "one"}, {ID: "2", Text: "two"}}}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both messages to be stored", func() bool { return store.count() == 2 })

	// Now the database starts refusing writes. Chat must carry on.
	store.mu.Lock()
	store.err = errors.New("database is locked")
	store.mu.Unlock()

	h.Detach(db.PlatformTwitch, "tw")
	b := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		messages: []Message{{ID: "3", Text: "three"}}}
	if err := h.Attach(ctx, b); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the live message despite the broken store", func() bool {
		for _, m := range h.History(0) {
			if m.Text == "three" {
				return true
			}
		}
		return false
	})
}

func TestPublishedEventsCarryMessagesAndState(t *testing.T) {
	broker := events.NewBroker()
	sub := broker.Subscribe(events.TypeChat, events.TypeChatState)
	defer sub.Close()

	h := testHub(t, WithPublisher(broker))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		messages: []Message{{ID: "1", Text: "hello bus"}}}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}

	var sawChat, sawState bool
	deadline := time.After(2 * time.Second)
	for !sawChat || !sawState {
		select {
		case ev := <-sub.C:
			switch ev.Type {
			case events.TypeChat:
				m, ok := ev.Data.(Message)
				if !ok || m.Text != "hello bus" {
					t.Fatalf("chat event carried %#v", ev.Data)
				}
				sawChat = true
			case events.TypeChatState:
				if _, ok := ev.Data.([]Status); !ok {
					t.Fatalf("state event carried %#v", ev.Data)
				}
				sawState = true
			}
		case <-deadline:
			t.Fatalf("timed out; sawChat=%v sawState=%v", sawChat, sawState)
		}
	}
}

func TestEmptyMessagesNeverReachTheHistory(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		messages: []Message{{ID: "1", Text: "   "}, {ID: "2", Text: "\x01\x02"}, {ID: "3", Text: "real"}}}
	if err := h.Attach(ctx, a); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the real message", func() bool { return len(h.History(0)) == 1 })
	time.Sleep(20 * time.Millisecond)

	got := h.History(0)
	if len(got) != 1 || got[0].Text != "real" {
		t.Fatalf("history = %+v, want only the real message", got)
	}
}

func TestDefaultBackoffEscalatesAndIsCapped(t *testing.T) {
	h := New(WithLogger(quietLogger()))
	defer h.Close()

	prev := time.Duration(0)
	for attempt := 0; attempt < 4; attempt++ {
		d := h.defaultBackoff(attempt)
		if d <= prev {
			t.Fatalf("attempt %d waited %s, not longer than the previous %s", attempt, d, prev)
		}
		prev = d
	}
	for _, attempt := range []int{6, 20, 1000} {
		if d := h.defaultBackoff(attempt); d > DefaultMaxBackoff {
			t.Fatalf("attempt %d waited %s, past the %s ceiling", attempt, d, DefaultMaxBackoff)
		}
	}
}

func TestCloseIsIdempotentAndStopsEverything(t *testing.T) {
	store := &recordingStore{}
	h := New(WithLogger(quietLogger()), WithStore(store),
		WithSleep(func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }))

	a := &fakeAdapter{platform: db.PlatformTwitch, account: "tw",
		messages: []Message{{ID: "1", Text: "last words"}}}
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the message", func() bool { return len(h.History(0)) == 1 })

	h.Close()
	h.Close()

	// Closing flushes: the final seconds of a broadcast are what gets scrolled
	// back to.
	if store.count() != 1 {
		t.Fatalf("stored %d messages after close, want the pending one flushed", store.count())
	}
	if err := h.Attach(context.Background(), &fakeAdapter{platform: db.PlatformKick, account: "kk"}); err == nil {
		t.Fatal("attaching to a closed hub was allowed")
	}
}

func TestHubAdapterReachesCapabilitiesTheInterfaceDoesNotCarry(t *testing.T) {
	// Kick's webhook receiver and Facebook's live-video id are both set after
	// attach, by whoever learns them. Without this lookup the caller would keep
	// its own map, which goes stale the first time a runner fails fatally.
	hub := New()
	t.Cleanup(hub.Close)

	a := &fakeAdapter{platform: db.PlatformKick, account: "42"}
	if err := hub.Attach(context.Background(), a); err != nil {
		t.Fatalf("attach: %v", err)
	}

	tests := []struct {
		name     string
		platform db.Platform
		account  string
		want     bool
	}{
		{"the attached adapter comes back", db.PlatformKick, "42", true},
		{"a different account on the same platform is absent", db.PlatformKick, "43", false},
		{"a platform that was never attached is absent", db.PlatformTwitch, "42", false},
		{"detached is absent rather than an error", db.PlatformYouTube, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := hub.Adapter(tc.platform, tc.account)
			if ok != tc.want {
				t.Fatalf("Adapter(%q, %q) ok = %v, want %v", tc.platform, tc.account, ok, tc.want)
			}
			if tc.want && got != Adapter(a) {
				t.Fatalf("Adapter returned %#v, want the attached adapter", got)
			}
		})
	}

	hub.Detach(db.PlatformKick, "42")
	if _, ok := hub.Adapter(db.PlatformKick, "42"); ok {
		t.Fatal("a detached adapter is still reachable")
	}
}

// The settings defaults must equal the constants this package falls back to.
//
// Two places name the same numbers: db.DefaultSettings().Chat, which a fresh
// install stores, and the Default* constants here, which every Hub built
// without settings uses -- that is every test, and any caller that forgets to
// apply the stored values. If they drift, the two behave differently while both
// claiming to be "the default", and it surfaces as "my chat history is shorter
// than the settings page says".
//
// The test lives here rather than in internal/db because internal/chat imports
// internal/db; the reverse import would be a cycle.
func TestChatDefaultsMatchTheSettingsDefaults(t *testing.T) {
	s := db.DefaultSettings().Chat
	if want := int(DefaultRetention / time.Hour); s.RetentionHours != want {
		t.Errorf("settings default retention = %dh, DefaultRetention = %dh", s.RetentionHours, want)
	}
	if s.KeepMessages != DefaultRetentionKeep {
		t.Errorf("settings default keep = %d, DefaultRetentionKeep = %d", s.KeepMessages, DefaultRetentionKeep)
	}
	if want := int(DefaultPurgeEvery / time.Minute); s.PurgeMinutes != want {
		t.Errorf("settings default purge = %dm, DefaultPurgeEvery = %dm", s.PurgeMinutes, want)
	}
}

// Retention has to be changeable on a RUNNING Hub. A setting that only takes
// effect after a restart is one an operator changes, sees nothing happen, and
// changes again.
func TestSetRetentionAppliesWithoutARestart(t *testing.T) {
	h := testHub(t)

	h.SetRetention(48*time.Hour, 100000, 30*time.Minute)
	h.mu.Lock()
	gotAge, gotKeep, gotEvery := h.retention, h.retainKeep, h.purgeEvery
	h.mu.Unlock()

	if gotAge != 48*time.Hour || gotKeep != 100000 || gotEvery != 30*time.Minute {
		t.Fatalf("retention = %v/%d/%v, want 48h/100000/30m", gotAge, gotKeep, gotEvery)
	}
}

// Keep-forever must not become purge-everything.
//
// The Hub spells "forever" as a non-positive duration, and the API converts 0
// hours into that. Getting this backwards would turn the setting an operator
// picks to KEEP all their chat into the one that deletes it on the next sweep,
// which is the worst available failure for this feature.
func TestKeepForeverPurgesNothing(t *testing.T) {
	h := testHub(t)
	purged := false
	h.store = &purgeSpy{onPurge: func() { purged = true }}

	h.SetRetention(-1, 2000, time.Minute)
	h.purge()
	if purged {
		t.Fatal("a keep-forever retention still ran a purge; that setting DELETES the history it promises to keep")
	}

	// And the opposite, so the test is not passing because purging never works.
	h.SetRetention(time.Hour, 2000, time.Minute)
	h.purge()
	if !purged {
		t.Fatal("a finite retention never purged, so the check above proves nothing")
	}
}

// purgeSpy is a Store that records whether the sweep ran.
type purgeSpy struct{ onPurge func() }

func (p *purgeSpy) AppendChatMessages([]db.ChatMessage) (int, error) { return 0, nil }
func (p *purgeSpy) PurgeChatMessages(time.Time, int) (int, error) {
	p.onPurge()
	return 0, nil
}
