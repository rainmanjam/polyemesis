package engine

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// This file exists because two of the three defects found in the selector's
// panic recovery (decideSource, engine.go) shipped with no permanent test:
// triggering them needs a decision that panics, and no reachable input
// produces one. They were caught by manual mutation runs that no longer
// exist. decideFn and selPanicRelog (both fields on Engine, next to
// selPanicMsg/selPanicAt -- see their comment there for why fields rather
// than package-level vars) are the seam these two tests need to drive that
// panic and its re-log window on demand, without touching production
// behaviour: both are left at their zero value -- nil, and "use
// selPanicRelogDefault" -- unless a test sets them.

// TestSwitchSourceRollsBackPinOnARecoveredPanic is fix A.
//
// Before the fix, SwitchSource committed e.sel.pinned, called
// applySourceChoice, and returned nil regardless of whether the decision
// inside it had panicked and been recovered -- logging "source selected by
// operator" first, unconditionally. What that looked like from the outside:
// an HTTP 200 to the operator who asked for "backup", a log line telling them
// it was done, a dashboard still reading "Pinned: backup / Active: primary"
// because nothing was ever put on air, and the pin LATCHED -- read back by
// every 500ms sweep, so the same panic fired again and again forever, all
// silently, because the one caller that had somebody to report to had already
// said it worked.
func TestSwitchSourceRollsBackPinOnARecoveredPanic(t *testing.T) {
	e := failoverEngine(t)
	// A buffer-backed logger, not failoverEngine's default io.Discard one,
	// because this test needs to see what SwitchSource told the operator --
	// specifically, that it did NOT tell them the switch succeeded. Assigned
	// before any goroutine of this engine's exists (failoverEngine starts
	// none, and reconcileSelector below runs synchronously on this
	// goroutine), so there is nothing to race with it.
	var buf bytes.Buffer
	e.log = slog.New(slog.NewTextHandler(&buf, nil))

	s := failoverOnSettings()
	setSettings(e, s)

	e.reconcileSelector(s, wantSelector(s), "")
	hub := e.selectorHub()
	if hub == nil {
		t.Fatal("the selector tier did not start")
	}
	t.Cleanup(func() {
		e.selMu.Lock()
		defer e.selMu.Unlock()
		e.teardownFeed(e.sel.feed)
		_ = hub.Close()
	})

	// An arbitrary pin, distinct both from sourceNone (the zero value a fresh
	// selector starts with) and from "backup" (what this test asks for), so a
	// rollback that silently left `want` in place would still be caught.
	e.mu.Lock()
	e.sel.pinned = sourceSlate
	e.mu.Unlock()

	// The seam: substitute a decision that always panics, standing in for the
	// real failure mode this machinery exists to survive -- a winning
	// candidate whose kind has no entry in the reason map, which is exactly
	// what a fourth candidate kind risks forgetting.
	e.decideFn = func(sourceChoice) (sourceKind, string) {
		panic("seam: simulated missing reason-map entry")
	}

	err := e.SwitchSource("backup")
	// errSelectorUndecided specifically, not just any error: this pins the
	// test to the recovered-panic path rather than accepting any failure
	// (e.g. an unrelated validation error) as if it proved the same thing.
	if !errors.Is(err, errSelectorUndecided) {
		t.Fatalf("SwitchSource err = %v, want errSelectorUndecided -- "+
			"a decision that panicked must be reported as undecided, not silently accepted", err)
	}

	e.mu.Lock()
	pinned := e.sel.pinned
	e.mu.Unlock()
	if pinned != sourceSlate {
		t.Errorf("pinned = %q after a recovered panic, want %q (the pre-call value, rolled back); "+
			"a pin left at %q would latch and re-panic on every later sweep",
			pinned, sourceSlate, sourceBackup)
	}

	// The other half of the defect: even with the error returned and the pin
	// rolled back, a partial regression that logged success before calling
	// applySourceChoice would leave the operator-visible lie intact. That lie
	// -- not the return value, not the pin -- was the actual complaint.
	if out := buf.String(); strings.Contains(out, "source selected by operator") {
		t.Errorf("log claims the source was selected by the operator after a recovered panic; log:\n%s", out)
	}
}

// TestPersistentPanicRelogsItsStackOncePerWindowNotOnceEver is fix B.
//
// Before the fix, decideSource's recover refreshed selPanicAt on every single
// panic rather than only when it logged the stack. Measured that way,
// time.Since(selPanicAt) never has a chance to exceed the relog window -- the
// clock restarts before it can elapse -- so the full stack trace was written
// once EVER instead of once per window. An operator opening the logs an hour
// into an incident would find nothing but stackless "panicked again" lines:
// the one record with the stack they need had long since rotated out of the
// log, and none was ever produced again to replace it.
func TestPersistentPanicRelogsItsStackOncePerWindowNotOnceEver(t *testing.T) {
	var buf bytes.Buffer
	e := &Engine{
		log:      slog.New(slog.NewTextHandler(&buf, nil)),
		sourceID: 1,
		// Shortens the relog window for this engine only -- selPanicRelog's
		// zero value would mean selPanicRelogDefault (a minute), far too long
		// for a test. A field set once here, before anything concurrent
		// exists for this engine, not a package-level var every other test
		// in this package would share and could race over.
		selPanicRelog: 150 * time.Millisecond,
	}
	e.decideFn = func(sourceChoice) (sourceKind, string) {
		panic("seam: simulated missing reason-map entry")
	}
	c := failoverChoice(sourceNone, func(*sourceChoice) {})

	// A tight loop, each gap far shorter than the window, is the shape that
	// tells the fix and the bug apart. A 500ms production ticker refreshing
	// selPanicAt on every one of these short gaps (the bug) never lets
	// time.Since(selPanicAt) exceed the window, even though the TOTAL
	// elapsed time across the run does -- that is exactly how the bug hid a
	// persistent panic behind stackless lines forever. The fix only
	// refreshes selPanicAt when it logs a stack, so accumulated real time
	// crossing the window produces a second stack regardless of how many
	// stackless calls happened in between.
	for i := 0; i < 40; i++ {
		e.decideSource(c)
		time.Sleep(5 * time.Millisecond)
	}

	out := buf.String()
	stacks := strings.Count(out, "stack=")
	again := strings.Count(out, "panicked again")

	if stacks < 2 {
		t.Errorf("stack records = %d, want at least 2 (once per window, not once ever); log:\n%s", stacks, out)
	}
	if again == 0 {
		t.Errorf("no stackless \"panicked again\" record between the stacks; log:\n%s", out)
	}
}
