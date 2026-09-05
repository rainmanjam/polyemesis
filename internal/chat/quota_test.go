package chat

import (
	"testing"
	"time"
)

// atPacific builds a clock at a wall time in the quota's own timezone, which is
// the only way the reset arithmetic can be stated without ambiguity.
func atPacific(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, pacific())
}

func TestQuotaResetIsTheNextMidnightPacific(t *testing.T) {
	tests := []struct {
		name string
		from time.Time
		want time.Time
	}{
		{
			name: "just after midnight resets tomorrow, not in a second",
			from: atPacific(2026, 3, 1, 0, 1),
			want: atPacific(2026, 3, 2, 0, 0),
		},
		{
			name: "late evening resets in hours",
			from: atPacific(2026, 3, 1, 23, 30),
			want: atPacific(2026, 3, 2, 0, 0),
		},
		{
			name: "a UTC clock is converted before the day is taken",
			from: atPacific(2026, 3, 1, 20, 0).UTC(),
			want: atPacific(2026, 3, 2, 0, 0),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := quotaResetAfter(tc.from)
			if !got.Equal(tc.want) {
				t.Fatalf("reset = %v, want %v", got, tc.want)
			}
			if !got.After(tc.from) {
				t.Fatal("the reset is not in the future, so the budget would roll over immediately")
			}
		})
	}
}

// This is the test the whole pacer exists for. A naive five-second poll spends
// 5 units every 5 seconds — 86,400 units a day against an allowance of 10,000 —
// and chat is dead by mid-morning. The pacer must choose a spacing that lasts.
func TestAFreshBudgetPacesToLastTheWholeDay(t *testing.T) {
	start := atPacific(2026, 3, 1, 0, 0).Add(time.Second)
	b := newBudget(DefaultQuotaUnits, DefaultQuotaReserve, fixedClock(start))

	got, ok := b.intervalFor(5*time.Second, 1)
	if !ok {
		t.Fatal("a fresh budget refused to poll")
	}

	// (10000 - 200 reserve) / 5 units = 1960 calls across ~24 hours.
	calls := (DefaultQuotaUnits - DefaultQuotaReserve) / QuotaCostListMessages
	want := (24 * time.Hour) / time.Duration(calls)
	if got < want-2*time.Second || got > want+2*time.Second {
		t.Fatalf("interval = %s, want about %s so the allowance lasts until the reset", got, want)
	}
	if got <= 5*time.Second {
		t.Fatalf("interval = %s: the API's suggested rate was taken at face value and the day's quota would be gone by mid-morning", got)
	}
}

func TestIntervalTakesTheSlowestOfEveryConstraint(t *testing.T) {
	// Half an hour before the reset with plenty of units left, the budget is
	// not the binding constraint and the API's own interval is.
	start := atPacific(2026, 3, 1, 23, 30)
	b := newBudget(DefaultQuotaUnits, DefaultQuotaReserve, fixedClock(start))

	tests := []struct {
		name        string
		apiInterval time.Duration
		idle        float64
		want        time.Duration
	}{
		{
			name:        "the floor applies when the API asks for something absurd",
			apiInterval: time.Second,
			idle:        1,
			want:        MinPollInterval,
		},
		{
			name:        "the API's own interval wins when it is slower than the floor",
			apiInterval: 12 * time.Second,
			idle:        1,
			want:        12 * time.Second,
		},
		{
			name:        "idleness multiplies the interval",
			apiInterval: 10 * time.Second,
			idle:        4,
			want:        40 * time.Second,
		},
		{
			name:        "the idle multiplier is capped",
			apiInterval: 10 * time.Second,
			idle:        1000,
			want:        time.Duration(IdleBackoffMax) * 10 * time.Second,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := b.intervalFor(tc.apiInterval, tc.idle)
			if !ok {
				t.Fatal("polling was refused with units to spare")
			}
			if got != tc.want {
				t.Fatalf("interval = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestASpentBudgetPausesRatherThanPolling(t *testing.T) {
	now := atPacific(2026, 3, 1, 16, 0)
	b := newBudget(1000, 100, fixedClock(now))
	b.spend(900) // everything but the reserve

	if _, ok := b.intervalFor(5*time.Second, 1); ok {
		t.Fatal("polling continued past the allowance; this is how chat dies at 4pm")
	}
	st := b.status()
	if !st.Paused {
		t.Fatal("the status did not report the pause")
	}
	if st.ResetAt.Before(now) {
		t.Fatal("the reported reset time is in the past")
	}
	if !st.Estimated {
		t.Fatal("the quota figure must declare itself an estimate")
	}
}

func TestTheReserveKeepsSendingPossibleAfterReadingStops(t *testing.T) {
	now := atPacific(2026, 3, 1, 16, 0)
	b := newBudget(1000, DefaultQuotaReserve, fixedClock(now))
	b.spend(1000 - DefaultQuotaReserve)

	if _, ok := b.intervalFor(5*time.Second, 1); ok {
		t.Fatal("reading did not stop at the reserve")
	}
	if !b.allow(QuotaCostSendMessage) {
		t.Fatal("the reserve did not survive for a send; the operator could not reply")
	}
}

func TestAReserveLargerThanTheAllowanceIsIgnoredRatherThanFatal(t *testing.T) {
	now := atPacific(2026, 3, 1, 0, 1)
	b := newBudget(100, 500, fixedClock(now))
	if _, ok := b.intervalFor(5*time.Second, 1); !ok {
		t.Fatal("a misconfigured reserve disabled chat entirely; it should degrade to no reserve")
	}
}

func TestTheBudgetRollsOverAtTheReset(t *testing.T) {
	now := atPacific(2026, 3, 1, 23, 0)
	clock := now
	b := newBudget(1000, 0, func() time.Time { return clock })
	b.spend(1000)

	if _, ok := b.intervalFor(5*time.Second, 1); ok {
		t.Fatal("a spent budget kept polling")
	}

	clock = atPacific(2026, 3, 2, 0, 1)
	if _, ok := b.intervalFor(5*time.Second, 1); !ok {
		t.Fatal("the budget did not roll over after the reset")
	}
	if st := b.status(); st.Used != 0 || st.Paused {
		t.Fatalf("status after rollover = %+v, want a clean day", st)
	}
}

func TestPauseTakesThePlatformsWordOverOurEstimate(t *testing.T) {
	now := atPacific(2026, 3, 1, 10, 0)
	b := newBudget(DefaultQuotaUnits, DefaultQuotaReserve, fixedClock(now))
	// Our own tally says there is plenty left...
	if _, ok := b.intervalFor(5*time.Second, 1); !ok {
		t.Fatal("a fresh budget refused to poll")
	}
	// ...but YouTube returned quotaExceeded, which is the authority.
	b.pause()
	if _, ok := b.intervalFor(5*time.Second, 1); ok {
		t.Fatal("polling continued after the platform said the quota was gone")
	}
}

func TestASustainableIntervalMayExceedTheIdleCeiling(t *testing.T) {
	// Very little left, a long time until the reset: the correct answer is to
	// poll slower than MaxPollInterval rather than to stop.
	now := atPacific(2026, 3, 1, 1, 0)
	// The reserve is stated rather than left at zero: zero means "unset" to
	// clampQuota now, exactly as it always has to NewYouTube, so asking for no
	// reserve at all gets the default. The property under test is the spacing
	// when very few READS remain, which fifty units of reserve expresses just as
	// well as none.
	b := newBudget(1000, 50, fixedClock(now))
	b.spend(900) // 10 calls left across 23 hours

	got, ok := b.intervalFor(5*time.Second, 1)
	if !ok {
		t.Fatal("polling stopped while units remained")
	}
	if got <= MaxPollInterval {
		t.Fatalf("interval = %s; with 10 calls left across 23 hours it must stretch past %s rather than burn out", got, MaxPollInterval)
	}
}
