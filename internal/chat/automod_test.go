package chat

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// stubModerator returns whatever the test wants, and records what it was asked.
type stubModerator struct {
	mu      sync.Mutex
	verdict automod.Verdict
	seen    []string
	calls   int

	// The model half. Off unless a test turns it on, so the existing tests
	// exercise the fast path alone.
	modelOn      bool
	modelVerdict automod.Verdict
	modelSeen    []string
	modelCalls   int
	// modelBlock, when set, holds CheckModel until it is closed or the context
	// is cancelled -- which is how a real HTTP client behaves and what lets a
	// test observe a worker mid-call.
	modelBlock chan struct{}
}

func (s *stubModerator) CheckFast(p db.Platform, authorID, text string) automod.Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.seen = append(s.seen, text)
	return s.verdict
}

func (s *stubModerator) CheckModel(ctx context.Context, p db.Platform, text string) (automod.Verdict, error) {
	s.mu.Lock()
	s.modelCalls++
	s.modelSeen = append(s.modelSeen, text)
	block := s.modelBlock
	v := s.modelVerdict
	s.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return automod.Verdict{}, ctx.Err()
		}
	}
	return v, nil
}

func (s *stubModerator) ModelEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelOn
}

func (s *stubModerator) ModelStats() automod.ModelStats { return automod.ModelStats{} }

func (s *stubModerator) observed() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]string(nil), s.seen...)
}

func (s *stubModerator) modelObserved() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelCalls, append([]string(nil), s.modelSeen...)
}

func attach(t *testing.T, h *Hub, a *fakeAdapter) {
	t.Helper()
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
}

// THE property. A message reaches the history ring whether or not automod likes
// it, because it is published BEFORE any check runs. Blocking display on a
// check is how chat starts feeling broken; a verdict retracts afterwards.
func TestAMessageIsDeliveredBeforeAutomodSeesIt(t *testing.T) {
	h := testHub(t)
	mod := &stubModerator{verdict: automod.Verdict{
		Findings: []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionDelete, Reason: "test"}},
		// Act empty: found, but the matrix permitted nothing.
	}}
	h.SetModerator(mod)

	attach(t, h, &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1", Name: "someone"}, Text: "hello"}},
	})

	waitFor(t, "the message to reach history", func() bool {
		return len(h.History(10)) == 1
	})
	waitFor(t, "automod to have looked", func() bool {
		n, _ := mod.observed()
		return n == 1
	})

	_, seen := mod.observed()
	if len(seen) != 1 || seen[0] != "hello" {
		t.Fatalf("automod saw %v, want [hello]", seen)
	}
}

// With the matrix permitting nothing, a finding must produce NO action. This is
// what stands between an operator and an accidental auto-ban.
func TestNothingIsActedOnWhenTheMatrixPermitsNothing(t *testing.T) {
	h := testHub(t)
	a := &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "spam"}},
	}
	h.SetModerator(&stubModerator{verdict: automod.Verdict{
		Findings: []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionBan, Reason: "test"}},
		Act:      nil,
	}})
	attach(t, h, a)

	waitFor(t, "the message to arrive", func() bool { return len(h.History(10)) == 1 })
	// Give the worker every chance to do the wrong thing.
	time.Sleep(150 * time.Millisecond)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sends != 0 {
		t.Fatalf("an action was performed with the matrix permitting nothing: %d sends", a.sends)
	}
}

// A Hub with no moderator must behave exactly as it did before automod existed.
func TestNoModeratorMeansNoChange(t *testing.T) {
	h := testHub(t)
	attach(t, h, &fakeAdapter{
		platform: db.PlatformKick, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "hello"}},
	})
	waitFor(t, "the message to arrive", func() bool { return len(h.History(10)) == 1 })
}

// The queue is bounded and DROPS rather than blocking. Blocking would put a
// network call on the adapter's read loop, and under a raid -- exactly when it
// matters -- that stalls chat for every viewer.
func TestTheActionQueueDropsRatherThanBlocking(t *testing.T) {
	h := testHub(t)

	// A moderator that always asks for an action, so the queue fills.
	h.SetModerator(&stubModerator{verdict: automod.Verdict{
		Findings: []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionHideLocal}},
		Act:      []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionHideLocal}},
	}})

	msgs := make([]Message, 0, automodQueueDepth*3)
	for i := 0; i < cap(msgs); i++ {
		msgs = append(msgs, Message{
			ID:     string(rune('a'+i%26)) + time.Now().Format("150405.000000000"),
			Author: Author{ID: "u1"}, Text: "flood",
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		attach(t, h, &fakeAdapter{platform: db.PlatformTwitch, account: "acct", messages: msgs})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery blocked on the automod queue; it must drop instead")
	}
}

// Flag is recorded and sends nothing: it is the one action that changes nothing
// an audience can see, which is why it is the only default.
func TestFlagPerformsNoPlatformAction(t *testing.T) {
	h := testHub(t)
	a := &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "hmm"}},
	}
	h.SetModerator(&stubModerator{verdict: automod.Verdict{
		Findings: []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionFlag}},
		Act:      []automod.Finding{{Checker: automod.CheckerRules, Action: automod.ActionFlag}},
	}})
	attach(t, h, a)

	waitFor(t, "the message to arrive", func() bool { return len(h.History(10)) == 1 })
	time.Sleep(100 * time.Millisecond)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sends != 0 {
		t.Fatalf("flag sent something to the platform: %d sends", a.sends)
	}
}

// The model checker has a production caller.
//
// This is the regression test for the wiring bug that shipped in the first
// draft: internal/chat declared only CheckFast, so an operator could enable the
// model, paste a key, see "configured" in the UI, and never get a single call.
// Nothing failed -- which is exactly what made it expensive.
func TestTheModelCheckerIsActuallyCalled(t *testing.T) {
	h := testHub(t)
	mod := &stubModerator{modelOn: true}
	h.SetModerator(mod)

	attach(t, h, &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "ask the model about this"}},
	})

	waitFor(t, "the model to be asked", func() bool {
		n, _ := mod.modelObserved()
		return n == 1
	})
	_, seen := mod.modelObserved()
	if seen[0] != "ask the model about this" {
		t.Fatalf("the model was asked about %q, want the message text", seen[0])
	}
}

// With the model off, it must not be called at all: it is the one checker that
// costs money per message, and a raid is when that bill is largest.
func TestTheModelIsNotCalledWhenDisabled(t *testing.T) {
	h := testHub(t)
	mod := &stubModerator{modelOn: false}
	h.SetModerator(mod)

	attach(t, h, &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
		messages: []Message{{ID: "m1", Author: Author{ID: "u1"}, Text: "hello"}},
	})

	waitFor(t, "the fast checkers to run", func() bool { n, _ := mod.observed(); return n == 1 })
	time.Sleep(100 * time.Millisecond)
	if n, _ := mod.modelObserved(); n != 0 {
		t.Fatalf("the model was called %d times with the model disabled", n)
	}
}

// THE kill switch property. Turning automod off must abandon what is already
// queued, not merely stop new decisions.
//
// An operator reaches for the switch mid-incident, and one that lets a queue of
// bans keep draining afterwards is not a kill switch -- it is a delay.
func TestTurningAutomodOffAbandonsQueuedWork(t *testing.T) {
	h := testHub(t)
	release := make(chan struct{})
	defer close(release)

	mod := &stubModerator{modelOn: true, modelBlock: release}
	h.SetModerator(mod)

	const queued = 6
	msgs := make([]Message, 0, queued)
	for i := 0; i < queued; i++ {
		msgs = append(msgs, Message{
			ID:     string(rune('a' + i)),
			Author: Author{ID: "u1"}, Text: "flood",
		})
	}
	attach(t, h, &fakeAdapter{platform: db.PlatformTwitch, account: "acct", messages: msgs})

	// Everything delivered, and the worker held inside the first model call
	// with the rest waiting behind it.
	waitFor(t, "every message to be delivered", func() bool { return len(h.History(20)) == queued })
	waitFor(t, "the worker to be inside the model call", func() bool {
		n, _ := mod.modelObserved()
		return n == 1
	})

	// The switch. It must return rather than block behind the in-flight call,
	// because the generation's context cancels it.
	done := make(chan struct{})
	go func() { defer close(done); h.SetModerator(nil) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SetModerator(nil) blocked behind an in-flight call")
	}

	// Give an abandoned worker every chance to keep going.
	time.Sleep(150 * time.Millisecond)
	if n, _ := mod.modelObserved(); n != 1 {
		t.Fatalf("%d model calls after the kill switch; the queue kept draining", n)
	}
}

// Reconfiguration must not leak a worker per call. Settings are re-applied on
// every save, so this runs far more often than an operator would guess.
func TestReconfiguringDoesNotLeakWorkers(t *testing.T) {
	h := testHub(t)
	before := runtime.NumGoroutine()

	for i := 0; i < 25; i++ {
		h.SetModerator(&stubModerator{})
	}
	h.SetModerator(nil)

	// SetModerator waits for the previous worker, so no polling is needed --
	// but the runtime needs a moment to retire them.
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines grew from %d to %d over 25 reconfigurations", before, after)
	}
}
