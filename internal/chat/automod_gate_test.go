package chat

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// gatedModerator is a stubModerator that also implements the optional halves:
// the hard-case gate and the hold-on-failure setting.
type gatedModerator struct {
	*stubModerator
	want  func(automod.Subject) bool
	holds bool
	asked atomic.Int32
}

func (g *gatedModerator) WantsModel(_ db.Platform, s automod.Subject, _ automod.Verdict) bool {
	g.asked.Add(1)
	return g.want(s)
}
func (g *gatedModerator) HoldOnModelError() bool { return g.holds }

// A message the gate declines is not Pending: it reaches the overlay at once,
// and the model is never asked. One it admits is Pending as before.
func TestAGateDeclinedMessageIsClearAtOnceAndNeverAsked(t *testing.T) {
	stub := &stubModerator{modelOn: true}
	mod := &gatedModerator{stubModerator: stub, want: func(s automod.Subject) bool { return s.Text == "hard" }}
	_, sink, _ := displayHub(t, mod, msg("m1", "u1", "easy"), msg("m2", "u2", "hard"))
	sink.waitFor(t, fmt.Sprintf("offer m1 %d", DisplayClear))
	sink.waitFor(t, fmt.Sprintf("offer m2 %d", DisplayPending))
	sink.waitFor(t, fmt.Sprintf("judge m2 %d", DisplayClear))
	if n, texts := stub.modelObserved(); n != 1 || texts[0] != "hard" {
		t.Fatalf("model asked %d times about %v, want once about the hard line", n, texts)
	}
	if got := mod.asked.Load(); got != 2 {
		t.Fatalf("the gate was consulted %d times, want 2", got)
	}
}

// The gate is not consulted when the model is off or the fast verdict removed
// the message: it spends a per-author allowance when it says yes.
func TestTheGateIsNotConsultedWhenThereIsNothingToAsk(t *testing.T) {
	off := &gatedModerator{stubModerator: &stubModerator{}, want: func(automod.Subject) bool { return true }}
	_, sink, _ := displayHub(t, off, msg("m1", "u1", "hello"))
	sink.waitFor(t, fmt.Sprintf("offer m1 %d", DisplayClear))

	f := automod.Finding{Checker: automod.CheckerRules, Action: automod.ActionHideLocal}
	removed := &gatedModerator{
		stubModerator: &stubModerator{modelOn: true, verdict: automod.Verdict{Findings: []automod.Finding{f}, Act: []automod.Finding{f}}},
		want:          func(automod.Subject) bool { return true },
	}
	_, sink2, _ := displayHub(t, removed, msg("m2", "u1", "bad"))
	sink2.waitFor(t, fmt.Sprintf("offer m2 %d", DisplayBlocked))
	if off.asked.Load() != 0 || removed.asked.Load() != 0 {
		t.Fatalf("the gate was consulted: off=%d removed=%d", off.asked.Load(), removed.asked.Load())
	}
}

// A Moderator without the optional halves asks about every message, as before.
func TestAModeratorWithoutTheGateAsksAboutEverything(t *testing.T) {
	stub := &stubModerator{modelOn: true}
	_, sink, _ := displayHub(t, stub, msg("m1", "u1", "a"), msg("m2", "u2", "b"))
	sink.waitFor(t, fmt.Sprintf("judge m1 %d", DisplayClear))
	sink.waitFor(t, fmt.Sprintf("judge m2 %d", DisplayClear))
	if n, _ := stub.modelObserved(); n != 2 {
		t.Fatalf("model asked %d times, want 2", n)
	}
}

// The setting is off by default: a model outage passes the line (fail open),
// exactly as TestWithTheModelOnAMessageIsPendingUntilJudged pins.
func TestAnUnansweredQuestionPassesUnlessTheOperatorAskedToHold(t *testing.T) {
	stub := &stubModerator{modelOn: true, modelErr: errors.New("503")}
	mod := &gatedModerator{stubModerator: stub, want: func(automod.Subject) bool { return true }}
	h, sink, _ := displayHub(t, mod, msg("m1", "u1", "hello"))
	sink.waitFor(t, fmt.Sprintf("judge m1 %d", DisplayClear))
	if got := h.Flagged(db.PlatformTwitch, 10); len(got) != 0 {
		t.Fatalf("an unheld outage flagged %d messages", len(got))
	}
}

func TestHoldWhenUnavailableHoldsAndListsTheLine(t *testing.T) {
	stub := &stubModerator{modelOn: true, modelErr: errors.New("503")}
	mod := &gatedModerator{stubModerator: stub, want: func(automod.Subject) bool { return true }, holds: true}
	h, sink, _ := displayHub(t, mod, msg("m1", "u1", "hello"))
	sink.waitFor(t, fmt.Sprintf("judge m1 %d", DisplayHeld))
	waitFor(t, "the held line on the review list", func() bool { return len(h.Flagged(db.PlatformTwitch, 10)) == 1 })
	f := h.Flagged(db.PlatformTwitch, 10)[0]
	if !f.Held || len(f.Findings) != 1 {
		t.Fatalf("flag = %+v", f)
	}
	fd := f.Findings[0]
	if fd.Action != automod.ActionFlag || !fd.Hold || fd.Checker != automod.CheckerModel {
		t.Fatalf("finding = %+v", fd)
	}
	// A held line is not a removal: nothing was queued for any platform.
	time.Sleep(10 * time.Millisecond)
	if got := sink.got(); len(got) != 2 {
		t.Fatalf("display calls %v, want offer and judge only", got)
	}
}

// The hourly ceiling is a spend limit, not an outage: with hold-when-unavailable
// on, a line the model was not asked about because the budget is spent passes
// and is not listed.
func TestHoldWhenUnavailableDoesNotHoldASpentBudget(t *testing.T) {
	for name, err := range map[string]error{
		"direct":  fmt.Errorf("primary checker: %w", automod.ErrBudgetSpent),
		"chained": automodCeilingErr(t),
	} {
		t.Run(name, func(t *testing.T) {
			stub := &stubModerator{modelOn: true, modelErr: err}
			mod := &gatedModerator{stubModerator: stub, want: func(automod.Subject) bool { return true }, holds: true}
			h, sink, _ := displayHub(t, mod, msg("m1", "u1", "hello"))
			sink.waitFor(t, fmt.Sprintf("judge m1 %d", DisplayClear))
			if got := h.Flagged(db.PlatformTwitch, 10); len(got) != 0 {
				t.Fatalf("a spent budget flagged %d messages", len(got))
			}
		})
	}
}

// automodCeilingErr is what a real chain returns when its only checker is
// refused by its ceiling.
func automodCeilingErr(t *testing.T) error {
	t.Helper()
	b := automod.NewBudget()
	m := automod.NewModel(automod.ModelConfig{Enabled: true, MaxCallsPerHour: 1}, b)
	// Spend the one call allowed, against an endpoint that is not there.
	_, _ = m.Check(context.Background(), "x")
	_, err := automod.NewChain(m, nil).Check(context.Background(), "y")
	if !errors.Is(err, automod.ErrBudgetSpent) {
		t.Fatalf("setup: %v is not a spent-budget error", err)
	}
	return err
}

// A long outage cannot bury real findings: only unjudgedHoldCap lines are
// listed; the rest stay held, with no entry.
func TestHoldWhenUnavailableIsCappedOnTheReviewList(t *testing.T) {
	stub := &stubModerator{modelOn: true, modelErr: errors.New("503")}
	mod := &gatedModerator{stubModerator: stub, want: func(automod.Subject) bool { return true }, holds: true}
	var msgs []Message
	for i := 0; i < unjudgedHoldCap+10; i++ {
		msgs = append(msgs, msg(fmt.Sprintf("m%d", i), fmt.Sprintf("u%d", i), "hello"))
	}
	h, sink, _ := displayHub(t, mod, msgs...)
	// The cap bounds the LIST, not the hold: the line past it stays held off
	// the overlay (the overlay's hold TTL drops it) and is simply not listed.
	last := fmt.Sprintf("m%d", unjudgedHoldCap+9)
	sink.waitFor(t, fmt.Sprintf("judge %s %d", last, DisplayHeld))
	for _, c := range sink.got() {
		if c == fmt.Sprintf("judge %s %d", last, DisplayClear) {
			t.Fatalf("a line past the cap was released as clear: %v", c)
		}
	}
	if got := len(h.Flagged(db.PlatformTwitch, 0)); got != unjudgedHoldCap {
		t.Fatalf("%d lines listed, want the cap of %d", got, unjudgedHoldCap)
	}
}

// The cap counts lines that can still be holding something. Once an outage is
// over and the overlay gate's ten minutes have passed, the old entries no longer
// count, or one bad hour would switch the setting off for good.
func TestTheUnjudgedHoldCapForgetsFinishedOutages(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	h.now = func() time.Time { return now }
	for i := 0; i < unjudgedHoldCap; i++ {
		h.noteFlag(Message{Platform: db.PlatformTwitch, Account: "acct", ID: fmt.Sprintf("old%d", i)}, []automod.Finding{{
			Checker: automod.CheckerModel, Action: automod.ActionFlag, Category: automod.CategoryOther, Reason: unjudgedReason, Hold: true,
		}})
	}
	if got := h.unjudgedHeld(); got != unjudgedHoldCap {
		t.Fatalf("unjudgedHeld = %d, want %d", got, unjudgedHoldCap)
	}
	now = now.Add(unjudgedHeldFor + time.Second)
	if got := h.unjudgedHeld(); got != 0 {
		t.Fatalf("unjudgedHeld = %d after the outage's lines could no longer be released, want 0", got)
	}
}

// engineModerator is the stub with a REAL engine answering the gate question,
// so the tests below exercise the actual per-author cap and raid brake.
type engineModerator struct {
	*stubModerator
	eng   *automod.Engine
	holds bool
}

func (m *engineModerator) ModelOutcome(p db.Platform, s automod.Subject, v automod.Verdict) automod.GateOutcome {
	return m.eng.ModelOutcome(p, s, v)
}
func (m *engineModerator) HoldOnModelError() bool { return m.holds }

// passChecker is a model checker that answers "clean".
type passChecker struct{}

func (passChecker) Check(context.Context, string) ([]automod.Finding, error) { return nil, nil }
func (passChecker) Stats() automod.ModelStats                                { return automod.ModelStats{} }
func (passChecker) Enabled() bool                                            { return true }

const abusiveLine = "i will find you and kill you" // address + violence: a hard-case signal

func hardEngineModerator(t *testing.T, cfg automod.GateConfig, holds bool, modelErr error) *engineModerator {
	t.Helper()
	eng := automod.New(automod.DefaultMatrix(), automod.PlatformCaps{}, nil, nil, passChecker{}).
		WithModelGate(automod.ScopeHard, automod.NewModelGate(cfg))
	return &engineModerator{stubModerator: &stubModerator{modelOn: true, modelErr: modelErr}, eng: eng, holds: holds}
}

// A decoy first, then the real message inside AuthorEvery: the second was never
// judged and must not be shown as clear. With the hold on it waits; with it off
// it passes as an outage would; the review list has it either way.
func TestAGateCappedSignalLineIsNeverShownAsClear(t *testing.T) {
	for _, holds := range []bool{true, false} {
		t.Run(fmt.Sprintf("holds=%v", holds), func(t *testing.T) {
			mod := hardEngineModerator(t, automod.GateConfig{}, holds, nil)
			h, sink, _ := displayHub(t, mod, msg("decoy", "u1", abusiveLine), msg("real", "u1", abusiveLine+" tonight"))
			sink.waitFor(t, fmt.Sprintf("offer decoy %d", DisplayPending))
			if holds {
				sink.waitFor(t, fmt.Sprintf("offer real %d", DisplayHeld))
			} else {
				sink.waitFor(t, fmt.Sprintf("offer real %d", DisplayClear))
			}
			waitFor(t, "the unchecked line on the review list", func() bool { return len(h.Flagged(db.PlatformTwitch, 10)) == 1 })
			f := h.Flagged(db.PlatformTwitch, 10)[0]
			if f.Message.ID != "real" || f.Held != holds || f.Findings[0].Reason != uncheckedGateReason {
				t.Fatalf("flag = %+v", f)
			}
		})
	}
}

// Flooding past RaidMax trips the brake; the line after it was never judged
// either, and the line that tripped it was declined too.
func TestARaidBrakedSignalLineIsNeverShownAsClear(t *testing.T) {
	for _, holds := range []bool{true, false} {
		t.Run(fmt.Sprintf("holds=%v", holds), func(t *testing.T) {
			mod := hardEngineModerator(t, automod.GateConfig{RaidMax: 3}, holds, nil)
			var msgs []Message
			for i := 0; i < 5; i++ { // distinct authors: only the brake can decline
				msgs = append(msgs, msg(fmt.Sprintf("m%d", i), fmt.Sprintf("u%d", i), abusiveLine))
			}
			h, sink, _ := displayHub(t, mod, msgs...)
			want := DisplayClear
			if holds {
				want = DisplayHeld
			}
			sink.waitFor(t, fmt.Sprintf("offer m4 %d", want))
			sink.waitFor(t, fmt.Sprintf("offer m3 %d", want))
			waitFor(t, "m3 and m4 listed", func() bool { return len(h.Flagged(db.PlatformTwitch, 10)) == 2 })
			for _, f := range h.Flagged(db.PlatformTwitch, 10) {
				if f.Held != holds {
					t.Fatalf("flag %s held=%v, want %v", f.Message.ID, f.Held, holds)
				}
			}
		})
	}
}

// A spent hourly ceiling does not hold the line, but a line with a hard-case
// signal that skipped the model for it is listed for review (never held).
func TestASpentCeilingListsASignalLineWithoutHoldingIt(t *testing.T) {
	mod := hardEngineModerator(t, automod.GateConfig{}, true, fmt.Errorf("primary checker: %w", automod.ErrBudgetSpent))
	h, sink, _ := displayHub(t, mod, msg("s1", "u1", abusiveLine))
	sink.waitFor(t, fmt.Sprintf("judge s1 %d", DisplayClear))
	waitFor(t, "the listed line", func() bool { return len(h.Flagged(db.PlatformTwitch, 10)) == 1 })
	f := h.Flagged(db.PlatformTwitch, 10)[0]
	if f.Held || f.Findings[0].Reason != uncheckedBudgetReason || f.Findings[0].Hold {
		t.Fatalf("flag = %+v", f)
	}
}
