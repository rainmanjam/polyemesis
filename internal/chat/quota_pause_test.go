package chat

import (
	"testing"
	"time"
)

// RAISING THE ALLOWANCE ON A CHAT THAT ALREADY STOPPED HAS TO RESTART IT.
//
// This is the whole reason the feature exists. Chat runs out at four in the
// afternoon, the operator goes to the Google Cloud console, is granted more,
// comes back to Settings, types the new number and saves. If that does not
// resume polling, the operator has done everything right and watched nothing
// happen, and the only remedy left is a restart nobody told them about.
//
// It did not resume. budget carried a stored paused flag; setLimits wrote the
// limit and the reserve and never touched it; and the only thing that cleared
// it was intervalFor, which the adapter stops calling the moment it parks in
// waitForQuota. The status the UI read said Paused=true and Remaining=990,000
// in the same breath -- a summary disagreeing with the numbers it summarises.
// There is no flag now, so there is nothing to disagree.
func TestRaisingTheAllowanceResumesAChatThatStoppedOnAnExhaustedQuota(t *testing.T) {
	now := atPacific(2026, 3, 1, 16, 0)
	b := newBudget(10_000, 200, fixedClock(now))

	// YouTube answered quotaExceeded, which is the authority whatever our own
	// tally said. This is the state an operator finds chat in at 4pm.
	b.pause()
	if st := b.status(); !st.Paused {
		t.Fatalf("setup: a budget the platform refused reported %+v, want paused", st)
	}
	if _, ok := b.intervalFor(5*time.Second, 1); ok {
		t.Fatal("setup: polling continued after the platform said the quota was gone")
	}

	// The operator raises the allowance and saves. Nothing else changes -- in
	// particular the clock does not advance, because the whole complaint is
	// that waiting until midnight Pacific was the only thing that worked.
	b.setLimits(1_000_000, 200)

	st := b.status()
	if st.Paused {
		t.Fatalf("chat stayed paused after the allowance was raised to 1,000,000: %+v "+
			"-- Remaining=%d says there is plenty to read with", st, st.Remaining)
	}
	if st.Remaining <= QuotaCostListMessages {
		t.Fatalf("remaining = %d after a hundredfold raise, want room for many reads", st.Remaining)
	}
	if _, ok := b.intervalFor(5*time.Second, 1); !ok {
		t.Fatal("polling did not resume after the allowance was raised; " +
			"the operator's only remaining remedy is a restart")
	}
}

// POSITIVE CONTROL for the test above, and the reason it is worth anything.
//
// "Paused" is now derived, and a derivation that never fires -- a hardcoded
// false, a comparison written < 0 instead of <= 0 -- would satisfy the resume
// test completely while reporting a spent budget as healthy, which is the
// opposite failure and the worse one: chat would keep polling an endpoint that
// returns nothing but 403 for the rest of the day. So the boundary is pinned
// from both sides, one unit apart.
func TestAPauseIsStillReportedWhenTheBudgetIsGenuinelySpent(t *testing.T) {
	now := atPacific(2026, 3, 1, 16, 0)

	// Exactly the reserve left: not one more read is affordable.
	spent := newBudget(1_000, 100, fixedClock(now))
	spent.spend(900)
	if st := spent.status(); !st.Paused {
		t.Fatalf("a budget with only the send reserve left reported %+v, want paused", st)
	}

	// Five units further back is one read, and one read is not a pause.
	running := newBudget(1_000, 100, fixedClock(now))
	running.spend(895)
	if st := running.status(); st.Paused {
		t.Fatalf("a budget with a read still affordable reported %+v, want running", st)
	}
}

// The reset is the other way a pause ends, and it has to end the same way --
// by the numbers, not by someone remembering to clear something. A stored flag
// needed rollLocked to unset it; a derived one is simply false again once used
// goes back to zero.
func TestThePauseEndsAtTheResetWithoutAnythingClearingIt(t *testing.T) {
	clock := atPacific(2026, 3, 1, 23, 0)
	b := newBudget(1_000, 100, func() time.Time { return clock })
	b.pause()
	if st := b.status(); !st.Paused {
		t.Fatalf("setup: %+v, want paused", st)
	}

	clock = atPacific(2026, 3, 2, 0, 1)
	st := b.status()
	if st.Paused {
		t.Fatalf("the pause outlived the reset: %+v", st)
	}
	if st.Used != 0 {
		t.Fatalf("used = %d after the reset, want a clean day", st.Used)
	}
}
