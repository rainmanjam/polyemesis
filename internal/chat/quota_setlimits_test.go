package chat

import (
	"testing"
	"time"
)

// A raised allowance has to reach the PACING, not just the struct field.
//
// #732's first fix set the number and #733's reload entry claims it is live.
// Both are satisfied by a setter that stores an int, which is why the assertion
// here is on the poll interval intervalFor computes: that is the only thing an
// operator can observe, and the only thing "paces against the new allowance"
// can honestly mean.
func TestSetLimitsChangesThePacingAndNotJustTheField(t *testing.T) {
	// A fixed clock, six hours before the reset, so the sustainable spacing is
	// arithmetic rather than a race with wall time.
	reset := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return reset.Add(-6 * time.Hour) }

	b := newBudget(10_000, 200, now)
	b.resetAt = reset

	before, ok := b.intervalFor(0, 1)
	if !ok {
		t.Fatal("a fresh budget refused to poll at all")
	}

	b.setLimits(1_000_000, 200)

	after, ok := b.intervalFor(0, 1)
	if !ok {
		t.Fatal("a hundredfold larger allowance refused to poll")
	}
	if after >= before {
		t.Fatalf("raising the allowance 10,000 -> 1,000,000 did not speed up polling: %s then %s", before, after)
	}
}

// THE OTHER DIRECTION, so the pair pins the sign and not just the movement.
// A setLimits that always widened the interval, or one that clamped to a floor
// the moment it was called, would satisfy the test above on its own. Lowering
// the allowance has to slow polling back down, and the limit is the only thing
// that changed between the two readings.
func TestSetLimitsSlowsPollingBackDown(t *testing.T) {
	reset := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return reset.Add(-6 * time.Hour) }

	b := newBudget(1_000_000, 200, now)
	b.resetAt = reset
	fast, _ := b.intervalFor(0, 1)

	b.setLimits(10_000, 200)
	slow, _ := b.intervalFor(0, 1)

	if slow <= fast {
		t.Fatalf("lowering the allowance did not slow polling: %s then %s", fast, slow)
	}
}

// A reserve at or above the allowance would pause reading forever. newBudget
// already refuses it; setLimits has to refuse it identically, because the two
// disagreeing is exactly how an install ends up with working chat that stops
// the moment someone saves the settings page without changing anything.
func TestSetLimitsRefusesAReserveThatSwallowsTheAllowance(t *testing.T) {
	b := newBudget(10_000, 200, nil)

	b.setLimits(5_000, 5_000)

	b.mu.Lock()
	limit, reserve := b.limit, b.reserve
	b.mu.Unlock()

	if reserve != 0 {
		t.Fatalf("a reserve equal to the allowance was kept: limit %d reserve %d", limit, reserve)
	}
	if limit != 5_000 {
		t.Fatalf("the allowance was not applied: %d", limit)
	}

	// And it agrees with construction, which is the whole point of clampQuota.
	c := newBudget(5_000, 5_000, nil)
	c.mu.Lock()
	cLimit, cReserve := c.limit, c.reserve
	c.mu.Unlock()
	if cLimit != limit || cReserve != reserve {
		t.Fatalf("setLimits and newBudget disagree from the same numbers: set(%d,%d) new(%d,%d)", limit, reserve, cLimit, cReserve)
	}
}

// Zero means "unset", not "no allowance", everywhere else in this package.
func TestSetLimitsTreatsZeroAsTheDefaultAllowance(t *testing.T) {
	b := newBudget(50_000, 200, nil)
	b.setLimits(0, 0)

	b.mu.Lock()
	limit := b.limit
	b.mu.Unlock()

	if limit != DefaultQuotaUnits {
		t.Fatalf("setLimits(0) gave %d, want the default %d", limit, DefaultQuotaUnits)
	}
}

// Today's spend survives a settings save. Clearing it would let an operator
// mint a fresh allowance by pressing Save, and YouTube's own counter would not
// play along -- reads would stop with no warning, which is the failure the
// reserve exists to prevent.
func TestSetLimitsDoesNotForgetWhatWasAlreadySpent(t *testing.T) {
	b := newBudget(10_000, 200, nil)
	b.spend(4_000)

	b.setLimits(20_000, 200)

	b.mu.Lock()
	used := b.used
	b.mu.Unlock()

	if used != 4_000 {
		t.Fatalf("spend was reset by a settings save: used %d, want 4000", used)
	}
}
