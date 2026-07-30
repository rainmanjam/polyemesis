package chat

import (
	"context"
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
}

func (s *stubModerator) CheckFast(p db.Platform, authorID, text string) automod.Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.seen = append(s.seen, text)
	return s.verdict
}

func (s *stubModerator) observed() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]string(nil), s.seen...)
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
